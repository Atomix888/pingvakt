package wifi

import (
	"context"
	"sort"
	"strings"
	"time"
)

// BSS är ett trådlöst nätverk (en åtkomstpunkts radio) som syns vid en avsökning.
type BSS struct {
	SSID  string `json:"ssid,omitempty"` // tomt = dolt nätverk
	BSSID string `json:"bssid,omitempty"`
	RSSI  int    `json:"rssi"`
	Ch    int    `json:"ch"`
	Freq  int    `json:"freq,omitempty"`
	Width int    `json:"width,omitempty"` // MHz
	Sec   string `json:"sec,omitempty"`   // Open, WEP, WPA, WPA2, WPA3, WPA2/WPA3 (+ " Enterprise")
	Std   string `json:"std,omitempty"`
	Conn  bool   `json:"conn,omitempty"` // datorn är ansluten till denna
}

// Scan är resultatet av en avsökning.
type Scan struct {
	T    int64  `json:"t"`
	Err  string `json:"err,omitempty"`
	BSS  []BSS  `json:"bss"`
	Tool string `json:"tool,omitempty"` // iw, nmcli, netsh, system_profiler
}

// Survey söker av grannätverken. Det tar några sekunder och kan kräva administratörsrätt.
func Survey(ctx context.Context) Scan {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	sc := Scan{T: time.Now().UnixMilli()}
	list, tool, err := survey(ctx)
	sc.Tool = tool
	if err != nil {
		sc.Err = err.Error()
	}
	for i := range list {
		b := &list[i]
		if b.Freq == 0 && b.Ch > 0 {
			b.Freq = FreqOf(b.Ch, "")
		}
		if b.Ch == 0 && b.Freq > 0 {
			b.Ch = ChannelOf(b.Freq)
		}
		if b.Width == 0 {
			b.Width = 20
		}
		b.BSSID = strings.ToLower(b.BSSID)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].RSSI > list[j].RSSI })
	if len(list) > 300 {
		list = list[:300]
	}
	sc.BSS = list
	if sc.BSS == nil {
		sc.BSS = []BSS{}
	}
	return sc
}

// ---------- tolkning (plattformsoberoende, för test) ----------

// parseIwScan tolkar "iw dev <if> scan".
func parseIwScan(out string) []BSS {
	var list []BSS
	var cur *BSS
	var rsn, wpa, privacy, sae, eap, ht40 bool
	flush := func() {
		if cur == nil {
			return
		}
		switch {
		case rsn && sae && strings.Contains(cur.Sec, "psk"):
			cur.Sec = "WPA2/WPA3"
		case rsn && sae:
			cur.Sec = "WPA3"
		case rsn:
			cur.Sec = "WPA2"
		case wpa:
			cur.Sec = "WPA"
		case privacy:
			cur.Sec = "WEP"
		default:
			cur.Sec = "Open"
		}
		if eap && (rsn || wpa) {
			cur.Sec += " Enterprise"
		}
		if cur.Width < 40 && ht40 {
			cur.Width = 40
		}
		list = append(list, *cur)
		cur = nil
	}
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(line, "BSS ") {
			flush()
			f := strings.Fields(strings.TrimPrefix(line, "BSS "))
			if len(f) == 0 {
				continue
			}
			bssid, _, _ := strings.Cut(f[0], "(")
			cur = &BSS{BSSID: bssid, Width: 20, Conn: strings.Contains(line, "associated")}
			rsn, wpa, privacy, sae, eap, ht40 = false, false, false, false, false, false
			continue
		}
		if cur == nil {
			continue
		}
		switch {
		case strings.HasPrefix(t, "freq:"):
			cur.Freq = int(firstNum(t))
			cur.Ch = ChannelOf(cur.Freq)
		case strings.HasPrefix(t, "signal:"):
			cur.RSSI = int(firstNum(t))
		case strings.HasPrefix(t, "SSID:"):
			cur.SSID = strings.TrimSpace(strings.TrimPrefix(t, "SSID:"))
			if strings.HasPrefix(cur.SSID, `\x00`) {
				cur.SSID = ""
			}
		case strings.HasPrefix(t, "capability:"):
			privacy = strings.Contains(t, "Privacy")
		case strings.HasPrefix(t, "RSN:"):
			rsn = true
		case strings.HasPrefix(t, "WPA:"):
			wpa = true
		case strings.Contains(t, "Authentication suites:"):
			if strings.Contains(t, "SAE") {
				sae = true
			}
			if strings.Contains(t, "PSK") {
				cur.Sec = "psk" // tillfälligt: används för blandat WPA2/WPA3
			}
			if strings.Contains(t, "IEEE 802.1X") || strings.Contains(t, "802.1X") {
				eap = true
			}
		case strings.Contains(t, "secondary channel offset:"):
			ht40 = !strings.Contains(t, "no secondary")
		case strings.Contains(t, "channel width:"):
			// "VHT operation: * channel width: 1 (80 MHz)", EHT/HE: "channel width: 160 MHz"
			if w := widthOf(strings.NewReplacer("(", " ", ")", " ", " MHz", "MHz").Replace(t), 0); w > cur.Width {
				cur.Width = w
			}
		case strings.HasPrefix(t, "EHT capabilities"):
			cur.Std = higherStd(cur.Std, "802.11be")
		case strings.HasPrefix(t, "HE capabilities"):
			cur.Std = higherStd(cur.Std, "802.11ax")
		case strings.HasPrefix(t, "VHT capabilities"):
			cur.Std = higherStd(cur.Std, "802.11ac")
		case strings.HasPrefix(t, "HT capabilities"):
			cur.Std = higherStd(cur.Std, "802.11n")
		}
	}
	flush()
	return list
}

