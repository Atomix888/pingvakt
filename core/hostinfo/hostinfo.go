// Package hostinfo samlar in CPU, minne och disk för datorn som programmet körs på.
// Används av agenten (rapporteras till servern) och av servern (egna värden).
package hostinfo

import (
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
)

// Disk är en volym.
type Disk struct {
	Mount string `json:"m"`
	Used  uint64 `json:"u"`
	Total uint64 `json:"t"`
}

// Pct returnerar hur full volymen är i procent.
func (d Disk) Pct() float64 {
	if d.Total == 0 {
		return 0
	}
	return float64(d.Used) * 100 / float64(d.Total)
}

// Metrics är en mätning av datorn.
type Metrics struct {
	T        int64   `json:"t"`
	CPU      float64 `json:"cpu"` // procent, alla kärnor
	CPUs     int     `json:"cpus"`
	MemUsed  uint64  `json:"memUsed"`
	MemTotal uint64  `json:"memTotal"`
	Disks    []Disk  `json:"disks"`
	Uptime   uint64  `json:"uptime"` // sekunder
	Load1    float64 `json:"load1,omitempty"`
}

// MemPct returnerar använt minne i procent.
func (m Metrics) MemPct() float64 {
	if m.MemTotal == 0 {
		return 0
	}
	return float64(m.MemUsed) * 100 / float64(m.MemTotal)
}

// MaxDiskPct returnerar den fullaste volymen i procent.
func (m Metrics) MaxDiskPct() float64 {
	max := 0.0
	for _, d := range m.Disks {
		if p := d.Pct(); p > max {
			max = p
		}
	}
	return max
}

// Collector håller reda på CPU-tid mellan mätningar.
type Collector struct {
	mu        sync.Mutex
	primed    bool
	disks     []string
	disksTime time.Time
}

// New skapar en Collector.
func New() *Collector { return &Collector{} }

// Collect gör en mätning. CPU-värdet är medelvärdet sedan förra anropet.
func (c *Collector) Collect() Metrics {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := Metrics{T: time.Now().UnixMilli(), CPUs: runtime.NumCPU()}
	if !c.primed {
		// första anropet: mät under en kort stund
		if p, err := cpu.Percent(300*time.Millisecond, false); err == nil && len(p) > 0 {
			m.CPU = round1(p[0])
		}
		c.primed = true
	} else if p, err := cpu.Percent(0, false); err == nil && len(p) > 0 {
		m.CPU = round1(p[0])
	}
	if v, err := mem.VirtualMemory(); err == nil {
		m.MemTotal = v.Total
		m.MemUsed = v.Total - v.Available
	}
	if up, err := host.Uptime(); err == nil {
		m.Uptime = up
	}
	m.Load1 = load1()
	if time.Since(c.disksTime) > 10*time.Minute || c.disks == nil {
		c.disks = mounts()
		c.disksTime = time.Now()
	}
	for _, mp := range c.disks {
		u, err := disk.Usage(mp)
		if err != nil || u.Total == 0 {
			continue
		}
		m.Disks = append(m.Disks, Disk{Mount: mp, Used: u.Used, Total: u.Total})
	}
	return m
}

// mounts returnerar riktiga volymer (inga tmpfs, snap-paket o.d.).
func mounts() []string {
	parts, err := disk.Partitions(false)
	if err != nil {
		return defaultMount()
	}
	skipFS := map[string]bool{"tmpfs": true, "devtmpfs": true, "squashfs": true, "overlay": true, "autofs": true,
		"proc": true, "sysfs": true, "devfs": true, "nullfs": true, "iso9660": true, "udf": true, "efivarfs": true}
	seenDev := map[string]bool{}
	var out []string
	for _, p := range parts {
		mp := p.Mountpoint
		if skipFS[strings.ToLower(p.Fstype)] {
			continue
		}
		switch runtime.GOOS {
		case "linux":
			if strings.HasPrefix(mp, "/snap/") || strings.HasPrefix(mp, "/boot") || strings.HasPrefix(mp, "/run") ||
				strings.HasPrefix(mp, "/sys") || strings.HasPrefix(mp, "/proc") || strings.HasPrefix(mp, "/dev") ||
				strings.HasPrefix(mp, "/var/lib/docker") || strings.HasPrefix(mp, "/var/snap") {
				continue
			}
		case "darwin":
			// APFS visar många systemvolymer – visa bara huvudvolymen och externa diskar
			if mp != "/" && !strings.HasPrefix(mp, "/Volumes/") {
				continue
			}
		case "windows":
			ro := false
			for _, o := range p.Opts {
				if strings.EqualFold(o, "ro") {
					ro = true // skrivskyddade (t.ex. CD/DVD) hoppas över
				}
			}
			if ro {
				continue
			}
		}
		if seenDev[p.Device] && runtime.GOOS != "windows" {
			continue
		}
		seenDev[p.Device] = true
		out = append(out, mp)
	}
	sort.Strings(out)
	if len(out) > 8 {
		out = out[:8]
	}
	if len(out) == 0 {
		return defaultMount()
	}
	return out
}

func defaultMount() []string {
	if runtime.GOOS == "windows" {
		return []string{`C:\`}
	}
	return []string{"/"}
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }
