package wifi

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"
)

var (
	spMu   sync.Mutex
	spLast *Info
	spT    time.Time
)

func collect(ctx context.Context) (*Info, error) {
	if os.Geteuid() == 0 {
		if out, err := run(ctx, "/usr/bin/wdutil", "info"); err == nil {
			if in := parseWdutil(out); in != nil {
				return in, nil
			}
		}
	}
	// system_profiler är långsamt – läs av högst en gång per minut
	spMu.Lock()
	defer spMu.Unlock()
	if time.Since(spT) < time.Minute {
		if spLast == nil {
			return nil, errors.New("ingen Wi-Fi-anslutning")
		}
		cp := *spLast
		return &cp, nil
	}
	spT = time.Now()
	out, err := run(ctx, "/usr/sbin/system_profiler", "SPAirPortDataType")
	if err != nil {
		spLast = nil
		return nil, err
	}
	spLast = parseSystemProfiler(out)
	if spLast == nil {
		return nil, errors.New("ingen Wi-Fi-anslutning")
	}
	cp := *spLast
	cp.Iface = "en0"
	return &cp, nil
}
