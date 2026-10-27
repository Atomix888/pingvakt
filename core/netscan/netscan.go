// Package netscan hittar aktiva enheter i ett nät: ping-svep, vanliga TCP-portar,
// MAC-adress (ARP-tabellen), tillverkare (OUI), DNS-namn och NetBIOS-namn.
package netscan

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pingvakt/core/probe"
)

// Network är ett IPv4-nät som datorn sitter på.
type Network struct {
	Iface   string   `json:"iface"`
	Addr    string   `json:"addr"` // datorns adress i nätet
	CIDR    string   `json:"cidr"` // t.ex. 192.168.1.0/24
	Hosts   int      `json:"hosts"`
	Gateway string   `json:"gateway,omitempty"` // standard-gateway via gränssnittet
	DNS     []string `json:"dns,omitempty"`     // datorns DNS-servrar
}

// Device är en enhet som svarade.
type Device struct {
	IP     string  `json:"ip"`
	MAC    string  `json:"mac,omitempty"`
	Vendor string  `json:"vendor,omitempty"`
	Name   string  `json:"name,omitempty"`
	RTT    float64 `json:"rtt"` // ms, -1 om bara en port svarade
	Ports  []int   `json:"ports,omitempty"`
	Kind   string  `json:"kind,omitempty"` // router, printer, windows, nas, camera, ap, web, ssh, phone
	Self   bool    `json:"self,omitempty"`
}

// Ports är de TCP-portar som provas.
var Ports = []int{22, 23, 53, 80, 139, 443, 445, 515, 548, 554, 631, 3389, 5000, 8080, 8291, 8443, 9100, 62078}

// MaxHosts begränsar hur stort nät som får skannas (en /20).
const MaxHosts = 4096

// Networks returnerar datorns IPv4-nät (utan loopback och länklokala adresser).
func Networks() []Network {
	var out []Network
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil || ipn.IP.IsLinkLocalUnicast() || ipn.IP.IsLoopback() {
				continue
			}
			ones, bits := ipn.Mask.Size()
			if bits != 32 || ones < 8 || ones > 30 {
				continue
			}
			nw := &net.IPNet{IP: ipn.IP.Mask(ipn.Mask).To4(), Mask: ipn.Mask}
			out = append(out, Network{Iface: ifc.Name, Addr: ipn.IP.String(), CIDR: nw.String(), Hosts: hostCount(ones)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Iface < out[j].Iface })
	if len(out) > 0 {
		gws, dns := gatewayInfo()
		for i := range out {
			_, nw, _ := net.ParseCIDR(out[i].CIDR)
			if g := net.ParseIP(gws[out[i].Iface]); g != nil && nw != nil && nw.Contains(g) {
				out[i].Gateway = g.String()
			}
			out[i].DNS = dns
		}
	}
	return out
}

func hostCount(ones int) int {
	n := 1 << (32 - ones)
	if n > 2 {
		n -= 2
	}
	return n
}

// Hosts tolkar ett nät ("192.168.1.0/24", "10.0.0.5" eller "10.0.0.10-10.0.0.50")
// och returnerar adresserna som ska skannas.
func Hosts(spec string) ([]net.IP, error) {
	spec = strings.TrimSpace(spec)
	if a, b, ok := strings.Cut(spec, "-"); ok {
		from, to := net.ParseIP(strings.TrimSpace(a)).To4(), net.ParseIP(strings.TrimSpace(b)).To4()
		if from == nil || to == nil {
			return nil, errors.New("invalid range")
		}
		f, t := ip2u(from), ip2u(to)
		if t < f {
			f, t = t, f
		}
		if t-f+1 > MaxHosts {
			return nil, fmt.Errorf("too many addresses (max %d)", MaxHosts)
		}
		var out []net.IP
		for i := f; i <= t; i++ {
			out = append(out, u2ip(i))
		}
		return out, nil
	}
	if !strings.Contains(spec, "/") {
		ip := net.ParseIP(spec).To4()
		if ip == nil {
			return nil, errors.New("invalid network – use e.g. 192.168.1.0/24")
		}
		return []net.IP{ip}, nil
	}
	_, ipn, err := net.ParseCIDR(spec)
	if err != nil || ipn.IP.To4() == nil {
		return nil, errors.New("invalid network – use e.g. 192.168.1.0/24")
	}
	ones, _ := ipn.Mask.Size()
	if hostCount(ones) > MaxHosts {
		return nil, fmt.Errorf("the network is too large (max /20, %d addresses)", MaxHosts)
	}
	base := ip2u(ipn.IP.To4())
	n := uint32(1) << (32 - ones)
	var out []net.IP
	for i := uint32(0); i < n; i++ {
		if n > 2 && (i == 0 || i == n-1) {
			continue // nät- och broadcastadress
		}
		out = append(out, u2ip(base+i))
	}
	return out, nil
}

