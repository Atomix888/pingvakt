package monitor

import (
	"testing"

	"pingvakt/core/probe"
)

func TestParseHostToken(t *testing.T) {
	cases := []struct {
		in   string
		kind probe.Kind
		host string
		want probe.Kind
		port int
	}{
		{"192.168.1.1", probe.KindICMP, "192.168.1.1", probe.KindICMP, 0},
		{"webb.se:443", probe.KindICMP, "webb.se", probe.KindTCP, 443},
		{"https://webb.se/login", probe.KindICMP, "https://webb.se/login", probe.KindHTTP, 0},
		{"webb.se", probe.KindHTTP, "https://webb.se", probe.KindHTTP, 0},
		{"kund.se.", probe.KindDNS, "kund.se", probe.KindDNS, 0},
	}
	for _, c := range cases {
		h, k, p := ParseHostToken(c.in, c.kind, 0)
		if h != c.host || k != c.want || p != c.port {
			t.Errorf("%q: fick %q %s %d", c.in, h, k, p)
		}
	}
}
