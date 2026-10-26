package main

import (
	"context"
	"path/filepath"
	"testing"

	"pingvakt/core/agentproto"
	"pingvakt/core/alert"
	"pingvakt/core/monitor"
)

func TestIngestAndHistory(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO customers(id,name,created) VALUES('c1','Kund',1)`)
	_, _ = db.Exec(`INSERT INTO sites(id,customer_id,name,created) VALUES('s1','c1','Site',1)`)
	tok, h := newToken()
	_, _ = db.Exec(`INSERT INTO agents(id,site_id,name,token_hash,created) VALUES('a1','s1','Agent',?,1)`, h)
	rule := Rule{Alert: alert.Alert{ID: "r1", Name: "Nere", Condition: alert.CondDown, Count: 3, Samples: 3}, CustomerID: "c1"}
	rule.Normalize()
	if err := db.SaveRule(rule); err != nil {
		t.Fatal(err)
	}
	tg := Target{Target: monitor.Target{ID: "t1", Host: "10.0.0.1", AlertIDs: []string{"r1"}}, AgentID: "a1", Source: "server"}
	tg.Normalize()
	if err := db.SaveTarget(tg); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(db, "test", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if s.tokens[hashToken(tok)] != "a1" {
		t.Fatal("token hittades inte")
	}
	var events []alert.Event
	s.eng = alert.NewEngineFor(engineSource{s}, nil, "", func(e alert.Event) { events = append(events, e) })
	s.eng.SetAlerts([]alert.Alert{rule.Alert})

	base := now() - 10000
	rep := agentproto.Report{Agent: agentproto.AgentInfo{Kind: agentproto.KindAgent, Version: "x"}, ConfigVersion: 0}
	for i := 0; i < 5; i++ {
		r := float32(12.5)
		if i >= 2 {
			r = -1
		}
		rep.Samples = append(rep.Samples, agentproto.Sample{ID: "t1", T: base + int64(i)*1000, R: r})
	}
	rep.Samples = append(rep.Samples, agentproto.Sample{ID: "okänt", T: base, R: 1})
	resp, err := s.Ingest(context.Background(), "a1", "127.0.0.1", rep)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Config == nil || len(resp.Config.Targets) != 1 || resp.Customer != "Kund" {
		t.Fatalf("oväntat svar: %+v", resp)
	}
	st := s.snapshot(nil).Stats["t1"]
	if st.Status != monitor.StatusDown || st.Sent != 5 {
		t.Fatalf("statistik: %+v", st)
	}
	if len(events) != 1 || events[0].Kind != "fire" {
		t.Fatalf("larm: %+v", events)
	}
	h1, err := db.History("t1", base-1, now(), 7)
	if err != nil || h1.Mode != "raw" || len(h1.T) != 5 {
		t.Fatalf("rådata: %+v %v", h1, err)
	}
	h2, err := db.History("t1", now()-7*86400000, now(), 7)
	if err != nil || h2.Mode != "agg" || len(h2.T) == 0 {
		t.Fatalf("aggregat: %+v %v", h2, err)
	}
	var n, lost int
	for i := range h2.T {
		n += h2.N[i]
		lost += int(h2.Loss[i] * float64(h2.N[i]) / 100)
	}
	if n != 5 || lost != 3 {
		t.Fatalf("aggregat räknade fel: n=%d lost=%d", n, lost)
	}
}
