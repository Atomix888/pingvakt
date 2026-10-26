package core_test

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"pingvakt/core/alert"
	"pingvakt/core/monitor"
	"pingvakt/core/probe"
	"pingvakt/core/trace"
)

func TestICMPLocalhost(t *testing.T) {
	p := probe.New()
	defer p.Close()
	r := p.Do(context.Background(), probe.Request{Host: "127.0.0.1", Kind: probe.KindICMP, Timeout: time.Second})
	if !r.OK {
		t.Skipf("ICMP inte tillgängligt i den här miljön: %s", r.Err)
	}
	if r.RTT <= 0 || r.RTT > 500*time.Millisecond {
		t.Fatalf("orimlig RTT %v", r.RTT)
	}
}

func TestICMPTimeout(t *testing.T) {
	p := probe.New()
	defer p.Close()
	// 203.0.113.0/24 (TEST-NET-3) ska aldrig svara
	r := p.Do(context.Background(), probe.Request{Host: "203.0.113.9", Kind: probe.KindICMP, Timeout: 300 * time.Millisecond})
	if r.OK {
		t.Fatal("väntade inget svar")
	}
}

func TestTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	p := probe.New()
	r := p.Do(context.Background(), probe.Request{Host: "127.0.0.1", Kind: probe.KindTCP, Port: port, Timeout: time.Second})
	if !r.OK {
		t.Fatalf("TCP-ping misslyckades: %s", r.Err)
	}
	ln.Close()
	r = p.Do(context.Background(), probe.Request{Host: "127.0.0.1", Kind: probe.KindTCP, Port: port, Timeout: time.Second})
	if r.OK {
		t.Fatal("väntade stängd port")
	}
}

func TestTraceParse(t *testing.T) {
	cases := []struct {
		line string
		n    int
		addr string
		rtt  float64
	}{
		{"  1    <1 ms    <1 ms    <1 ms  192.168.1.1", 1, "192.168.1.1", 0.5},
		{"  2     8 ms     7 ms     9 ms  10.0.0.1", 2, "10.0.0.1", 7},
		{"  3     *        *        *     Tidsgränsen för begäran överskreds.", 3, "", -1},
		{" 4  172.16.0.1  12.345 ms", 4, "172.16.0.1", 12.345},
		{" 5  *", 5, "", -1},
	}
	for _, c := range cases {
		h, ok := trace.ParseLine(c.line)
		if !ok || h.N != c.n || h.Addr != c.addr || h.RTT != c.rtt {
			t.Errorf("%q => %+v", c.line, h)
		}
	}
	if _, ok := trace.ParseLine("Tracing route to x [1.2.3.4]"); ok {
		t.Error("rubrikrad tolkades som hopp")
	}
}

func samples(rtts ...float32) []monitor.Sample {
	out := make([]monitor.Sample, len(rtts))
	for i, r := range rtts {
		out[i] = monitor.Sample{T: int64(i), RTT: r}
	}
	return out
}

func TestAlertEvaluate(t *testing.T) {
	down := alert.Alert{Condition: alert.CondDown, Count: 3, Samples: 3}
	down.Normalize()
	if met, _ := down.Evaluate(samples(1, -1, -1)); met {
		t.Error("2 av 3 ska inte utlösa")
	}
	if met, _ := down.Evaluate(samples(1, -1, -1, -1)); !met {
		t.Error("3 av 3 ska utlösa")
	}
	lat := alert.Alert{Condition: alert.CondLatency, Threshold: 100, Count: 3, Samples: 5}
	lat.Normalize()
	if met, _ := lat.Evaluate(samples(10, 200, 20, 150, 30)); met {
		t.Error("2 höga av 5 ska inte utlösa")
	}
	if met, _ := lat.Evaluate(samples(10, 200, -1, 150, 30)); !met {
		t.Error("2 höga + 1 förlorat ska utlösa")
	}
	loss := alert.Alert{Condition: alert.CondLoss, Threshold: 10, Samples: 10}
	loss.Normalize()
	if met, _ := loss.Evaluate(samples(1, 1, 1, 1, 1, 1, 1, 1, 1, -1)); met {
		t.Error("10 % ska inte överstiga 10 %")
	}
	if met, _ := loss.Evaluate(samples(1, 1, 1, 1, 1, 1, 1, 1, -1, -1)); !met {
		t.Error("20 % ska utlösa")
	}
}

func TestMonitorAndAlerts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port

	m := monitor.NewManager(monitor.Settings{StatsWindow: 10, DownAfter: 2, HistorySize: 1000})
	defer m.Close()
	events := make(chan alert.Event, 10)
	eng := alert.NewEngine(m, nil, "", func(e alert.Event) { events <- e })
	a := alert.Alert{ID: "d", Name: "Nere", Condition: alert.CondDown, Count: 2, Samples: 2, OnClear: true}
	a.Normalize()
	eng.SetAlerts([]alert.Alert{a})

	tg := m.Upsert(monitor.Target{Host: "127.0.0.1", Kind: probe.KindTCP, Port: port, IntervalMs: 200, TimeoutMs: 200, AlertIDs: []string{"d"}})
	time.Sleep(700 * time.Millisecond)
	st := m.Snapshot()[tg.ID]
	if st.Status != monitor.StatusUp || st.Sent < 2 {
		t.Fatalf("väntade up, fick %+v", st)
	}
	ln.Close() // nu blir porten stängd => förlorade
	select {
	case e := <-events:
		if e.Kind != "fire" {
			t.Fatalf("väntade fire, fick %s", e.Kind)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("larmet utlöstes inte")
	}
	if st := m.Snapshot()[tg.ID]; st.Status != monitor.StatusDown {
		t.Fatalf("väntade down, fick %s", st.Status)
	}
	// återställ porten
	ln2, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Skip("kunde inte återöppna porten")
	}
	defer ln2.Close()
	go func() {
		for {
			c, err := ln2.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	select {
	case e := <-events:
		if e.Kind != "clear" {
			t.Fatalf("väntade clear, fick %s", e.Kind)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("larmet upphörde inte")
	}
	if h := m.History(tg.ID, 0); len(h) < 5 {
		t.Fatalf("för lite historik: %d", len(h))
	}
}
