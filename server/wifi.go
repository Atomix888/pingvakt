package main

import (
	"fmt"
	"log"
	"sync"
	"time"

	"pingvakt/core/alert"
	"pingvakt/core/i18n"
	"pingvakt/core/wifi"
)

const wifiSchema = `
CREATE TABLE IF NOT EXISTS wifi_metrics (
  agent_id TEXT NOT NULL, t INTEGER NOT NULL, rssi REAL NOT NULL, rssi_min INTEGER NOT NULL, noise INTEGER NOT NULL,
  ch INTEGER NOT NULL, freq INTEGER NOT NULL, tx REAL NOT NULL, rx REAL NOT NULL, ssid TEXT NOT NULL, bssid TEXT NOT NULL,
  PRIMARY KEY (agent_id, t)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS wifi_roam (
  agent_id TEXT NOT NULL, t INTEGER NOT NULL, ssid TEXT NOT NULL, from_bssid TEXT NOT NULL, to_bssid TEXT NOT NULL,
  from_rssi INTEGER NOT NULL, to_rssi INTEGER NOT NULL, from_ch INTEGER NOT NULL, to_ch INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS wifi_roam_at ON wifi_roam(agent_id, t);
`

type wifiPoint struct {
	t    int64
	rssi int
}

type wifiTrack struct {
	latest   *wifi.Info
	minute   int64
	n        int
	rssiSum  float64
	rssiMin  int
	noiseSum int
	noiseN   int
	last     wifi.Info
	recent   []wifiPoint
}

type wifiState struct {
	mu     sync.Mutex
	tracks map[string]*wifiTrack
}

// Roam är ett byte av åtkomstpunkt.
type Roam struct {
	T        int64  `json:"t"`
	SSID     string `json:"ssid"`
	From     string `json:"from"`
	To       string `json:"to"`
	FromRSSI int    `json:"fromRssi"`
	ToRSSI   int    `json:"toRssi"`
	FromCh   int    `json:"fromCh"`
	ToCh     int    `json:"toCh"`
}

var wifis = &wifiState{tracks: map[string]*wifiTrack{}}

// ingestWiFi tar emot en Wi-Fi-avläsning från en agent.
func (s *Server) ingestWiFi(agentID string, in wifi.Info) {
	t := now()
	in.T = t
	minute := t - t%60000
	var flush *wifiRow
	var roam *Roam
	wifis.mu.Lock()
	tr := wifis.tracks[agentID]
	if tr == nil {
		tr = &wifiTrack{}
		wifis.tracks[agentID] = tr
	}
	if tr.n > 0 && tr.minute != minute {
		r := tr.row(agentID)
		flush = &r
		tr.reset()
	}
	if p := tr.latest; p != nil && p.BSSID != "" && in.BSSID != "" && p.BSSID != in.BSSID && t-p.T < 10*60000 {
		roam = &Roam{T: t, SSID: in.SSID, From: p.BSSID, To: in.BSSID, FromRSSI: p.RSSI, ToRSSI: in.RSSI, FromCh: p.Ch, ToCh: in.Ch}
	}
	tr.minute = minute
	if tr.n == 0 || in.RSSI < tr.rssiMin {
		tr.rssiMin = in.RSSI
	}
	tr.n++
	tr.rssiSum += float64(in.RSSI)
	if in.Noise != 0 {
		tr.noiseSum += in.Noise
		tr.noiseN++
	}
	tr.last = in
	cp := in
	tr.latest = &cp
	tr.recent = append(tr.recent, wifiPoint{t, in.RSSI})
	cut := t - 3*3600*1000
	k := 0
	for k < len(tr.recent) && tr.recent[k].t < cut {
		k++
	}
	if k > 0 {
		tr.recent = append(tr.recent[:0:0], tr.recent[k:]...)
	}
	wifis.mu.Unlock()
	if flush != nil {
		s.saveWiFiRow(*flush)
	}
	if roam != nil {
		if _, err := s.db.Exec(`INSERT INTO wifi_roam(agent_id,t,ssid,from_bssid,to_bssid,from_rssi,to_rssi,from_ch,to_ch) VALUES(?,?,?,?,?,?,?,?,?)`,
			agentID, roam.T, roam.SSID, roam.From, roam.To, roam.FromRSSI, roam.ToRSSI, roam.FromCh, roam.ToCh); err != nil {
			log.Printf("save roaming: %v", err)
		}
	}
	s.checkWiFiAlerts(agentID)
	s.checkRoamRate(agentID)
}

func (tr *wifiTrack) reset() { tr.n, tr.rssiSum, tr.rssiMin, tr.noiseSum, tr.noiseN = 0, 0, 0, 0, 0 }

type wifiRow struct {
	agent       string
	t           int64
	rssi        float64
	min, noise  int
	ch, freq    int
	tx, rx      float64
	ssid, bssid string
}

