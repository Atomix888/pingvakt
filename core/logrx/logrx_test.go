package logrx

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
)

func TestParseSyslog(t *testing.T) {
	m := ParseSyslog([]byte("<187>Oct 27 07:40:01 core-sw dot1x[123]: Port 3 authentication failed\n"), "10.0.0.2")
	if m.Sev != 3 || m.Fac != 23 || m.Host != "core-sw" || m.App != "dot1x" || m.Text != "Port 3 authentication failed" {
		t.Errorf("3164: %+v", m)
	}
	m = ParseSyslog([]byte(`<165>1 2026-10-27T07:40:01Z fw01 sshd 991 ID47 [exampleSDID@32473 iut="3"] Accepted password for admin`), "10.0.0.1")
	if m.Sev != 5 || m.Host != "fw01" || m.App != "sshd" || m.Text != "Accepted password for admin" {
		t.Errorf("5424: %+v", m)
	}
	m = ParseSyslog([]byte("<14>UniFi U6-Pro: wevent: user disconnected"), "10.0.0.20")
	if m.Sev != 6 || m.Text == "" {
		t.Errorf("enkel: %+v", m)
	}
	m = ParseSyslog([]byte("utan prioritet"), "x")
	if m.Sev != 5 || m.Text != "utan prioritet" {
		t.Errorf("utan pri: %+v", m)
	}
}

func TestReceiver(t *testing.T) {
	got := make(chan Message, 10)
	r := NewReceiver(func(m Message) { got <- m })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.ListenSyslog(ctx, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	// hitta porten genom att lyssna på en fast port i stället
	cancel()
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	if err := r.ListenSyslog(ctx, "127.0.0.1:25514"); err != nil {
		t.Skip("porten upptagen:", err)
	}
	c, _ := net.Dial("udp", "127.0.0.1:25514")
	c.Write([]byte("<11>Oct 27 07:40:01 sw1 kernel: link down"))
	select {
	case m := <-got:
		if m.Sev != 3 || m.Host != "sw1" || m.Src != "127.0.0.1" {
			t.Errorf("%+v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("inget meddelande")
	}
	if err := r.ListenTraps(ctx, "127.0.0.1:21162"); err != nil {
		t.Skip("trapport:", err)
	}
	time.Sleep(200 * time.Millisecond)
	g := &gosnmp.GoSNMP{Target: "127.0.0.1", Port: 21162, Version: gosnmp.Version2c, Community: "public", Timeout: time.Second}
	if err := g.Connect(); err != nil {
		t.Fatal(err)
	}
	defer g.Conn.Close()
	_, err := g.SendTrap(gosnmp.SnmpTrap{Variables: []gosnmp.SnmpPDU{
		{Name: ".1.3.6.1.6.3.1.1.4.1.0", Type: gosnmp.ObjectIdentifier, Value: ".1.3.6.1.6.3.1.1.5.3"},
		{Name: ".1.3.6.1.2.1.2.2.1.1.7", Type: gosnmp.Integer, Value: 7},
		{Name: ".1.3.6.1.2.1.31.1.1.1.1.7", Type: gosnmp.OctetString, Value: "Gi1/0/7"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		if m.Kind != "trap" || m.App != "linkDown" || m.Sev != 4 || m.Text != "linkDown ifIndex=7 ifName=Gi1/0/7" {
			t.Errorf("%+v", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ingen trap")
	}
}
