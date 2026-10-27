package snmp

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/gosnmp/gosnmp"
)

// Neighbor är en granne som enheten ser via LLDP eller CDP.
type Neighbor struct {
	Proto      string `json:"proto"`           // lldp eller cdp
	LocalIf    int    `json:"lif"`             // ifIndex (CDP) eller LLDP-portnummer
	LocalPort  string `json:"lport,omitempty"` // portens namn enligt LLDP
	RemoteName string `json:"rname,omitempty"` // systemnamn / Device ID
	RemotePort string `json:"rport,omitempty"` // portens namn hos grannen
	RemoteDesc string `json:"rdesc,omitempty"` // systembeskrivning / plattform
	RemoteIP   string `json:"rip,omitempty"`   // hanteringsadress
	RemoteMAC  string `json:"rmac,omitempty"`  // chassi-ID om det är en MAC-adress
}

const (
	oidLldpLocPort  = ".1.0.8802.1.1.2.1.3.7.1"
	oidLldpRem      = ".1.0.8802.1.1.2.1.4.1.1"
	oidLldpRemMan   = ".1.0.8802.1.1.2.1.4.2.1"
	oidCdpCache     = ".1.3.6.1.4.1.9.9.23.1.2.1.1"
	oidLldpLocChass = ".1.0.8802.1.1.2.1.3.2.0"
)

// suffix ger talen efter bas-OID:n.
func suffix(name, base string) []int {
	rest := strings.TrimPrefix(strings.TrimPrefix(name, "."), strings.TrimPrefix(base, ".")+".")
	if rest == name {
		return nil
	}
	parts := strings.Split(rest, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

func macOf(b []byte) string {
	if len(b) != 6 {
		return ""
	}
	return net.HardwareAddr(b).String()
}

// textOrMAC visar ett ID som text om det är läsbart, annars som MAC-adress eller hex.
func textOrMAC(v gosnmp.SnmpPDU) string {
	b, ok := v.Value.([]byte)
	if !ok {
		return str(v)
	}
	printable := len(b) > 0
	for _, r := range string(b) {
		if r == 0 || (!unicode.IsPrint(r) && !unicode.IsSpace(r)) {
			printable = false
			break
		}
	}
	if printable {
		return strings.TrimSpace(string(b))
	}
	if m := macOf(b); m != "" {
		return m
	}
	return fmt.Sprintf("%x", b)
}

// pollNeighbors läser LLDP- och CDP-grannar.
func pollNeighbors(walk func(string, func(gosnmp.SnmpPDU) error) error) ([]Neighbor, string) {
	locPort := map[int]string{}
	_ = walk(oidLldpLocPort, func(v gosnmp.SnmpPDU) error {
		s := suffix(v.Name, oidLldpLocPort)
		if len(s) != 2 {
			return nil
		}
		switch s[0] {
		case 3: // lldpLocPortId
			if locPort[s[1]] == "" {
				locPort[s[1]] = textOrMAC(v)
			}
		case 4: // lldpLocPortDesc
			if t := strings.TrimSpace(str(v)); t != "" {
				locPort[s[1]] = t
			}
		}
		return nil
	})
	type key struct{ port, idx int }
	lldp := map[key]*Neighbor{}
	_ = walk(oidLldpRem, func(v gosnmp.SnmpPDU) error {
		s := suffix(v.Name, oidLldpRem)
		if len(s) != 4 {
			return nil
		}
		k := key{s[2], s[3]}
		n := lldp[k]
		if n == nil {
			n = &Neighbor{Proto: "lldp", LocalIf: s[2], LocalPort: locPort[s[2]]}
			lldp[k] = n
		}
		switch s[0] {
		case 5:
			if b, ok := v.Value.([]byte); ok {
				n.RemoteMAC = macOf(b)
			}
		case 7:
			n.RemotePort = textOrMAC(v)
		case 8:
			if t := strings.TrimSpace(str(v)); t != "" {
				n.RemotePort = t // portbeskrivningen är oftast mer läsbar än port-ID
			}
		case 9:
			n.RemoteName = strings.TrimSpace(str(v))
		case 10:
			n.RemoteDesc = strings.TrimSpace(str(v))
		}
		return nil
	})
	// hanteringsadresser: index = tid.port.idx.typ.längd.adress…
	_ = walk(oidLldpRemMan, func(v gosnmp.SnmpPDU) error {
		s := suffix(v.Name, oidLldpRemMan)
		if len(s) < 10 || s[0] != 3 || s[4] != 1 || s[5] != 4 {
			return nil
		}
		if n := lldp[key{s[2], s[3]}]; n != nil && n.RemoteIP == "" {
			n.RemoteIP = fmt.Sprintf("%d.%d.%d.%d", s[6], s[7], s[8], s[9])
		}
		return nil
	})
	var out []Neighbor
	for _, n := range lldp {
		if n.RemoteName == "" && n.RemoteMAC == "" && n.RemoteIP == "" {
			continue
		}
		out = append(out, *n)
	}
	cdp := map[key]*Neighbor{}
	_ = walk(oidCdpCache, func(v gosnmp.SnmpPDU) error {
		s := suffix(v.Name, oidCdpCache)
		if len(s) != 3 {
			return nil
		}
		k := key{s[1], s[2]}
		n := cdp[k]
		if n == nil {
			n = &Neighbor{Proto: "cdp", LocalIf: s[1]}
			cdp[k] = n
		}
		switch s[0] {
		case 4:
			if b, ok := v.Value.([]byte); ok && len(b) == 4 {
				n.RemoteIP = net.IP(b).String()
			}
		case 6:
			n.RemoteName = strings.TrimSpace(str(v))
		case 7:
			n.RemotePort = strings.TrimSpace(str(v))
		case 8:
			n.RemoteDesc = strings.TrimSpace(str(v))
		}
		return nil
	})
	for _, n := range cdp {
		// samma granne kan synas i både LLDP och CDP – behåll LLDP
		dup := false
		for _, o := range out {
			if o.Proto == "lldp" && strings.EqualFold(o.RemoteName, n.RemoteName) {
				dup = true
			}
		}
		if !dup && n.RemoteName != "" {
			out = append(out, *n)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LocalIf != out[j].LocalIf {
			return out[i].LocalIf < out[j].LocalIf
		}
		return out[i].RemoteName < out[j].RemoteName
	})
	return out, ""
}
