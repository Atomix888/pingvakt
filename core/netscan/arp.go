package netscan

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

var (
	reIP  = regexp.MustCompile(`\b(\d{1,3}(?:\.\d{1,3}){3})\b`)
	reMAC = regexp.MustCompile(`\b([0-9A-Fa-f]{1,2}(?:[:-][0-9A-Fa-f]{1,2}){5})\b`)
)

// arpTable läser datorns ARP-tabell (ip -> mac).
func arpTable(ctx context.Context) map[string]string {
	out := map[string]string{}
	if runtime.GOOS == "linux" {
		if f, err := os.Open("/proc/net/arp"); err == nil {
			defer f.Close()
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				fs := strings.Fields(sc.Text())
				if len(fs) >= 4 && fs[2] != "0x0" && fs[3] != "00:00:00:00:00:00" && reIP.MatchString(fs[0]) {
					out[fs[0]] = fs[3]
				}
			}
			return out
		}
	}
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	args := []string{"-a"}
	if runtime.GOOS != "windows" {
		args = []string{"-an"}
	}
	cmd := exec.CommandContext(c, "arp", args...)
	hideWindow(cmd)
	b, err := cmd.Output()
	if err != nil {
		return out
	}
	return parseARP(string(b))
}

// parseARP tolkar utdata från "arp -a" (Windows) och "arp -an" (macOS/BSD).
func parseARP(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		ip := reIP.FindString(line)
		mac := reMAC.FindString(line)
		if ip == "" || mac == "" {
			continue
		}
		mac = normMAC(mac)
		if mac == "ff:ff:ff:ff:ff:ff" || strings.HasPrefix(mac, "01:00:5e") || mac == "00:00:00:00:00:00" {
			continue
		}
		out[ip] = mac
	}
	return out
}

// normMAC skriver en MAC-adress som aa:bb:cc:dd:ee:ff.
func normMAC(m string) string {
	m = strings.TrimSpace(strings.ToLower(strings.ReplaceAll(m, "-", ":")))
	if m == "" {
		return ""
	}
	parts := strings.Split(m, ":")
	if len(parts) != 6 {
		return m
	}
	for i, p := range parts {
		if len(p) == 1 {
			parts[i] = "0" + p
		}
	}
	return strings.Join(parts, ":")
}

// isRandomMAC är sant för lokalt administrerade adresser (t.ex. telefoners privata Wi-Fi-adress).
func isRandomMAC(m string) bool {
	m = normMAC(m)
	if len(m) < 2 {
		return false
	}
	var b byte
	for _, c := range m[:2] {
		b <<= 4
		switch {
		case c >= '0' && c <= '9':
			b |= byte(c - '0')
		case c >= 'a' && c <= 'f':
			b |= byte(c - 'a' + 10)
		}
	}
	return b&0x02 != 0
}
