package netscan

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

func sysGateways() (map[string]string, []string) {
	gw := map[string]string{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "route", "-n", "get", "default").Output(); err == nil {
		var g, ifc string
		for _, l := range strings.Split(string(out), "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(l), ":")
			if !ok {
				continue
			}
			switch k {
			case "gateway":
				g = strings.TrimSpace(v)
			case "interface":
				ifc = strings.TrimSpace(v)
			}
		}
		if g != "" && ifc != "" {
			gw[ifc] = g
		}
	}
	var dns []string
	if out, err := exec.CommandContext(ctx, "scutil", "--dns").Output(); err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			l = strings.TrimSpace(l)
			if strings.HasPrefix(l, "nameserver[") {
				if _, v, ok := strings.Cut(l, ":"); ok {
					dns = append(dns, strings.TrimSpace(v))
				}
			}
			if strings.HasPrefix(l, "resolver #2") {
				break // bara den första (systemets) resolvern
			}
		}
	}
	if len(dns) == 0 {
		dns = resolvConf("/etc/resolv.conf")
	}
	return gw, dns
}
