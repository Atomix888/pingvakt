package wifi

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
)

func survey(ctx context.Context) ([]BSS, string, error) {
	ifs, _ := filepath.Glob("/sys/class/net/*/wireless")
	if len(ifs) == 0 {
		ifs, _ = filepath.Glob("/sys/class/net/*/phy80211")
	}
	if len(ifs) == 0 {
		return nil, "", errors.New("no Wi-Fi adapter")
	}
	var lastErr error
	for _, p := range ifs {
		iface := filepath.Base(filepath.Dir(p))
		out, err := run(ctx, "iw", "dev", iface, "scan")
		if err != nil {
			// upptagen eller saknar rättighet – använd senaste resultatet
			if o2, err2 := run(ctx, "iw", "dev", iface, "scan", "dump"); err2 == nil && strings.Contains(o2, "BSS ") {
				out, err = o2, nil
			}
		}
		if err == nil {
			if l := parseIwScan(out); len(l) > 0 {
				return l, "iw", nil
			}
		}
		lastErr = err
	}
	if _, err := exec.LookPath("nmcli"); err == nil {
		_, _ = run(ctx, "nmcli", "dev", "wifi", "rescan")
		out, err := run(ctx, "nmcli", "-t", "-f", "IN-USE,SSID,BSSID,CHAN,FREQ,SIGNAL,SECURITY", "dev", "wifi", "list")
		if err == nil {
			return parseNmcli(out), "nmcli", nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no networks found")
	}
	return nil, "iw", lastErr
}
