// Package wifi läser av datorns trådlösa anslutning: signalstyrka (RSSI), brus,
// kanal, åtkomstpunkt (BSSID) och länkhastighet. Värdena hämtas med systemets egna
// verktyg (iw på Linux, netsh på Windows, wdutil/system_profiler på macOS).
package wifi

import (
	"context"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Info är en avläsning av Wi-Fi-anslutningen.
type Info struct {
	T     int64   `json:"t,omitempty"`
	Iface string  `json:"if,omitempty"`
	SSID  string  `json:"ssid,omitempty"`
	BSSID string  `json:"bssid,omitempty"`
	RSSI  int     `json:"rssi"`            // dBm
	Noise int     `json:"noise,omitempty"` // dBm (0 = okänt)
	Ch    int     `json:"ch,omitempty"`
	Freq  int     `json:"freq,omitempty"`  // MHz
	Width int     `json:"width,omitempty"` // kanalbredd i MHz
	Tx    float64 `json:"tx,omitempty"`    // Mbit/s
	Rx    float64 `json:"rx,omitempty"`    // Mbit/s
	Std   string  `json:"std,omitempty"`   // t.ex. 802.11ax
}

// SNR är signal/brus-förhållandet i dB (0 om bruset är okänt).
func (i Info) SNR() int {
	if i.Noise == 0 || i.RSSI == 0 {
		return 0
	}
	return i.RSSI - i.Noise
}

// Band returnerar "2,4 GHz", "5 GHz" eller "6 GHz".
func (i Info) Band() string {
	f := i.Freq
	if f == 0 {
		f = FreqOf(i.Ch, "")
	}
	switch {
	case f >= 5925:
		return "6 GHz"
	case f >= 4900:
		return "5 GHz"
	case f > 0:
		return "2,4 GHz"
	}
	return ""
}

// ChannelOf räknar ut kanalnumret från frekvensen.
func ChannelOf(freq int) int {
	switch {
	case freq == 2484:
		return 14
	case freq >= 2412 && freq <= 2472:
		return (freq - 2407) / 5
	case freq >= 5955 && freq <= 7115:
		return (freq - 5950) / 5
	case freq >= 4900 && freq < 5925:
		return (freq - 5000) / 5
	}
	return 0
}

// FreqOf räknar ut mittfrekvensen för en kanal. band = "2", "5" eller "6" (tomt = gissa).
func FreqOf(ch int, band string) int {
	switch {
	case ch <= 0:
		return 0
	case band == "6":
		return 5950 + ch*5
	case ch == 14 && band != "5":
		return 2484
	case ch <= 13 && band != "5":
		return 2407 + ch*5
	}
	return 5000 + ch*5
}

// pctToDBm översätter Windows signalkvalitet (0–100 %) till ungefärlig RSSI.
func pctToDBm(p int) int { return int(math.Round(float64(p)/2)) - 100 }

// Collector läser av Wi-Fi högst en gång per intervall och backar av när det saknas.
type Collector struct {
	mu      sync.Mutex
	last    *Info
	lastT   time.Time
	every   time.Duration
	missing int
}

// New skapar en Collector som läser av högst var tionde sekund.
func New() *Collector { return &Collector{every: 10 * time.Second} }

// Get returnerar senaste avläsningen (nil om datorn inte är ansluten till Wi-Fi).
func (c *Collector) Get() *Info {
	c.mu.Lock()
	defer c.mu.Unlock()
	wait := c.every
	if c.missing > 3 {
		wait = 5 * time.Minute // ingen Wi-Fi här – fråga sällan
	}
	if !c.lastT.IsZero() && time.Since(c.lastT) < wait {
		return c.copyLast()
	}
	c.lastT = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	in, _ := collect(ctx)
	if in == nil || in.RSSI == 0 {
		c.missing++
		c.last = nil
		return nil
	}
	c.missing = 0
	in.T = time.Now().UnixMilli()
	if in.Ch == 0 && in.Freq > 0 {
		in.Ch = ChannelOf(in.Freq)
	}
	if in.Freq == 0 && in.Ch > 0 {
		in.Freq = FreqOf(in.Ch, "")
	}
	in.BSSID = strings.ToLower(in.BSSID)
	c.last = in
	return c.copyLast()
}

func (c *Collector) copyLast() *Info {
	if c.last == nil {
		return nil
	}
	cp := *c.last
	return &cp
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	hideWindow(cmd)
	b, err := cmd.Output()
	return string(b), err
}

// kv delar "Nyckel : värde" och normaliserar nyckeln.
func kv(line string) (string, string, bool) {
	k, v, ok := strings.Cut(line, ":")
	if !ok {
		return "", "", false
	}
	return strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v), true
}

var reNum = regexp.MustCompile(`-?\d+(?:[.,]\d+)?`)

