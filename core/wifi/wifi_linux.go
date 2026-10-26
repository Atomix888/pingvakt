package wifi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

func collect(ctx context.Context) (*Info, error) {
	ifs, _ := filepath.Glob("/sys/class/net/*/wireless")
	if len(ifs) == 0 {
		ifs, _ = filepath.Glob("/sys/class/net/*/phy80211")
	}
	proc, _ := os.ReadFile("/proc/net/wireless")
	for _, p := range ifs {
		iface := filepath.Base(filepath.Dir(p))
		var in *Info
		if out, err := run(ctx, "iw", "dev", iface, "link"); err == nil {
			in = parseIwLink(out)
		}
		sig, noise := parseProcWireless(string(proc), iface)
		if in == nil {
			if sig == 0 || sig >= 0 {
				continue
			}
			in = &Info{RSSI: sig} // utan iw: bara signal och brus
		}
		in.Iface = iface
		in.Noise = noise
		if in.Noise == 0 {
			if out, err := run(ctx, "iw", "dev", iface, "survey", "dump"); err == nil {
				in.Noise = parseSurveyNoise(out)
			}
		}
		return in, nil
	}
	return nil, errors.New("ingen Wi-Fi-anslutning")
}