func higherStd(a, b string) string {
	rank := map[string]int{"802.11n": 1, "802.11ac": 2, "802.11ax": 3, "802.11be": 4}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// parseNmcli tolkar "nmcli -t -f IN-USE,SSID,BSSID,CHAN,FREQ,SIGNAL,SECURITY dev wifi list".
func parseNmcli(out string) []BSS {
	var list []BSS
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// fälten skiljs med ':' och kolon i värden skrivs som '\:'
		var fs []string
		var b strings.Builder
		esc := false
		for _, r := range line {
			switch {
			case esc:
				b.WriteRune(r)
				esc = false
			case r == '\\':
				esc = true
			case r == ':':
				fs = append(fs, b.String())
				b.Reset()
			default:
				b.WriteRune(r)
			}
		}
		fs = append(fs, b.String())
		if len(fs) < 7 {
			continue
		}
		x := BSS{Conn: strings.TrimSpace(fs[0]) == "*", SSID: fs[1], BSSID: fs[2], Ch: int(firstNum(fs[3])), Freq: int(firstNum(fs[4])),
			RSSI: pctToDBm(int(firstNum(fs[5])))}
		sec := strings.ToUpper(fs[6])
		switch {
		case sec == "" || sec == "--":
			x.Sec = "Open"
		case strings.Contains(sec, "WPA3") && strings.Contains(sec, "WPA2"):
			x.Sec = "WPA2/WPA3"
		case strings.Contains(sec, "WPA3"):
			x.Sec = "WPA3"
		case strings.Contains(sec, "WPA2"):
			x.Sec = "WPA2"
		case strings.Contains(sec, "WPA"):
			x.Sec = "WPA"
		case strings.Contains(sec, "WEP"):
			x.Sec = "WEP"
		}
		if strings.Contains(sec, "802.1X") {
			x.Sec += " Enterprise"
		}
		list = append(list, x)
	}
	return list
}

