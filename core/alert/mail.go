package alert

import (
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SMTP är inställningar för utgående e-post.
type SMTP struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Security   string `json:"security"` // starttls, tls, none
	Username   string `json:"username"`
	Password   string `json:"password"`
	From       string `json:"from"`
	MinMinutes int    `json:"minMinutes"` // skicka högst ett mejl per larm så här ofta; övriga samlas ihop
}

// Mailer skickar larm via e-post med begränsning av hur ofta.
type Mailer struct {
	mu      sync.Mutex
	cfg     SMTP
	last    map[string]time.Time
	pending map[string][]Event
	to      map[string][]string
	timers  map[string]*time.Timer
	OnError func(error)
}

// NewMailer skapar en Mailer.
func NewMailer(cfg SMTP) *Mailer {
	return &Mailer{cfg: cfg, last: map[string]time.Time{}, pending: map[string][]Event{}, to: map[string][]string{}, timers: map[string]*time.Timer{}}
}

// SetConfig byter SMTP-inställningar.
func (m *Mailer) SetConfig(c SMTP) {
	m.mu.Lock()
	m.cfg = c
	m.mu.Unlock()
}

// Queue lägger ett larm i kö och skickar direkt eller när spärrtiden gått ut.
func (m *Mailer) Queue(alertID string, to []string, ev Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending[alertID] = append(m.pending[alertID], ev)
	m.to[alertID] = to
	if _, waiting := m.timers[alertID]; waiting {
		return
	}
	wait := time.Duration(m.cfg.MinMinutes)*time.Minute - time.Since(m.last[alertID])
	if wait < 0 {
		wait = 0
	}
	m.timers[alertID] = time.AfterFunc(wait, func() { m.flush(alertID) })
}

func (m *Mailer) flush(alertID string) {
	m.mu.Lock()
	evs := m.pending[alertID]
	to := m.to[alertID]
	delete(m.pending, alertID)
	delete(m.timers, alertID)
	m.last[alertID] = time.Now()
	onErr := m.OnError
	m.mu.Unlock()
	if len(evs) == 0 {
		return
	}
	if err := m.SendNow(to, evs); err != nil && onErr != nil {
		onErr(err)
	}
}

// SendNow skickar ett mejl direkt med en eller flera händelser.
func (m *Mailer) SendNow(to []string, evs []Event) error {
	m.mu.Lock()
	cfg := m.cfg
	m.mu.Unlock()
	subject, body := compose(evs)
	return Send(cfg, to, subject, body)
}

func compose(evs []Event) (string, string) {
	first := evs[0]
	subject := fmt.Sprintf("[PingVakt] %s: %s – %s", first.KindText(), first.TargetName, first.AlertName)
	if len(evs) > 1 {
		subject = fmt.Sprintf("[PingVakt] %d larmhändelser (senast: %s %s)", len(evs), evs[len(evs)-1].KindText(), evs[len(evs)-1].TargetName)
	}
	var b strings.Builder
	b.WriteString("Larm från PingVakt\r\n\r\n")
	for _, e := range evs {
		fmt.Fprintf(&b, "%s  %s\r\n", time.UnixMilli(e.Time).Format("2006-01-02 15:04:05"), e.KindText())
		fmt.Fprintf(&b, "  Mål:    %s (%s)\r\n", e.TargetName, e.Host)
		fmt.Fprintf(&b, "  Larm:   %s\r\n", e.AlertName)
		fmt.Fprintf(&b, "  Detalj: %s\r\n\r\n", e.Detail)
	}
	return subject, b.String()
}

// Attachment är en bifogad fil.
type Attachment struct {
	Name        string
	ContentType string
	Data        []byte
}

// Send skickar ett textmejl via SMTP.
func Send(cfg SMTP, to []string, subject, body string) error {
	return SendWithAttachments(cfg, to, subject, body)
}

// SendWithAttachments skickar ett textmejl med bifogade filer via SMTP.
func SendWithAttachments(cfg SMTP, to []string, subject, body string, files ...Attachment) error {
	if cfg.Host == "" {
		return errors.New("SMTP-server saknas i inställningarna")
	}
	if cfg.From == "" {
		return errors.New("avsändaradress saknas i inställningarna")
	}
	port := cfg.Port
	if port == 0 {
		port = map[string]int{"tls": 465, "none": 25}[cfg.Security]
		if port == 0 {
			port = 587
		}
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port))
	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}

	var conn net.Conn
	var err error
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	if cfg.Security == "tls" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("kunde inte ansluta till %s: %w", addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if cfg.Security == "starttls" || cfg.Security == "" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("STARTTLS: %w", err)
			}
		} else if cfg.Security == "starttls" {
			return errors.New("servern stöder inte STARTTLS")
		}
	}
	if cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return fmt.Errorf("inloggning: %w", err)
		}
	}
	if err := c.Mail(cfg.From); err != nil {
		return err
	}
	for _, r := range to {
		if err := c.Rcpt(r); err != nil {
			return fmt.Errorf("mottagare %s: %w", r, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	msg := "From: " + cfg.From + "\r\n" +
		"To: " + strings.Join(to, ", ") + "\r\n" +
		"Subject: " + mimeHeader(subject) + "\r\n" +
		"Date: " + time.Now().Format(time.RFC1123Z) + "\r\n" +
		"MIME-Version: 1.0\r\n"
	if len(files) == 0 {
		msg += "Content-Type: text/plain; charset=UTF-8\r\n" +
			"Content-Transfer-Encoding: 8bit\r\n\r\n" + body
	} else {
		boundary := "pingvakt-" + strconv.FormatInt(time.Now().UnixNano(), 36)
		var b strings.Builder
		b.WriteString("Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n\r\n")
		b.WriteString("--" + boundary + "\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + body + "\r\n")
		for _, f := range files {
			ct := f.ContentType
			if ct == "" {
				ct = "application/octet-stream"
			}
			name := strings.NewReplacer("\"", "", "\r", "", "\n", "").Replace(f.Name)
			b.WriteString("--" + boundary + "\r\nContent-Type: " + ct + "; name=\"" + name + "\"\r\n" +
				"Content-Disposition: attachment; filename=\"" + name + "\"\r\nContent-Transfer-Encoding: base64\r\n\r\n")
			enc := base64.StdEncoding.EncodeToString(f.Data)
			for len(enc) > 76 {
				b.WriteString(enc[:76] + "\r\n")
				enc = enc[76:]
			}
			b.WriteString(enc + "\r\n")
		}
		b.WriteString("--" + boundary + "--\r\n")
		msg += b.String()
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func mimeHeader(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	for _, r := range s {
		if r > 127 {
			return "=?UTF-8?B?" + b64(s) + "?="
		}
	}
	return s
}
