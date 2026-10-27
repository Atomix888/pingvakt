package main

import (
	"testing"

	"pingvakt/core/probe"
)

func TestCounterDelta(t *testing.T) {
	cases := []struct {
		cur, prev, want uint64
		ok              bool
	}{
		{1500, 1000, 500, true},
		{100, (1 << 32) - 400, 500, true}, // 32-bitars omslag
		{10, 5_000_000_000, 0, false},     // omstart av 64-bitarsräknare
	}
	for i, c := range cases {
		got, ok := counterDelta(c.cur, c.prev)
		if got != c.want || ok != c.ok {
			t.Errorf("fall %d: fick %d %v, ville ha %d %v", i, got, ok, c.want, c.ok)
		}
	}
	// en 32-bitarsräknare som minskar lite är en omstart, inte ett omslag
	if _, ok := counterDelta(900, 1000); ok {
		t.Error("liten minskning ska räknas som omstart")
	}
}

func TestResolveTemplate(t *testing.T) {
	tpl := builtinTemplates()[0]
	res := resolveTemplate(tpl, "10.0.0.1", []string{"10.0.0.53"}, map[string]bool{targetKey(probe.KindICMP, "1.1.1.1", 0): true})
	got := map[string]ResolvedTarget{}
	for _, r := range res {
		got[r.Name] = r
	}
	if r := got["Gateway"]; r.Host != "10.0.0.1" || r.Missing != "" {
		t.Errorf("gateway: %+v", r)
	}
	if r := got["DNS lookup"]; r.DNSServer != "10.0.0.53" {
		t.Errorf("dns: %+v", r)
	}
	if !got["Cloudflare DNS"].Exists {
		t.Error("1.1.1.1 finns redan")
	}
	res = resolveTemplate(tpl, "", nil, nil)
	if res[0].Missing != "{gateway}" || res[1].Missing != "{dns1}" {
		t.Errorf("saknade värden: %+v %+v", res[0], res[1])
	}
}

func TestNormTemplate(t *testing.T) {
	tp := Template{Name: " Kontor ", Targets: []TemplateTarget{{Host: "{gateway}"}, {Host: "https://x.se", Kind: probe.KindHTTP}, {Host: "  "}, {Host: "web.se:443"}}}
	if err := normTemplate(&tp); err != nil {
		t.Fatal(err)
	}
	if len(tp.Targets) != 3 || tp.Targets[0].Kind != probe.KindICMP || tp.Targets[2].Kind != probe.KindTCP || tp.Targets[2].Port != 443 {
		t.Errorf("%+v", tp.Targets)
	}
	if err := normTemplate(&Template{Name: "x"}); err == nil {
		t.Error("tom mall ska ge fel")
	}
}