// parseNetshNetworks tolkar "netsh wlan show networks mode=bssid" (engelska och svenska Windows).
func parseNetshNetworks(out string) []BSS {
	out = fromOEM(out)
	var list []BSS
	var ssid, auth string
	var cur *BSS
	flush := func() {
		if cur != nil {
			list = append(list, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		t := strings.TrimSpace(line)
		k, v, ok := kv(t)
		if !ok {
			continue
		}
		switch {
		case strings.HasPrefix(k, "ssid"):
			flush()
			ssid, auth = v, ""
		case strings.HasPrefix(k, "authentication") || strings.HasPrefix(k, "autentisering"):
			auth = v
		case strings.HasPrefix(k, "bssid"):
			flush()
			_, rest, _ := strings.Cut(t, ":")
			cur = &BSS{SSID: ssid, BSSID: strings.TrimSpace(rest), Sec: netshSec(auth)}
		case cur == nil:
		case k == "signal":
			cur.RSSI = pctToDBm(int(firstNum(v)))
		case k == "rssi":
			cur.RSSI = int(firstNum(v))
		case k == "channel" || k == "kanal":
			cur.Ch = int(firstNum(v))
			if cur.Freq == 0 {
				cur.Freq = FreqOf(cur.Ch, "")
			}
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
		}
	}
	flush()
	// kanalen kommer efter bandet i vissa versioner – räkna om frekvensen när båda är kända
	for i := range list {
		if list[i].Freq == 0 || ChannelOf(list[i].Freq) != list[i].Ch {
			band := ""
			switch {
			case list[i].Freq >= 5925:
				band = "6"
			case list[i].Freq >= 4900:
				band = "5"
			}
			list[i].Freq = FreqOf(list[i].Ch, band)
		}
	}
	return list
}

func netshSec(auth string) string {
	a := strings.ToUpper(auth)
	ent := strings.Contains(a, "ENTERPRISE")
	var s string
	switch {
	case strings.Contains(a, "WPA3") && strings.Contains(a, "WPA2"):
		s = "WPA2/WPA3"
	case strings.Contains(a, "WPA3"):
		s = "WPA3"
	case strings.Contains(a, "WPA2"):
		s = "WPA2"
	case strings.Contains(a, "WPA"):
		s = "WPA"
	case strings.Contains(a, "OPEN") || strings.Contains(a, "ÖPPEN") || a == "":
		return "Open"
	default:
		return auth
	}
	if ent {
		s += " Enterprise"
	}
	return s
}

// parseSPNetworks tolkar avsnittet "Other Local Wi-Fi Networks" i "system_profiler SPAirPortDataType".
// macOS visar inte BSSID (och ibland inte SSID) utan platsbehörighet.
func parseSPNetworks(out string) []BSS {
	var list []BSS
	lines := strings.Split(out, "\n")
	indent := func(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }
	in := false
	base := 0
	var cur *BSS
	flush := func() {
		if cur != nil && cur.Ch > 0 {
			list = append(list, *cur)
		}
		cur = nil
	}
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "Current Network Information:") || strings.HasPrefix(t, "Other Local Wi-Fi Networks:") {
			flush()
			in = true
			base = indent(line)
			continue
		}
		if !in {
			continue
		}
		if indent(line) <= base {
			flush()
			in = false
			continue
		}
		if strings.HasSuffix(t, ":") && indent(line) == base+2 {
			flush()
			cur = &BSS{SSID: cleanRedacted(strings.TrimSuffix(t, ":"))}
			cur.Conn = isCurrentSection(lines, i)
			continue
		}
		if cur == nil {
			continue
		}
		k, v, ok := kv(t)
		if !ok {
			continue
		}
		switch k {
		case "channel":
			cur.Ch = int(firstNum(v))
			band := ""
			if strings.Contains(v, "6GHz") {
				band = "6"
			} else if strings.Contains(v, "5GHz") {
				band = "5"
			} else if strings.Contains(v, "2GHz") {
				band = "2"
			}
			cur.Freq = FreqOf(cur.Ch, band)
			cur.Width = widthOf(strings.ReplaceAll(strings.ReplaceAll(v, ")", ""), ",", " "), 20)
		case "signal / noise":
			cur.RSSI = int(firstNum(v))
		case "security":
			cur.Sec = macSec(v)
		case "phy mode":
			cur.Std = v
		}
	}
	flush()
	return list
}

func isCurrentSection(lines []string, i int) bool {
	for j := i - 1; j >= 0; j-- {
		t := strings.TrimSpace(lines[j])
		if strings.HasPrefix(t, "Current Network Information:") {
			return true
		}
		if strings.HasPrefix(t, "Other Local Wi-Fi Networks:") {
			return false
		}
	}
	return false
}

func macSec(v string) string {
	u := strings.ToUpper(v)
	ent := strings.Contains(u, "ENTERPRISE")
	var s string
	switch {
	case strings.Contains(u, "WPA3") && strings.Contains(u, "WPA2"):
		s = "WPA2/WPA3"
	case strings.Contains(u, "WPA3"):
		s = "WPA3"
	case strings.Contains(u, "WPA2"):
		s = "WPA2"
	case strings.Contains(u, "WPA"):
		s = "WPA"
	case strings.Contains(u, "WEP"):
		return "WEP"
	case strings.Contains(u, "NONE") || strings.Contains(u, "OPEN"):
		return "Open"
	default:
		return v
	}
	if ent {
		s += " Enterprise"
	}
	return s
}
