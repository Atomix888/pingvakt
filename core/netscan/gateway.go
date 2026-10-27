package netscan

import (
	"bufio"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// Gateway och DNS hämtas högst en gång i minuten (kan kräva externa kommandon).
var gwCache struct {
	sync.Mutex
	t   time.Time
	gw  map[string]string // gränssnitt -> standard-gateway
	dns []string
}

func gatewayInfo() (map[string]string, []string) {
	gwCache.Lock()
	defer gwCache.Unlock()
	if time.Since(gwCache.t) < time.Minute && gwCache.gw != nil {
		return gwCache.gw, gwCache.dns
	}
	gw, dns := sysGateways()
	if gw == nil {
		gw = map[string]string{}
	}
	gwCache.t, gwCache.gw, gwCache.dns = time.Now(), gw, uniqIPv4(dns)
	return gwCache.gw, gwCache.dns
}

func uniqIPv4(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		ip := net.ParseIP(strings.TrimSpace(s))
		if ip == nil || ip.To4() == nil || ip.IsUnspecified() {
			continue
		}
		v := ip.String()
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// resolvConf läser namnservrar ur en resolv.conf (hoppar över lokala stubbar som 127.0.0.53).
func resolvConf(paths ...string) []string {
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		var out []string
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fs := strings.Fields(sc.Text())
			if len(fs) >= 2 && fs[0] == "nameserver" {
				if ip := net.ParseIP(fs[1]); ip != nil && !ip.IsLoopback() {
					out = append(out, fs[1])
				}
			}
		}
		f.Close()
		if len(out) > 0 {
			return out
		}
	}
	return nil
}
