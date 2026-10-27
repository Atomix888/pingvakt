package wifi

import (
	"context"
	"errors"
	"strings"
)

func survey(ctx context.Context) ([]BSS, string, error) {
	out, err := run(ctx, "netsh", "wlan", "show", "networks", "mode=bssid")
	if err != nil {
		if strings.Contains(strings.ToLower(out), "location") {
			return nil, "netsh", errors.New("Windows requires location access for Wi-Fi scanning")
		}
		return nil, "netsh", err
	}
	l := parseNetshNetworks(out)
	if len(l) == 0 && strings.Contains(strings.ToLower(fromOEM(out)), "location") {
		return nil, "netsh", errors.New("Windows requires location access for Wi-Fi scanning")
	}
	// markera nätet datorn är ansluten till
	if cur, err := run(ctx, "netsh", "wlan", "show", "interfaces"); err == nil {
		if in := parseNetsh(cur); in != nil {
			for i := range l {
				if strings.EqualFold(l[i].BSSID, in.BSSID) {
					l[i].Conn = true
				}
			}
		}
	}
	return l, "netsh", nil
}
