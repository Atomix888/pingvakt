package snmp

import (
	"net"
	"sort"

	"github.com/gosnmp/gosnmp"
)

// ArpEntry är en rad i enhetens ARP-tabell.
type ArpEntry struct {
	IP  string `json:"ip"`
	MAC string `json:"mac"`
	If  int    `json:"if,omitempty"`
}

const (
	oidIpNetToMediaPhys    = ".1.3.6.1.2.1.4.22.1.2" // ipNetToMediaPhysAddress.ifIndex.a.b.c.d
	oidIpNetToPhysicalPhys = ".1.3.6.1.2.1.4.35.1.4" // ipNetToPhysicalPhysAddress.ifIndex.type.len.addr…
	maxArp                 = 8192
)

// pollArp läser ARP-tabellen (gamla tabellen först, annars den nyare IP-MIB-tabellen) – bara IPv4.
func pollArp(walk func(string, func(gosnmp.SnmpPDU) error) error) []ArpEntry {
	seen := map[string]ArpEntry{}
	add := func(ifIdx int, ip net.IP, b []byte) {
		mac := macOf(b)
		if ip == nil || mac == "" || mac == "00:00:00:00:00:00" || mac == "ff:ff:ff:ff:ff:ff" || len(seen) >= maxArp {
			return
		}
		seen[ip.String()] = ArpEntry{IP: ip.String(), MAC: mac, If: ifIdx}
	}
	_ = walk(oidIpNetToMediaPhys, func(v gosnmp.SnmpPDU) error {
		x := suffix(v.Name, oidIpNetToMediaPhys)
		b, ok := v.Value.([]byte)
		if len(x) != 5 || !ok {
			return nil
		}
		add(x[0], net.IPv4(byte(x[1]), byte(x[2]), byte(x[3]), byte(x[4])).To4(), b)
		return nil
	})
	if len(seen) == 0 {
		_ = walk(oidIpNetToPhysicalPhys, func(v gosnmp.SnmpPDU) error {
			x := suffix(v.Name, oidIpNetToPhysicalPhys)
			b, ok := v.Value.([]byte)
			// ifIndex, adresstyp (1 = ipv4), längd (4), a.b.c.d
			if len(x) != 7 || !ok || x[1] != 1 || x[2] != 4 {
				return nil
			}
			add(x[0], net.IPv4(byte(x[3]), byte(x[4]), byte(x[5]), byte(x[6])).To4(), b)
			return nil
		})
	}
	out := make([]ArpEntry, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out
}