func ip2u(ip net.IP) uint32 {
	ip = ip.To4()
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}

func u2ip(u uint32) net.IP { return net.IPv4(byte(u>>24), byte(u>>16), byte(u>>8), byte(u)).To4() }

// Progress rapporterar hur långt skanningen kommit.
type Progress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
	Found int `json:"found"`
}

// Options styr skanningen.
type Options struct {
	PingTimeout time.Duration // standard 800 ms
	PortTimeout time.Duration // standard 600 ms
	Workers     int           // standard 64
	NoPorts     bool
}

// Scan skannar nätet och anropar onDevice för varje enhet som svarar.
// Resultatet är sorterat på IP-adress.
func Scan(ctx context.Context, spec string, opt Options, onDevice func(Device), onProgress func(Progress)) ([]Device, error) {
	hosts, err := Hosts(spec)
	if err != nil {
		return nil, err
	}
	if opt.PingTimeout <= 0 {
		opt.PingTimeout = 800 * time.Millisecond
	}
	if opt.PortTimeout <= 0 {
		opt.PortTimeout = 600 * time.Millisecond
	}
	if opt.Workers <= 0 {
		opt.Workers = 64
	}
	selfIPs := map[string]string{} // ip -> mac
	if ifs, err := net.Interfaces(); err == nil {
		for _, ifc := range ifs {
			addrs, _ := ifc.Addrs()
			for _, a := range addrs {
				if ipn, ok := a.(*net.IPNet); ok {
					selfIPs[ipn.IP.String()] = ifc.HardwareAddr.String()
				}
			}
		}
	}
	pinger := probe.New()
	defer pinger.Close()
	sem := make(chan struct{}, 256) // samtidiga TCP-försök
	var (
		mu      sync.Mutex
		found   []Device
		done    int32
		wg      sync.WaitGroup
		jobs    = make(chan net.IP)
		lastRep time.Time
	)
	report := func(force bool) {
		if onProgress == nil {
			return
		}
		mu.Lock()
		if !force && time.Since(lastRep) < 300*time.Millisecond {
			mu.Unlock()
			return
		}
		lastRep = time.Now()
		p := Progress{Done: int(atomic.LoadInt32(&done)), Total: len(hosts), Found: len(found)}
		mu.Unlock()
		onProgress(p)
	}
	worker := func() {
		defer wg.Done()
		for ip := range jobs {
			if ctx.Err() != nil {
				atomic.AddInt32(&done, 1)
				continue
			}
			d, alive := probeHost(ctx, pinger, ip, opt, sem)
			if mac, ok := selfIPs[d.IP]; ok {
				d.Self, alive = true, true
				d.MAC = mac
			}
			atomic.AddInt32(&done, 1)
			if alive {
				mu.Lock()
				found = append(found, d)
				mu.Unlock()
			}
			report(false)
		}
	}
	for i := 0; i < opt.Workers; i++ {
		wg.Add(1)
		go worker()
	}
	for _, ip := range hosts {
		select {
		case jobs <- ip:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()

	// MAC-adresser från ARP-tabellen (fylld av pingen), namn och tillverkare
	arp := arpTable(ctx)
	var nwg sync.WaitGroup
	nsem := make(chan struct{}, 32)
	for i := range found {
		d := &found[i]
		if d.MAC == "" {
			d.MAC = arp[d.IP]
		}
		d.MAC = normMAC(d.MAC)
		d.Vendor = Vendor(d.MAC)
		nwg.Add(1)
		nsem <- struct{}{}
		go func(d *Device) {
			defer nwg.Done()
			defer func() { <-nsem }()
			d.Name = lookupName(ctx, d.IP, d.Ports)
		}(d)
	}
	nwg.Wait()
	for i := range found {
		found[i].Kind = guessKind(found[i])
	}
	sort.Slice(found, func(i, j int) bool { return ip2u(net.ParseIP(found[i].IP)) < ip2u(net.ParseIP(found[j].IP)) })
	if onDevice != nil {
		for _, d := range found {
			onDevice(d)
		}
	}
	report(true)
	return found, ctx.Err()
}

// probeHost pingar och provar portar. En värd räknas som aktiv om den svarar på
// ping eller på någon port (även "connection refused" betyder att den finns).
func probeHost(ctx context.Context, p *probe.Pinger, ip net.IP, opt Options, sem chan struct{}) (Device, bool) {
	d := Device{IP: ip.String(), RTT: -1}
	alive := false
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for try := 0; try < 2; try++ {
			r := p.Do(ctx, probe.Request{Host: d.IP, Kind: probe.KindICMP, Timeout: opt.PingTimeout, Size: 32})
			if r.OK {
				mu.Lock()
				d.RTT, alive = float64(r.RTT.Microseconds())/1000, true
				mu.Unlock()
				return
			}
		}
	}()
	if !opt.NoPorts {
		for _, port := range Ports {
			wg.Add(1)
			go func(port int) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-sem }()
				dl := net.Dialer{Timeout: opt.PortTimeout}
				c, err := dl.DialContext(ctx, "tcp", net.JoinHostPort(d.IP, fmt.Sprint(port)))
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					_ = c.Close()
					d.Ports = append(d.Ports, port)
					alive = true
				} else if isRefused(err) {
					alive = true
				}
			}(port)
		}
	}
	wg.Wait()
	sort.Ints(d.Ports)
	return d, alive
}