func (tr *wifiTrack) row(agent string) wifiRow {
	r := wifiRow{agent: agent, t: tr.minute, rssi: tr.rssiSum / float64(tr.n), min: tr.rssiMin, ch: tr.last.Ch, freq: tr.last.Freq,
		tx: tr.last.Tx, rx: tr.last.Rx, ssid: tr.last.SSID, bssid: tr.last.BSSID}
	if tr.noiseN > 0 {
		r.noise = tr.noiseSum / tr.noiseN
	}
	return r
}

func (s *Server) saveWiFiRow(r wifiRow) {
	if _, err := s.db.Exec(`INSERT OR REPLACE INTO wifi_metrics(agent_id,t,rssi,rssi_min,noise,ch,freq,tx,rx,ssid,bssid) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		r.agent, r.t, round1(r.rssi), r.min, r.noise, r.ch, r.freq, round1(r.tx), round1(r.rx), r.ssid, r.bssid); err != nil {
		log.Printf("save Wi-Fi: %v", err)
	}
}

// flushWiFi sparar avslutade minuter.
func (s *Server) flushWiFi(force bool) {
	cur := now() - now()%60000
	var rows []wifiRow
	wifis.mu.Lock()
	for id, tr := range wifis.tracks {
		if tr.n > 0 && (force || tr.minute < cur) {
			rows = append(rows, tr.row(id))
			tr.reset()
		}
	}
	wifis.mu.Unlock()
	for _, r := range rows {
		s.saveWiFiRow(r)
	}
}

// wifiLatest returnerar senaste avläsningen om den är färsk (annars nil).
func (s *Server) wifiLatest(agentID string) *wifi.Info {
	wifis.mu.Lock()
	defer wifis.mu.Unlock()
	if tr := wifis.tracks[agentID]; tr != nil && tr.latest != nil && now()-tr.latest.T < 5*60000 {
		cp := *tr.latest
		return &cp
	}
	return nil
}

func (s *Server) pruneWiFi(aggDays int) {
	cut := now() - int64(aggDays)*86400000
	_, _ = s.db.Exec(`DELETE FROM wifi_metrics WHERE t < ?`, cut)
	_, _ = s.db.Exec(`DELETE FROM wifi_roam WHERE t < ?`, cut)
	_, _ = s.db.Exec(`DELETE FROM wifi_metrics WHERE agent_id NOT IN (SELECT id FROM agents)`)
	_, _ = s.db.Exec(`DELETE FROM wifi_roam WHERE agent_id NOT IN (SELECT id FROM agents)`)
}

// ---------- historik ----------

// WiFiHistory är svaret för grafer över signalstyrka.
type WiFiHistory struct {
	Bucket  int64      `json:"bucket"`
	T       []int64    `json:"t"`
	RSSI    []*float64 `json:"rssi"`
	RSSIMin []*float64 `json:"rssiMin"`
	Noise   []*float64 `json:"noise"`
	Roams   []Roam     `json:"roams"`
	APs     []APStat   `json:"aps"`
}

// APStat sammanfattar tiden mot en åtkomstpunkt under perioden.
type APStat struct {
	BSSID   string  `json:"bssid"`
	SSID    string  `json:"ssid"`
	Ch      int     `json:"ch"`
	Minutes int     `json:"minutes"`
	RSSI    float64 `json:"rssi"`
}

func (s *Server) wifiHistory(agentID string, from, to int64) (WiFiHistory, error) {
	s.flushWiFi(false)
	span := to - from
	bucket := int64(60000)
	switch {
	case span > 14*86400000:
		bucket = 3600000
	case span > 2*86400000:
		bucket = 900000
	}
	h := WiFiHistory{Bucket: bucket, T: []int64{}, RSSI: []*float64{}, RSSIMin: []*float64{}, Noise: []*float64{}, Roams: []Roam{}, APs: []APStat{}}
	rows, err := s.db.Query(`SELECT t,rssi,rssi_min,noise,ch,ssid,bssid FROM wifi_metrics WHERE agent_id=? AND t>? AND t<=? ORDER BY t`, agentID, from-bucket, to)
	if err != nil {
		return h, err
	}
	type acc struct {
		n, nn    int
		sum, nsm float64
		min      int
	}
	var order []int64
	buckets := map[int64]*acc{}
	aps := map[string]*APStat{}
	var apOrder []string
	for rows.Next() {
		var t int64
		var rssi float64
		var mn, noise, ch int
		var ssid, bssid string
		if err := rows.Scan(&t, &rssi, &mn, &noise, &ch, &ssid, &bssid); err != nil {
			rows.Close()
			return h, err
		}
		b := t - t%bucket
		a := buckets[b]
		if a == nil {
			a = &acc{min: mn}
			buckets[b] = a
			order = append(order, b)
		}
		a.n++
		a.sum += rssi
		if mn < a.min {
			a.min = mn
		}
		if noise != 0 {
			a.nn++
			a.nsm += float64(noise)
		}
		if bssid != "" {
			ap := aps[bssid]
			if ap == nil {
				ap = &APStat{BSSID: bssid}
				aps[bssid] = ap
				apOrder = append(apOrder, bssid)
			}
			ap.SSID, ap.Ch = ssid, ch
			ap.RSSI = (ap.RSSI*float64(ap.Minutes) + rssi) / float64(ap.Minutes+1)
			ap.Minutes++
		}
	}
	rows.Close()
	pf := func(v float64) *float64 { v = round1(v); return &v }
	for _, b := range order {
		a := buckets[b]
		h.T = append(h.T, b)
		h.RSSI = append(h.RSSI, pf(a.sum/float64(a.n)))
		h.RSSIMin = append(h.RSSIMin, pf(float64(a.min)))
		if a.nn > 0 {
			h.Noise = append(h.Noise, pf(a.nsm/float64(a.nn)))
		} else {
			h.Noise = append(h.Noise, nil)
		}
	}
	for _, b := range apOrder {
		ap := *aps[b]
		ap.RSSI = round1(ap.RSSI)
		h.APs = append(h.APs, ap)
	}
	rr, err := s.db.Query(`SELECT t,ssid,from_bssid,to_bssid,from_rssi,to_rssi,from_ch,to_ch FROM wifi_roam WHERE agent_id=? AND t>? AND t<=? ORDER BY t`, agentID, from, to)
	if err != nil {
		return h, err
	}
	defer rr.Close()
	for rr.Next() {
		var r Roam
		if rr.Scan(&r.T, &r.SSID, &r.From, &r.To, &r.FromRSSI, &r.ToRSSI, &r.FromCh, &r.ToCh) == nil {
			h.Roams = append(h.Roams, r)
		}
	}
	return h, rr.Err()
}

// ---------- larm ----------

// evalRSSI avgör om signalen legat under gränsen hela fönstret (r.Samples minuter).
func evalRSSI(r alert.Alert, recent []wifiPoint, t int64) (bool, string) {
	window := int64(r.Samples) * 60000
	var sum float64
	n := 0
	var first int64
	under := true
	for _, p := range recent {
		if p.t < t-window {
			continue
		}
		if first == 0 {
			first = p.t
		}
		sum += float64(p.rssi)
		n++
		if float64(p.rssi) >= r.Threshold {
			under = false
		}
	}
	if n == 0 {
		return false, ""
	}
	covered := t-first >= window*8/10 || r.Samples <= 1
	return under && covered, fmt.Sprintf(i18n.T("Wi-Fi signal %.0f dBm on average over the last %d min"), sum/float64(n), r.Samples)
}

func (s *Server) checkWiFiAlerts(agentID string) {
	s.mu.RLock()
	a, ok := s.agents[agentID]
	if !ok {
		s.mu.RUnlock()
		return
	}
	cust := s.customerOfAgent(a)
	site := s.sites[a.SiteID]
	name := s.customers[site.CustomerID].Name + " · " + site.Name + " · " + a.Name
	hostname := a.Hostname
	var rules []Rule
	for _, r := range s.rules {
		if r.Condition == alert.CondRSSI && (r.CustomerID == "" || r.CustomerID == cust) {
			rules = append(rules, r)
		}
	}
	s.mu.RUnlock()
	if len(rules) == 0 {
		return
	}
	wifis.mu.Lock()
	var recent []wifiPoint
	if tr := wifis.tracks[agentID]; tr != nil {
		recent = append(recent, tr.recent...)
	}
	wifis.mu.Unlock()
	t := now()
	for _, r := range rules {
		met, detail := evalRSSI(r.Alert, recent, t)
		key := r.ID + "|" + agentID
		s.host.mu.Lock()
		since, active := s.host.alerts[key]
		kind := ""
		switch {
		case met && !active:
			s.host.alerts[key] = t
			kind = "fire"
		case !met && active:
			delete(s.host.alerts, key)
			if r.OnClear {
				kind = "clear"
				detail = fmt.Sprintf(i18n.T("%s (the alert lasted %s)"), detail, time.Duration(t-since)*time.Millisecond/time.Second*time.Second)
			}
		}
		s.host.mu.Unlock()
		if kind == "" {
			continue
		}
		s.eng.Fire(r.Alert, alert.Event{Kind: kind, AlertID: r.ID, AlertName: r.Name, TargetID: agentID, TargetName: name, Host: hostname,
			Time: t, Detail: detail, Popup: r.Popup, Sound: r.Sound && kind == "fire", SoundName: r.SoundName})
	}
}
