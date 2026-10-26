package netscan

import (
	"context"
	"testing"
	"time"
)

func TestHosts(t *testing.T) {
	h, err := Hosts("192.168.1.0/24")
	if err != nil || len(h) != 254 || h[0].String() != "192.168.1.1" || h[253].String() != "192.168.1.254" {
		t.Fatalf("/24: %v %d", err, len(h))
	}
	if h, _ := Hosts("10.0.0.10-10.0.0.12"); len(h) != 3 {
		t.Fatal("intervall")
	}
	if _, err := Hosts("10.0.0.0/8"); err == nil {
		t.Fatal("för stort nät ska ge fel")
	}
}

func TestParseARP(t *testing.T) {
	win := "\r\nInterface: 192.168.1.10 --- 0xb\r\n  Internet Address      Physical Address      Type\r\n  192.168.1.1           24-a4-3c-01-02-03     dynamic\r\n  192.168.1.255         ff-ff-ff-ff-ff-ff     static\r\n"
	mac := "? (192.168.1.20) at 0:c:29:a:b:c on en0 ifscope [ethernet]\n? (192.168.1.21) at (incomplete) on en0 ifscope [ethernet]\n"
	m := parseARP(win + mac)
	if m["192.168.1.1"] != "24:a4:3c:01:02:03" || m["192.168.1.20"] != "00:0c:29:0a:0b:0c" || len(m) != 2 {
		t.Fatalf("%v", m)
	}
	if Vendor("24:A4:3C:01:02:03") != "Ubiquiti" || Vendor("00-0c-29-0a-0b-0c") != "VMware" {
		t.Fatal("tillverkare")
	}
	if !isRandomMAC("da:a1:19:00:00:01") || isRandomMAC("24:a4:3c:00:00:01") || Vendor("da:a1:19:00:00:01") != "Private address" {
		t.Fatal("privat adress")
	}
}

func TestScanLocalhost(t *testing.T) {
	ds, err := Scan(context.Background(), "127.0.0.1", Options{PingTimeout: time.Second}, nil, nil)
	if err != nil || len(ds) != 1 || !ds[0].Self {
		t.Fatalf("%v %+v", err, ds)
	}
}
