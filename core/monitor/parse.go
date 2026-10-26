package monitor

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	"pingvakt/core/probe"
)

// ParseHostToken tolkar en värd som användaren skrivit in.
//   - "https://…" eller "http://…" (eller mättypen http) ger en HTTP(S)-kontroll
//   - mättypen dns ger ett DNS-uppslag av namnet
//   - "värd:port" ger TCP-ping
func ParseHostToken(tok string, kind probe.Kind, port int) (string, probe.Kind, int) {
	tok = strings.TrimSpace(tok)
	low := strings.ToLower(tok)
	if kind == probe.KindHTTP || strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
		u, err := url.Parse(probe.NormalizeURL(tok))
		if err != nil || u.Host == "" || strings.ContainsAny(tok, " \"'<>\\") {
			return "", probe.KindHTTP, 0
		}
		return u.String(), probe.KindHTTP, 0
	}
	for _, p := range []string{"tcp://", "icmp://", "dns://"} {
		if strings.HasPrefix(low, p) {
			tok = tok[len(p):]
		}
	}
	tok = strings.TrimSuffix(tok, "/")
	if kind == probe.KindDNS {
		tok = strings.TrimSuffix(tok, ".")
		if tok == "" || strings.ContainsAny(tok, " /\\\"'<>:") {
			return "", kind, 0
		}
		return tok, kind, 0
	}
	if kind != probe.KindTCP {
		kind = probe.KindICMP
	}
	if h, p, err := net.SplitHostPort(tok); err == nil {
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
			return h, probe.KindTCP, n
		}
	}
	tok = strings.Trim(tok, "[]")
	if tok == "" || strings.ContainsAny(tok, " /\\\"'<>") {
		return "", kind, port
	}
	return tok, kind, port
}

// KindLabel ger en kort text för mättypen.
func (t Target) KindLabel() string {
	switch t.Kind {
	case probe.KindTCP:
		return "TCP " + strconv.Itoa(t.Port)
	case probe.KindHTTP:
		if strings.HasPrefix(t.Host, "http://") {
			return "HTTP"
		}
		return "HTTPS"
	case probe.KindDNS:
		return "DNS"
	}
	return "ICMP"
}