func isRefused(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "refused") || strings.Contains(s, "actively refused") || strings.Contains(s, "reset by peer")
}

// lookupName försöker med omvänd DNS och därefter NetBIOS (Windows-datorer, NAS).
func lookupName(ctx context.Context, ip string, ports []int) string {
	c, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	if names, err := net.DefaultResolver.LookupAddr(c, ip); err == nil && len(names) > 0 {
		return strings.TrimSuffix(names[0], ".")
	}
	if n := netbiosName(ip, 800*time.Millisecond); n != "" {
		return n
	}
	return ""
}

func guessKind(d Device) string {
	has := func(p ...int) bool {
		for _, x := range p {
			for _, y := range d.Ports {
				if x == y {
					return true
				}
			}
		}
		return false
	}
	v := strings.ToLower(d.Vendor)
	switch {
	case has(9100, 515, 631) || containsAny(v, "brother", "canon", "epson", "lexmark", "kyocera", "ricoh", "xerox", "konica", "zebra"):
		return "printer"
	case has(554) || containsAny(v, "hikvision", "axis", "dahua", "hanwha", "vivotek"):
		return "camera"
	case has(8291) || containsAny(v, "mikrotik", "fortinet", "juniper", "draytek", "teltonika", "keenetic", "palo alto"):
		return "router"
	case containsAny(v, "ubiquiti", "aruba", "ruckus", "cambium", "meraki", "extreme"):
		return "ap"
	case has(5000) && containsAny(v, "synology") || containsAny(v, "synology", "qnap"):
		return "nas"
	case has(3389, 445):
		return "windows"
	case has(62078) || containsAny(v, "apple"):
		return "apple"
	case has(548):
		return "nas"
	case has(80, 443, 8080, 8443):
		return "web"
	case has(22):
		return "ssh"
	}
	return ""
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}
