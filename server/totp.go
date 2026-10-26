package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"

	"pingvakt/core/i18n"
)

// ---------- TOTP (RFC 6238) ----------

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func newTOTPSecret() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return b32.EncodeToString(b)
}

func totpCode(secret string, t time.Time) string {
	key, err := b32.DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil {
		return ""
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(t.Unix()/30))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1000000)
}

// totpValid godtar koden för nuvarande, föregående och nästa 30-sekundersperiod.
func totpValid(secret, code string) bool {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 || secret == "" {
		return false
	}
	n := time.Now()
	for _, d := range []time.Duration{0, -30 * time.Second, 30 * time.Second} {
		if subtle.ConstantTimeCompare([]byte(totpCode(secret, n.Add(d))), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

func newRecoveryCodes() (codes []string, hashes string) {
	var hs []string
	for i := 0; i < 8; i++ {
		b := make([]byte, 5)
		_, _ = rand.Read(b)
		c := strings.ToLower(b32.EncodeToString(b))
		c = c[:4] + "-" + c[4:]
		codes = append(codes, c)
		hs = append(hs, recoveryHash(c))
	}
	return codes, strings.Join(hs, ",")
}

func recoveryHash(c string) string {
	h := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(c))))
	return hex.EncodeToString(h[:])
}

// ---------- tillstånd ----------

type totpState struct {
	mu      sync.Mutex
	pending map[string]pendingSecret // användar-ID -> hemlighet under aktivering
	tickets map[string]loginTicket   // inloggningsbiljett efter rätt lösenord
}

type pendingSecret struct {
	secret string
	exp    time.Time
}

type loginTicket struct {
	userID   string
	exp      time.Time
	attempts int
}

var totps = &totpState{pending: map[string]pendingSecret{}, tickets: map[string]loginTicket{}}

func (s *Server) userTOTP(id string) (secret, recovery string) {
	_ = s.db.QueryRow(`SELECT totp_secret, totp_recovery FROM users WHERE id=?`, id).Scan(&secret, &recovery)
	return
}

// ---------- API ----------

// handleTOTPSetup skapar en ny hemlighet och en QR-kod att skanna med en autentiseringsapp.
func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	sec := newTOTPSecret()
	totps.mu.Lock()
	totps.pending[u.ID] = pendingSecret{secret: sec, exp: time.Now().Add(10 * time.Minute)}
	totps.mu.Unlock()
	label := url.PathEscape("PingVakt:" + u.Email)
	uri := fmt.Sprintf("otpauth://totp/%s?secret=%s&issuer=PingVakt&digits=6&period=30", label, sec)
	png, err := qrcode.Encode(uri, qrcode.Medium, 220)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]string{"secret": sec, "uri": uri, "qr": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)})
}

