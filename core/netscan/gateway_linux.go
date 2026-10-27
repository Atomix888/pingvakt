package netscan

import (
	"bufio"
	"encoding/hex"
	"net"
	"os"
	"strings"
)

func sysGateways() (map[string]string, []string) {
	gw := map[string]string{}
	if f, err := os.Open("/proc/net/route"); err == nil {
		sc := bufio.NewScanner(f)
		best := map[string]int{}
		for sc.Scan() {
			fs := strings.Fields(sc.Text())
			// Iface Destination Gateway Flags RefCnt Use Metric Mask
			if len(fs) < 8 || fs[1] != "00000000" || fs[7] != "00000000" {
				continue
			}
			b, err := hex.DecodeString(fs[2])
			if err != nil || len(b) != 4 {
				continue
			}
			ip := net.IPv4(b[3], b[2], b[1], b[0]).String()
			m := atoi(fs[6])
			if old, ok := best[fs[0]]; !ok || m < old {
				best[fs[0]], gw[fs[0]] = m, ip
			}
		}
		f.Close()
	}
	return gw, resolvConf("/run/systemd/resolve/resolv.conf", "/etc/resolv.conf")
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
