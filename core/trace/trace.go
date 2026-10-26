// Package trace kör en traceroute med operativsystemets eget verktyg
// (tracert på Windows, traceroute på macOS/Linux) och tolkar resultatet hopp för hopp.
package trace

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Hop är ett hopp på vägen till målet.
type Hop struct {
	N    int     `json:"n"`
	Addr string  `json:"addr"`
	Name string  `json:"name,omitempty"`
	RTT  float64 `json:"rtt"` // ms, -1 om inget svar
}

var (
	reHop = regexp.MustCompile(`^\s*(\d+)\s+(.*)$`)
	reMs  = regexp.MustCompile(`(<?\d+(?:[.,]\d+)?)\s*ms`)
)

// Run kör traceroute mot host och anropar onHop för varje hopp.
func Run(ctx context.Context, host string, maxHops int, onHop func(Hop)) error {
	if maxHops <= 0 {
		maxHops = 30
	}
	host = strings.TrimSpace(host)
	if host == "" || strings.HasPrefix(host, "-") || strings.ContainsAny(host, " ;&|`$<>\"'") {
		return errors.New("ogiltigt mål")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.CommandContext(ctx, "tracert", "-d", "-w", "1000", "-h", strconv.Itoa(maxHops), host)
	default:
		bin, err := exec.LookPath("traceroute")
		if err != nil {
			return errors.New("traceroute saknas – installera paketet 'traceroute'")
		}
		cmd = exec.CommandContext(ctx, bin, "-n", "-q", "1", "-w", "1", "-m", strconv.Itoa(maxHops), host)
	}
	hideWindow(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("kunde inte starta traceroute: %w", err)
	}
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		if h, ok := ParseLine(sc.Text()); ok {
			if h.Addr != "" {
				h.Name = reverse(ctx, h.Addr)
			}
			onHop(h)
		}
	}
	if err := cmd.Wait(); err != nil && ctx.Err() == nil {
		// traceroute avslutas ibland med felkod trots giltig utdata
		if _, ok := err.(*exec.ExitError); !ok {
			return err
		}
	}
	return ctx.Err()
}

// ParseLine tolkar en rad från tracert/traceroute.
func ParseLine(line string) (Hop, bool) {
	m := reHop.FindStringSubmatch(line)
	if m == nil {
		return Hop{}, false
	}
	n, _ := strconv.Atoi(m[1])
	rest := m[2]
	h := Hop{N: n, RTT: -1}
	for _, f := range strings.Fields(rest) {
		f = strings.Trim(f, "[]()")
		if ip := net.ParseIP(f); ip != nil {
			h.Addr = ip.String()
		}
	}
	best := -1.0
	for _, mm := range reMs.FindAllStringSubmatch(rest, -1) {
		s := strings.ReplaceAll(strings.TrimPrefix(mm[1], "<"), ",", ".")
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			continue
		}
		if strings.HasPrefix(mm[1], "<") {
			v = v / 2
		}
		if best < 0 || v < best {
			best = v
		}
	}
	h.RTT = best
	if h.Addr == "" {
		h.RTT = -1
	}
	return h, true
}

func reverse(ctx context.Context, addr string) string {
	c, cancel := context.WithTimeout(ctx, 700*time.Millisecond)
	defer cancel()
	names, err := net.DefaultResolver.LookupAddr(c, addr)
	if err != nil || len(names) == 0 {
		return ""
	}
	return strings.TrimSuffix(names[0], ".")
}
