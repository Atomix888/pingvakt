// Package probe innehåller själva mätningarna: ICMP-ping och TCP-"portping".
// Paketet används av skrivbordsappen och (i nästa etapp) av agenten och servern,
// så att en mätning beter sig likadant oavsett var den körs.
package probe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"
)

// Kind är typen av mätning.
type Kind string

const (
	KindICMP Kind = "icmp"
	KindTCP  Kind = "tcp"
	KindHTTP Kind = "http" // HTTP(S)-anrop; Host är en URL
	KindDNS  Kind = "dns"  // DNS-uppslag; Host är namnet som slås upp
)

// IPMode styr vilken adressfamilj som används vid namnuppslag.
type IPMode string

const (
	IPAuto IPMode = "auto" // IPv4 i första hand, annars IPv6
	IPv4   IPMode = "4"
	IPv6   IPMode = "6"
)

// Request beskriver en enskild mätning.
type Request struct {
	Host    string
	Kind    Kind
	Port    int
	Timeout time.Duration
	Size    int // ICMP-datastorlek i byte
	IPMode  IPMode
	Keyword string // HTTP: texten måste finnas i svaret
	DNSSrv  string // DNS: server att fråga (tom = systemets)
}

// Result är utfallet av en mätning.
type Result struct {
	Time time.Time
	OK   bool
	RTT  time.Duration
	Addr string // den IP-adress som faktiskt mättes
	Err  string
	// HTTP(S)
	Status   int // statuskod
	CertDays int // dagar tills certifikatet går ut (-1 = inget certifikat)
}

// Pinger utför mätningar. Implementeringen är trådsäker.
type Pinger struct {
	resolver *Resolver
	icmp     icmpPinger
}

// New skapar en Pinger.
func New() *Pinger {
	return &Pinger{resolver: NewResolver(60 * time.Second), icmp: newICMP()}
}

// Close frigör resurser.
func (p *Pinger) Close() { p.icmp.close() }

// Do utför en mätning enligt req.
func (p *Pinger) Do(ctx context.Context, req Request) Result {
	if req.Timeout <= 0 {
		req.Timeout = 2 * time.Second
	}
	if req.Size <= 0 {
		req.Size = 32
	}
	res := Result{Time: time.Now(), CertDays: -1}
	switch req.Kind {
	case KindHTTP:
		httpCheck(ctx, req, &res)
		return res
	case KindDNS:
		dnsCheck(ctx, req, &res)
		return res
	}
	ip, err := p.resolver.Resolve(ctx, req.Host, req.IPMode)
	if err != nil {
		res.Err = "Name lookup failed: " + shortErr(err)
		return res
	}
	res.Addr = ip.String()
	switch req.Kind {
	case KindTCP:
		res.RTT, err = tcpPing(ctx, ip, req.Port, req.Timeout)
	default:
		res.RTT, err = p.icmp.ping(ctx, ip, req.Size, req.Timeout)
	}
	if err != nil {
		res.Err = shortErr(err)
		return res
	}
	res.OK = true
	return res
}

// ErrTimeout returneras när inget svar kommit inom tidsgränsen.
var ErrTimeout = errors.New("Timed out")

func tcpPing(ctx context.Context, ip net.IP, port int, timeout time.Duration) (time.Duration, error) {
	if port <= 0 || port > 65535 {
		return 0, fmt.Errorf("invalid port %d", port)
	}
	d := net.Dialer{Timeout: timeout}
	start := time.Now()
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(port)))
	rtt := time.Since(start)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return 0, ErrTimeout
		}
		return 0, fmt.Errorf("Port closed: %s", shortErr(err))
	}
	_ = c.Close()
	return rtt, nil
}

func shortErr(err error) string {
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Err != nil {
		return oe.Err.Error()
	}
	var de *net.DNSError
	if errors.As(err, &de) {
		if de.IsNotFound {
			return "host name not found"
		}
		return de.Err
	}
	return err.Error()
}

// Resolver cachar namnuppslag så att varje ping inte belastar DNS.
type Resolver struct {
	ttl   time.Duration
	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	ip  net.IP
	exp time.Time
}

func NewResolver(ttl time.Duration) *Resolver {
	return &Resolver{ttl: ttl, cache: map[string]cacheEntry{}}
}

// Resolve slår upp host enligt mode. IP-adresser returneras direkt.
func (r *Resolver) Resolve(ctx context.Context, host string, mode IPMode) (net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if mode == IPv4 && ip.To4() == nil {
			return nil, errors.New("the address is not IPv4")
		}
		if mode == IPv6 && ip.To4() != nil {
			return nil, errors.New("the address is not IPv6")
		}
		return ip, nil
	}
	key := string(mode) + "|" + host
	r.mu.Lock()
	if e, ok := r.cache[key]; ok && time.Now().Before(e.exp) {
		r.mu.Unlock()
		return e.ip, nil
	}
	r.mu.Unlock()

	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(lctx, host)
	if err != nil {
		return nil, err
	}
	var v4, v6 net.IP
	for _, a := range addrs {
		if a.IP.To4() != nil {
			if v4 == nil {
				v4 = a.IP.To4()
			}
		} else if v6 == nil {
			v6 = a.IP
		}
	}
	var ip net.IP
	switch mode {
	case IPv4:
		ip = v4
	case IPv6:
		ip = v6
	default:
		ip = v4
		if ip == nil {
			ip = v6
		}
	}
	if ip == nil {
		return nil, fmt.Errorf("no %s address for %s", familyName(mode), host)
	}
	r.mu.Lock()
	r.cache[key] = cacheEntry{ip: ip, exp: time.Now().Add(r.ttl)}
	r.mu.Unlock()
	return ip, nil
}

func familyName(m IPMode) string {
	switch m {
	case IPv4:
		return "IPv4"
	case IPv6:
		return "IPv6"
	}
	return "IP"
}
