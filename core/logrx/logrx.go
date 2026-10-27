// Package logrx tar emot syslog-meddelanden och SNMP-traps från nätverksutrustning.
package logrx

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gosnmp/gosnmp"
)

// Message är ett mottaget meddelande.
type Message struct {
	T    int64  `json:"t"`
	Src  string `json:"src"`  // avsändarens IP-adress
	Kind string `json:"kind"` // syslog eller trap
	Sev  int    `json:"sev"`  // 0 = emergency … 7 = debug
	Fac  int    `json:"fac,omitempty"`
	Host string `json:"host,omitempty"`
	App  string `json:"app,omitempty"`
	Text string `json:"text"`
}

// SevNames är syslogs allvarlighetsgrader.
var SevNames = []string{"emerg", "alert", "crit", "err", "warning", "notice", "info", "debug"}

var (
	rePRI  = regexp.MustCompile(`^<(\d{1,3})>`)
	re3164 = regexp.MustCompile(`^([A-Z][a-z]{2} [ \d]\d \d\d:\d\d:\d\d) (\S+) (.*)$`)
	reTag  = regexp.MustCompile(`^([^\s:\[]{1,48})(\[\d+\])?: ?(.*)$`)
)

// ParseSyslog tolkar ett syslog-paket (RFC 3164 eller RFC 5424, med överseende för vanliga avvikelser).
func ParseSyslog(b []byte, src string) Message {
	m := Message{T: time.Now().UnixMilli(), Src: src, Kind: "syslog", Sev: 5, Fac: 1}
	s := strings.TrimRight(string(b), "\r\n\x00 ")
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "?")
	}
	if p := rePRI.FindStringSubmatch(s); p != nil {
		pri, _ := strconv.Atoi(p[1])
		if pri <= 191 {
			m.Fac, m.Sev = pri/8, pri%8
		}
		s = s[len(p[0]):]
	}
	if strings.HasPrefix(s, "1 ") {
		// RFC 5424: VERSION TIMESTAMP HOST APP PROCID MSGID [SD] MSG
		f := strings.SplitN(s, " ", 7)
		if len(f) >= 6 {
			m.Host, m.App = nilv(f[2]), nilv(f[3])
			rest := ""
			if len(f) == 7 {
				rest = f[6]
			}
			if strings.HasPrefix(rest, "-") {
				rest = strings.TrimPrefix(strings.TrimPrefix(rest, "-"), " ")
			} else if strings.HasPrefix(rest, "[") {
				if i := strings.Index(rest, "] "); i >= 0 {
					rest = rest[i+2:]
				} else if strings.HasSuffix(rest, "]") {
					rest = ""
				}
			}
			m.Text = strings.TrimPrefix(rest, "\ufeff")
			return clean(m)
		}
	}
	if p := re3164.FindStringSubmatch(s); p != nil {
		m.Host, s = p[2], p[3]
	}
	if p := reTag.FindStringSubmatch(s); p != nil && !strings.Contains(p[1], "=") {
		m.App, s = p[1], p[3]
	}
	m.Text = s
	return clean(m)
}

