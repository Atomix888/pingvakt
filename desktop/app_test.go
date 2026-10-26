package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pingvakt/core/monitor"
	"pingvakt/core/probe"
)

func TestParseHostToken(t *testing.T) {
	cases := []struct {
		in   string
		host string
		kind probe.Kind
		port int
	}{
		{"1.1.1.1", "1.1.1.1", probe.KindICMP, 0},
		{"example.com:443", "example.com", probe.KindTCP, 443},
		{"tcp://10.0.0.1:22", "10.0.0.1", probe.KindTCP, 22},
		{"[2001:db8::1]:80", "2001:db8::1", probe.KindTCP, 80},
		{"2001:db8::1", "2001:db8::1", probe.KindICMP, 0},
		{"https://spelaretro.se/", "spelaretro.se", probe.KindICMP, 0},
		{"bad\"host", "", probe.KindICMP, 0},
	}
	for _, c := range cases {
		h, k, p := parseHostToken(c.in, AddInput{Kind: probe.KindICMP})
		if h != c.host || (h != "" && (k != c.kind || p != c.port)) {
			t.Errorf("%q => %q %s %d", c.in, h, k, p)
		}
	}
}

func TestWriteCSV(t *testing.T) {
	a := NewApp()
	a.mon = monitor.NewManager(monitor.DefaultSettings())
	defer a.mon.Close()
	tg := a.mon.Upsert(monitor.Target{Host: "127.0.0.1", Name: "Lokal åäö", Kind: probe.KindTCP, Port: 1, IntervalMs: 200, TimeoutMs: 100})
	time.Sleep(700 * time.Millisecond)
	p := filepath.Join(t.TempDir(), "x.csv")
	if err := a.writeCSV(p, []string{tg.ID}, 0); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	s := string(b)
	if !strings.HasPrefix(s, "\xef\xbb\xbfTid;Mål;Värd;Typ;Latens (ms);Förlorad") || !strings.Contains(s, "Lokal åäö;127.0.0.1;TCP 1;;1") {
		t.Fatalf("oväntad CSV:\n%s", s)
	}
}
