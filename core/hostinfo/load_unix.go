//go:build !windows

package hostinfo

import "github.com/shirou/gopsutil/v4/load"

func load1() float64 {
	if l, err := load.Avg(); err == nil {
		return float64(int(l.Load1*100+0.5)) / 100
	}
	return 0
}