// firstNum läser första talet i s ("-55 dBm", "866.7 MBit/s", "93%").
func firstNum(s string) float64 {
	m := reNum.FindString(s)
	v, _ := strconv.ParseFloat(strings.ReplaceAll(m, ",", "."), 64)
	return v
}

// ---------- tolkning av verktygens utdata (plattformsoberoende, för test) ----------

// parseIwLink tolkar "iw dev <if> link".
func parseIwLink(out string) *Info {
	if !strings.Contains(out, "Connected to") {
		return nil
	}
	in := &Info{}
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "Connected to ") {
			f := strings.Fields(t)
			if len(f) >= 3 {
				in.BSSID = f[2]
			}
			if i := strings.Index(t, "(on "); i >= 0 {
				in.Iface = strings.TrimSuffix(t[i+4:], ")")
			}
			continue
		}
		k, v, ok := kv(t)
		if !ok {
			continue
		}
		switch k {
		case "ssid":
			in.SSID = v
		case "freq":
			in.Freq = int(firstNum(v))
		case "signal":
			in.RSSI = int(firstNum(v))
		case "rx bitrate":
			in.Rx = firstNum(v)
			in.Width = widthOf(v, in.Width)
			in.Std = stdOf(v, in.Std)
		case "tx bitrate":
			in.Tx = firstNum(v)
			in.Width = widthOf(v, in.Width)
			in.Std = stdOf(v, in.Std)
		}
	}
	in.Ch = ChannelOf(in.Freq)
	return in
}

func widthOf(s string, cur int) int {
	for _, f := range strings.Fields(s) {
		if strings.HasSuffix(f, "MHz") {
			if n, err := strconv.Atoi(strings.TrimSuffix(f, "MHz")); err == nil {
				return n
			}
		}
	}
	return cur
}

func stdOf(s string, cur string) string {
	switch {
	case strings.Contains(s, "EHT-"):
		return "802.11be"
	case strings.Contains(s, "HE-"):
		return "802.11ax"
	case strings.Contains(s, "VHT-"):
		return "802.11ac"
	case strings.Contains(s, "MCS"):
		if cur == "" {
			return "802.11n"
		}
	}
	return cur
}

// parseProcWireless läser brusnivån för gränssnittet ur /proc/net/wireless.
func parseProcWireless(out, iface string) (signal, noise int) {
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(k) != iface {
			continue
		}
		f := strings.Fields(v)
		if len(f) >= 4 {
			signal = int(firstNum(f[2]))
			noise = int(firstNum(f[3]))
		}
	}
	if noise <= -110 || noise >= 0 {
		noise = 0
	}
	return
}

// parseSurveyNoise läser bruset för kanalen som används ur "iw dev <if> survey dump".
func parseSurveyNoise(out string) int {
	inUse := false
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "Survey data") {
			inUse = false
			continue
		}
		if strings.HasPrefix(t, "frequency:") {
			inUse = strings.Contains(t, "[in use]")
			continue
		}
		if inUse && strings.HasPrefix(t, "noise:") {
			if n := int(firstNum(t[6:])); n < 0 && n > -110 {
				return n
			}
		}
	}
	return 0
}

