package main

import (
	"testing"
	"time"
)

func TestIPAMStatsAndNextFree(t *testing.T) {
	sn := &Subnet{ID: "s", CIDR: "10.0.0.0/24", Gateway: "10.0.0.1", DHCPFrom: "10.0.0.100", DHCPTo: "10.0.0.199"}
	n, err := parseSubnet(sn.CIDR)
	if err != nil {
		t.Fatal(err)
	}
	tnow := now()
	addrs := map[string]*IPAddr{
		"10.0.0.2":   {IP: "10.0.0.2", Last: tnow},
		"10.0.0.3":   {IP: "10.0.0.3", Reserved: true},
		"10.0.0.4":   {IP: "10.0.0.4", Last: tnow - int64(40*24*time.Hour/time.Millisecond)}, // gammal = ledig
		"10.0.0.150": {IP: "10.0.0.150", Last: tnow, Conflict: tnow},
	}
	st := stats(sn, n, addrs)
	if st.Size != 254 || st.Used != 4 || st.Reserved != 1 || st.Active != 2 || st.Conflicts != 1 || st.DHCPSize != 100 || st.DHCPUsed != 1 {
		t.Fatalf("stats: %+v", st)
	}
	if ip := nextFree(sn, n, addrs); ip != "10.0.0.4" {
		t.Fatalf("next free = %s, want 10.0.0.4", ip)
	}
	if _, err := parseSubnet("10.0.0.0/8"); err == nil {
		t.Fatal("a /8 should be refused")
	}
}

func TestIPAMConflict(t *testing.T) {
	ipam.mu.Lock()
	n, _ := parseSubnet("192.168.5.0/24")
	ipam.subnets["c"] = &Subnet{ID: "c", CustomerID: "k", CIDR: "192.168.5.0/24"}
	ipam.nets["c"], ipam.addrs["c"] = n, map[string]*IPAddr{}
	ipam.mu.Unlock()
	defer func() {
		ipam.mu.Lock()
		delete(ipam.subnets, "c")
		delete(ipam.nets, "c")
		delete(ipam.addrs, "c")
		ipam.mu.Unlock()
	}()
	s := &Server{}
	s.ipamObserve("k", []ipObs{{IP: "192.168.5.10", MAC: "AA:BB:CC:00:00:01", Src: "arp"}, {IP: "10.1.1.1", MAC: "aa:bb:cc:00:00:09"}})
	s.ipamObserve("k", []ipObs{{IP: "192.168.5.10", MAC: "aa:bb:cc:00:00:02", Host: "printer", Src: "scan"}})
	ipam.mu.Lock()
	a := ipam.addrs["c"]["192.168.5.10"]
	n2 := len(ipam.addrs["c"])
	ipam.mu.Unlock()
	if n2 != 1 || a == nil || a.MAC != "aa:bb:cc:00:00:02" || a.PrevMAC != "aa:bb:cc:00:00:01" || a.Conflict == 0 || a.Host != "printer" || a.Src != "scan" {
		t.Fatalf("addr: %+v (n=%d)", a, n2)
	}
}