func nilv(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

func clean(m Message) Message {
	m.Text = strings.TrimSpace(m.Text)
	if len(m.Text) > 2000 {
		m.Text = m.Text[:2000]
	}
	if len(m.Host) > 120 {
		m.Host = m.Host[:120]
	}
	if len(m.App) > 64 {
		m.App = m.App[:64]
	}
	return m
}

var trapNames = map[string]string{
	".1.3.6.1.6.3.1.1.5.1": "coldStart", ".1.3.6.1.6.3.1.1.5.2": "warmStart", ".1.3.6.1.6.3.1.1.5.3": "linkDown",
	".1.3.6.1.6.3.1.1.5.4": "linkUp", ".1.3.6.1.6.3.1.1.5.5": "authenticationFailure", ".1.3.6.1.6.3.1.1.5.6": "egpNeighborLoss",
}

var varNames = map[string]string{
	".1.3.6.1.2.1.2.2.1.1": "ifIndex", ".1.3.6.1.2.1.2.2.1.2": "ifDescr", ".1.3.6.1.2.1.31.1.1.1.1": "ifName",
	".1.3.6.1.2.1.2.2.1.7": "ifAdminStatus", ".1.3.6.1.2.1.2.2.1.8": "ifOperStatus", ".1.3.6.1.2.1.31.1.1.1.18": "ifAlias",
	".1.3.6.1.2.1.1.5.0": "sysName",
}

const oidTrapOID = ".1.3.6.1.6.3.1.1.4.1.0"
const oidUptime = ".1.3.6.1.2.1.1.3.0"

// TrapMessage gör ett läsbart meddelande av en trap.
func TrapMessage(p *gosnmp.SnmpPacket, src string) Message {
	m := Message{T: time.Now().UnixMilli(), Src: src, Kind: "trap", Sev: 5}
	trap := ""
	if p.Version == gosnmp.Version1 {
		generic := []string{"coldStart", "warmStart", "linkDown", "linkUp", "authenticationFailure", "egpNeighborLoss", "enterpriseSpecific"}
		if p.GenericTrap >= 0 && p.GenericTrap < len(generic) {
			trap = generic[p.GenericTrap]
		}
		if p.GenericTrap == 6 {
			trap = fmt.Sprintf("%s.%d", p.Enterprise, p.SpecificTrap)
		}
		if p.AgentAddress != "" && p.AgentAddress != "0.0.0.0" {
			m.Host = p.AgentAddress
		}
	}
	var parts []string
	for _, v := range p.Variables {
		name := "." + strings.TrimPrefix(v.Name, ".")
		switch {
		case name == oidTrapOID:
			o := "." + strings.TrimPrefix(fmt.Sprint(v.Value), ".")
			if n, ok := trapNames[o]; ok {
				trap = n
			} else {
				trap = o
			}
			continue
		case name == oidUptime:
			continue
		}
		label := name
		for base, n := range varNames {
			if name == base || strings.HasPrefix(name, base+".") {
				label = n
				break
			}
		}
		val := v.Value
		if b, ok := val.([]byte); ok {
			val = strings.TrimRight(string(b), "\x00")
		}
		if label == "sysName" {
			m.Host = fmt.Sprint(val)
		}
		parts = append(parts, fmt.Sprintf("%s=%v", label, val))
		if len(parts) >= 12 {
			break
		}
	}
	switch trap {
	case "linkDown", "authenticationFailure":
		m.Sev = 4
	case "coldStart", "warmStart", "linkUp":
		m.Sev = 5
	}
	m.App = trap
	m.Text = strings.TrimSpace(trap + " " + strings.Join(parts, " "))
	return clean(m)
}

// Receiver lyssnar efter syslog och traps.
type Receiver struct {
	mu        sync.Mutex
	out       func(Message)
	limit     int // högst så här många meddelanden per sekund (övriga kastas)
	window    int64
	count     int
	Dropped   int64
	Community string // om satt: traps med annan community ignoreras
}

// NewReceiver skapar en mottagare som lämnar meddelanden till out.
func NewReceiver(out func(Message)) *Receiver { return &Receiver{out: out, limit: 200} }

func (r *Receiver) deliver(m Message) {
	r.mu.Lock()
	sec := time.Now().Unix()
	if sec != r.window {
		r.window, r.count = sec, 0
	}
	r.count++
	if r.count > r.limit {
		r.Dropped++
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	r.out(m)
}

// ListenSyslog tar emot syslog över UDP tills ctx avslutas.
func (r *Receiver) ListenSyslog(ctx context.Context, addr string) error {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		pc.Close()
	}()
	go func() {
		buf := make([]byte, 8192)
		for {
			n, a, err := pc.ReadFrom(buf)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				time.Sleep(100 * time.Millisecond)
				continue
			}
			host, _, _ := net.SplitHostPort(a.String())
			r.deliver(ParseSyslog(append([]byte(nil), buf[:n]...), host))
		}
	}()
	return nil
}

// ListenTraps tar emot SNMP-traps (v1/v2c) över UDP tills ctx avslutas.
func (r *Receiver) ListenTraps(ctx context.Context, addr string) error {
	tl := gosnmp.NewTrapListener()
	tl.Params = &gosnmp.GoSNMP{Version: gosnmp.Version2c, Community: "public", Logger: gosnmp.NewLogger(nil)}
	tl.OnNewTrap = func(p *gosnmp.SnmpPacket, a *net.UDPAddr) {
		if r.Community != "" && p.Community != r.Community {
			return
		}
		r.deliver(TrapMessage(p, a.IP.String()))
	}
	errc := make(chan error, 1)
	go func() { errc <- tl.Listen(addr) }()
	select {
	case err := <-errc:
		return err
	case <-tl.Listening():
	case <-time.After(3 * time.Second):
	}
	go func() {
		<-ctx.Done()
		tl.Close()
	}()
	return nil
}