// parseNetsh tolkar "netsh wlan show interfaces" (engelska och svenska Windows).
func parseNetsh(out string) *Info {
	out = fromOEM(out)
	var best *Info
	var cur *Info
	pct := 0
	flush := func() {
		if cur != nil && cur.SSID != "" && cur.BSSID != "" {
			if cur.RSSI == 0 && pct > 0 {
				cur.RSSI = pctToDBm(pct)
			}
			if best == nil {
				best = cur
			}
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		k, v, ok := kv(line)
		if !ok {
			continue
		}
		switch {
		case k == "name" || k == "namn":
			flush()
			cur, pct = &Info{Iface: v}, 0
		case cur == nil:
		case k == "ssid":
			cur.SSID = v
		case strings.Contains(k, "bssid"):
			// "BSSID : aa:bb:…" – värdet innehåller kolon, så läs hela raden
			_, rest, _ := strings.Cut(line, ":")
			cur.BSSID = strings.TrimSpace(rest)
		case k == "rssi":
			cur.RSSI = int(firstNum(v))
		case k == "signal":
			pct = int(firstNum(v))
		case k == "channel" || k == "kanal":
			cur.Ch = int(firstNum(v))
		case k == "band":
			switch {
			case strings.HasPrefix(v, "6"):
				cur.Freq = FreqOf(cur.Ch, "6")
			case strings.HasPrefix(v, "5"):
				cur.Freq = FreqOf(cur.Ch, "5")
			case strings.HasPrefix(v, "2"):
				cur.Freq = FreqOf(cur.Ch, "2")
			}
		case strings.Contains(k, "radio"):
			cur.Std = v
		case strings.HasPrefix(k, "receive") || strings.HasPrefix(k, "mottag"):
			cur.Rx = firstNum(v)
		case strings.HasPrefix(k, "transmit") || strings.Contains(k, "verf") || strings.HasPrefix(k, "sänd"):
			cur.Tx = firstNum(v)
		}
	}
	flush()
	if best != nil && best.Freq == 0 {
		best.Freq = FreqOf(best.Ch, "")
	}
	return best
}

// parseWdutil tolkar "wdutil info" (macOS 12.3+, kräver root).
func parseWdutil(out string) *Info {
	in := &Info{}
	section := false
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if t == "WIFI" {
			section = true
			continue
		}
		if !section {
			continue
		}
		if t != "" && !strings.Contains(t, ":") && strings.ToUpper(t) == t && strings.IndexFunc(t, func(r rune) bool { return r >= 'A' && r <= 'Z' }) >= 0 {
			break // nästa avsnitt (t.ex. BLUETOOTH)
		}
		k, v, ok := kv(t)
		if !ok {
			continue
		}
		switch k {
		case "interface name":
			in.Iface = v
		case "ssid":
			in.SSID = cleanRedacted(v)
		case "bssid":
			_, rest, _ := strings.Cut(t, ":")
			in.BSSID = cleanRedacted(strings.TrimSpace(rest))
		case "rssi":
			in.RSSI = int(firstNum(v))
		case "noise":
			in.Noise = int(firstNum(v))
		case "tx rate":
			in.Tx = firstNum(v)
		case "phy mode":
			in.Std = v
		case "channel":
			// "5g36/80", "2g6/20", "6g37/160"
			band := ""
			if len(v) > 1 && v[1] == 'g' {
				band, v = v[:1], v[2:]
			}
			chs, w, _ := strings.Cut(v, "/")
			in.Ch, _ = strconv.Atoi(chs)
			in.Width, _ = strconv.Atoi(w)
			in.Freq = FreqOf(in.Ch, band)
		}
	}
	if in.RSSI == 0 {
		return nil
	}
	return in
}

// parseSystemProfiler tolkar "system_profiler SPAirPortDataType".
func parseSystemProfiler(out string) *Info {
	lines := strings.Split(out, "\n")
	in := &Info{}
	cur := -1
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if t == "Current Network Information:" {
			cur = i
			if i+1 < len(lines) {
				in.SSID = cleanRedacted(strings.TrimSuffix(strings.TrimSpace(lines[i+1]), ":"))
			}
			continue
		}
		if cur < 0 || i == cur+1 {
			continue
		}
		if strings.HasSuffix(t, ":") && !strings.Contains(strings.TrimSuffix(t, ":"), ":") {
			break // nästa grupp ("Other Local Wi-Fi Networks:")
		}
		k, v, ok := kv(t)
		if !ok {
			continue
		}
		switch k {
		case "phy mode":
			in.Std = v
		case "channel":
			// "36 (5GHz, 80MHz)"
			in.Ch = int(firstNum(v))
			band := ""
			switch {
			case strings.Contains(v, "6GHz"):
				band = "6"
			case strings.Contains(v, "5GHz"):
				band = "5"
			case strings.Contains(v, "2GHz"):
				band = "2"
			}
			in.Freq = FreqOf(in.Ch, band)
			in.Width = widthOf(strings.NewReplacer("(", " ", ")", " ", ",", " ").Replace(v), 0)
		case "signal / noise":
			s, n, _ := strings.Cut(v, "/")
			in.RSSI, in.Noise = int(firstNum(s)), int(firstNum(n))
		case "transmit rate":
			in.Tx = firstNum(v)
		}
	}
	if in.RSSI == 0 {
		return nil
	}
	return in
}

func cleanRedacted(s string) string {
	if strings.Contains(s, "redacted") || s == "<private>" {
		return ""
	}
	return s
}

// fromOEM översätter utdata i konsolens teckentabell (cp850) till UTF-8
// när den inte redan är giltig UTF-8 – vanligt på svenska Windows.
func fromOEM(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	tbl := map[byte]rune{0x86: 'å', 0x84: 'ä', 0x94: 'ö', 0x8f: 'Å', 0x8e: 'Ä', 0x99: 'Ö', 0x82: 'é', 0x90: 'É', 0x81: 'ü', 0x9a: 'Ü', 0x91: 'æ', 0x92: 'Æ', 0x9b: 'ø', 0x9d: 'Ø'}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x80 {
			b.WriteByte(c)
		} else if r, ok := tbl[c]; ok {
			b.WriteRune(r)
		} else {
			b.WriteRune('?')
		}
	}
	return b.String()
}