func (s *Server) handleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var in struct{ Code string }
	if !readJSON(w, r, &in) {
		return
	}
	totps.mu.Lock()
	p, ok := totps.pending[u.ID]
	totps.mu.Unlock()
	if !ok || time.Now().After(p.exp) {
		httpErr(w, 400, "start over – the QR code has expired")
		return
	}
	if !totpValid(p.secret, in.Code) {
		httpErr(w, 400, "wrong code – check that the clock on your phone is correct")
		return
	}
	codes, hashes := newRecoveryCodes()
	if _, err := s.db.Exec(`UPDATE users SET totp_secret=?, totp_recovery=? WHERE id=?`, p.secret, hashes, u.ID); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	totps.mu.Lock()
	delete(totps.pending, u.ID)
	totps.mu.Unlock()
	s.audit(r, "sign-in", u.ID, i18n.T("enabled two-factor authentication"))
	writeJSON(w, map[string]any{"ok": true, "recovery": codes})
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var in struct{ Password string }
	if !readJSON(w, r, &in) {
		return
	}
	var hash string
	_ = s.db.QueryRow(`SELECT pw_hash FROM users WHERE id=?`, u.ID).Scan(&hash)
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password)) != nil {
		httpErr(w, 400, "wrong password")
		return
	}
	_, _ = s.db.Exec(`UPDATE users SET totp_secret='', totp_recovery='' WHERE id=?`, u.ID)
	s.audit(r, "sign-in", u.ID, i18n.T("turned off two-factor authentication"))
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleTOTPReset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, _ = s.db.Exec(`UPDATE users SET totp_secret='', totp_recovery='' WHERE id=?`, id)
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE user_id=?`, id)
	s.audit(r, "user", id, i18n.T("reset two-factor authentication"))
	writeJSON(w, map[string]bool{"ok": true})
}

// startTOTPLogin skapar en biljett när lösenordet stämmer men en kod behövs.
func startTOTPLogin(userID string) string {
	tok, _ := newToken()
	totps.mu.Lock()
	defer totps.mu.Unlock()
	for k, t := range totps.tickets {
		if time.Now().After(t.exp) {
			delete(totps.tickets, k)
		}
	}
	totps.tickets[tok] = loginTicket{userID: userID, exp: time.Now().Add(5 * time.Minute)}
	return tok
}

func (s *Server) handleLoginTOTP(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.limiter.blocked(ip) {
		httpErr(w, http.StatusTooManyRequests, "too many failed attempts – wait 15 minutes")
		return
	}
	var in struct{ Ticket, Code string }
	if !readJSON(w, r, &in) {
		return
	}
	totps.mu.Lock()
	t, ok := totps.tickets[in.Ticket]
	if ok {
		t.attempts++
		totps.tickets[in.Ticket] = t
	}
	totps.mu.Unlock()
	if !ok || time.Now().After(t.exp) || t.attempts > 5 {
		httpErr(w, 401, "the sign-in has expired – start over")
		return
	}
	sec, rec := s.userTOTP(t.userID)
	valid := totpValid(sec, in.Code)
	if !valid && strings.Contains(in.Code, "-") {
		// reservkod (engångs)
		h := recoveryHash(in.Code)
		var keep []string
		for _, x := range strings.Split(rec, ",") {
			if x == h && !valid {
				valid = true
				continue
			}
			if x != "" {
				keep = append(keep, x)
			}
		}
		if valid {
			_, _ = s.db.Exec(`UPDATE users SET totp_recovery=? WHERE id=?`, strings.Join(keep, ","), t.userID)
		}
	}
	if !valid {
		s.limiter.fail(ip)
		httpErr(w, 401, "wrong code")
		return
	}
	totps.mu.Lock()
	delete(totps.tickets, in.Ticket)
	totps.mu.Unlock()
	s.limiter.reset(ip)
	if err := s.createSession(w, r, t.userID); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	var u User
	_ = s.db.QueryRow(`SELECT id,email,name,role,customer_id,created,lang FROM users WHERE id=?`, t.userID).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.CustomerID, &u.Created, &u.Lang)
	s.auditAs(u.Email, ip, "sign-in", u.ID, i18n.T("signed in (2FA)"))
	writeJSON(w, u)
}

// ---------- ändringslogg ----------

const auditSchema = `
CREATE TABLE IF NOT EXISTS audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT, t INTEGER NOT NULL, user TEXT NOT NULL, ip TEXT NOT NULL,
  kind TEXT NOT NULL, ref TEXT NOT NULL, text TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_t ON audit(t);
`

// AuditEntry är en rad i ändringsloggen.
type AuditEntry struct {
	T    int64  `json:"t"`
	User string `json:"user"`
	IP   string `json:"ip"`
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
	Text string `json:"text"`
}

func (s *Server) auditAs(user, ip, kind, ref, text string) {
	_, _ = s.db.Exec(`INSERT INTO audit(t,user,ip,kind,ref,text) VALUES(?,?,?,?,?,?)`, now(), user, ip, kind, ref, text)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	q := "%" + strings.TrimSpace(r.URL.Query().Get("q")) + "%"
	rows, err := s.db.Query(`SELECT t,user,ip,kind,ref,text FROM audit WHERE user LIKE ? OR kind LIKE ? OR text LIKE ? ORDER BY t DESC LIMIT 500`, q, q, q)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var a AuditEntry
		if rows.Scan(&a.T, &a.User, &a.IP, &a.Kind, &a.Ref, &a.Text) == nil {
			out = append(out, a)
		}
	}
	writeJSON(w, out)
}
