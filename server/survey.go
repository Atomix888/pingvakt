package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"pingvakt/core/agentproto"
	"pingvakt/core/alert"
	"pingvakt/core/i18n"
	"pingvakt/core/netscan"
	"pingvakt/core/wifi"
)

const surveySchema = `
CREATE TABLE IF NOT EXISTS wifi_scans (agent_id TEXT NOT NULL, t INTEGER NOT NULL, data TEXT NOT NULL, PRIMARY KEY (agent_id, t)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS agent_opts (agent_id TEXT PRIMARY KEY, wifi_scan_min INTEGER NOT NULL DEFAULT 0);
`

// SurveyBSS är ett grannät med tillverkare.
type SurveyBSS struct {
	wifi.BSS
	Vendor string `json:"vendor,omitempty"`
	Random bool   `json:"random,omitempty"` // lokalt administrerad MAC (t.ex. extra SSID på samma AP)
}

// SurveyView är en avsökning som den visas i gränssnittet.
type SurveyView struct {
	T    int64       `json:"t"`
	Err  string      `json:"err,omitempty"`
	Tool string      `json:"tool,omitempty"`
	BSS  []SurveyBSS `json:"bss"`
}

type stickyState struct {
	since int64
	n     int
	text  string
}

var surveys = struct {
	sync.Mutex
	latest map[string]*wifi.Scan
	sticky map[string]*stickyState // regel|agent
}{latest: map[string]*wifi.Scan{}, sticky: map[string]*stickyState{}}

func (s *Server) surveyMin(agentID string) int {
	var m int
	_ = s.db.QueryRow(`SELECT wifi_scan_min FROM agent_opts WHERE agent_id=?`, agentID).Scan(&m)
	return m
}

func (s *Server) ingestSurvey(agentID string, sc wifi.Scan) {
	if sc.T == 0 {
		sc.T = now()
	}
	b, _ := json.Marshal(sc)
	if _, err := s.db.Exec(`INSERT OR REPLACE INTO wifi_scans(agent_id,t,data) VALUES(?,?,?)`, agentID, sc.T, string(b)); err != nil {
		log.Printf("save Wi-Fi scan: %v", err)
	}
	cp := sc
	surveys.Lock()
	surveys.latest[agentID] = &cp
	surveys.Unlock()
	s.checkSticky(agentID, sc)
	s.mu.RLock()
	cust := ""
	if a, ok := s.agents[agentID]; ok {
		cust = s.customerOfAgent(a)
	}
	s.mu.RUnlock()
	msg, _ := json.Marshal(map[string]any{"type": "survey", "agentId": agentID})
	s.broadcastRaw(cust, msg)
}

func (s *Server) loadSurvey(agentID string, at int64) *wifi.Scan {
	if at == 0 {
		surveys.Lock()
		l := surveys.latest[agentID]
		surveys.Unlock()
		if l != nil {
			return l
		}
	}
	q := `SELECT data FROM wifi_scans WHERE agent_id=? ORDER BY t DESC LIMIT 1`
	args := []any{agentID}
	if at > 0 {
		q = `SELECT data FROM wifi_scans WHERE agent_id=? AND t<=? ORDER BY t DESC LIMIT 1`
		args = append(args, at)
	}
	var data string
	if s.db.QueryRow(q, args...).Scan(&data) != nil {
		return nil
	}
	var sc wifi.Scan
	if json.Unmarshal([]byte(data), &sc) != nil {
		return nil
	}
	if at == 0 {
		surveys.Lock()
		surveys.latest[agentID] = &sc
		surveys.Unlock()
	}
	return &sc
}

func surveyView(sc *wifi.Scan) SurveyView {
	v := SurveyView{T: sc.T, Err: sc.Err, Tool: sc.Tool, BSS: []SurveyBSS{}}
	for _, b := range sc.BSS {
		x := SurveyBSS{BSS: b}
		if b.BSSID != "" {
			x.Random = isLocalMAC(b.BSSID)
			x.Vendor = netscan.Vendor(b.BSSID)
		}
		v.BSS = append(v.BSS, x)
	}
	return v
}

// isLocalMAC: andra biten i första byten anger en lokalt administrerad adress.
func isLocalMAC(mac string) bool {
	if len(mac) < 2 {
		return false
	}
	v, err := strconv.ParseUint(mac[:2], 16, 8)
	return err == nil && v&2 != 0
}

func (s *Server) pruneSurvey(aggDays int) {
	days := aggDays
	if days > 30 {
		days = 30 // avsökningar tar plats – spara högst en månad
	}
	_, _ = s.db.Exec(`DELETE FROM wifi_scans WHERE t < ?`, now()-int64(days)*86400000)
}

// ---------- larm: klistrig klient och många roamingar ----------

// stickyCheck avgör om datorn sitter kvar på en svag åtkomstpunkt fast en starkare med samma SSID syns.
func stickyCheck(cur *wifi.Info, sc wifi.Scan, limit float64, diff int) (bool, string) {
	if cur == nil || cur.SSID == "" || float64(cur.RSSI) >= limit {
		return false, ""
	}
	var best *wifi.BSS
	for i := range sc.BSS {
		b := &sc.BSS[i]
		if b.SSID != cur.SSID || strings.EqualFold(b.BSSID, cur.BSSID) || b.Conn {
			continue
		}
		if best == nil || b.RSSI > best.RSSI {
			best = b
		}
	}
	if best == nil || best.RSSI-cur.RSSI < diff {
		return false, ""
	}
	return true, fmt.Sprintf(i18n.T("Connected to %s at %d dBm although %s (channel %d) is heard at %d dBm"), cur.BSSID, cur.RSSI, best.BSSID, best.Ch, best.RSSI)
}

func (s *Server) agentAlertInfo(agentID string, cond alert.Condition) (name, host string, rules []Rule) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.agents[agentID]
	if !ok {
		return
	}
	cust := s.customerOfAgent(a)
	site := s.sites[a.SiteID]
	name = s.customers[site.CustomerID].Name + " · " + site.Name + " · " + a.Name
	host = a.Hostname
	for _, r := range s.rules {
		if r.Condition == cond && (r.CustomerID == "" || r.CustomerID == cust) {
			rules = append(rules, r)
		}
	}
	return
}

// setHostAlert tänder eller släcker ett larm som lagras bland värdlarmen och skickar händelsen.
func (s *Server) setHostAlert(r Rule, targetID, name, host string, met bool, detail, clearDetail string) {
	t := now()
	key := r.ID + "|" + targetID
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
			detail = fmt.Sprintf(i18n.T("%s (the alert lasted %s)"), clearDetail, time.Duration(t-since)*time.Millisecond/time.Second*time.Second)
		}
	}
	s.host.mu.Unlock()
	if kind == "" {
		return
	}
	s.eng.Fire(r.Alert, alert.Event{Kind: kind, AlertID: r.ID, AlertName: r.Name, TargetID: targetID, TargetName: name, Host: host,
		Time: t, Detail: detail, Popup: r.Popup, Sound: r.Sound && kind == "fire", SoundName: r.SoundName})
}

func (s *Server) checkSticky(agentID string, sc wifi.Scan) {
	name, host, rules := s.agentAlertInfo(agentID, alert.CondSticky)
	if len(rules) == 0 {
		return
	}
	cur := s.wifiLatest(agentID)
	t := now()
	for _, r := range rules {
		bad, text := stickyCheck(cur, sc, r.Threshold, r.Count)
		key := r.ID + "|" + agentID
		surveys.Lock()
		st := surveys.sticky[key]
		if bad {
			if st == nil {
				st = &stickyState{since: t}
				surveys.sticky[key] = st
			}
			st.n++
			st.text = text
		} else {
			delete(surveys.sticky, key)
			st = nil
		}
		met := st != nil && st.n >= 2 && t-st.since >= int64(r.Samples)*60000*8/10
		surveys.Unlock()
		s.setHostAlert(r, agentID, name, host, met, text, i18n.T("The computer is connected to the strongest access point again"))
	}
}

// checkRoamRate körs vid varje Wi-Fi-avläsning: larmar vid fler än N roamingar under X minuter.
func (s *Server) checkRoamRate(agentID string) {
	name, host, rules := s.agentAlertInfo(agentID, alert.CondRoamRate)
	if len(rules) == 0 {
		return
	}
	t := now()
	for _, r := range rules {
		var n int
		_ = s.db.QueryRow(`SELECT count(*) FROM wifi_roam WHERE agent_id=? AND t>=?`, agentID, t-int64(r.Samples)*60000).Scan(&n)
		met := float64(n) > r.Threshold
		s.setHostAlert(r, agentID, name, host, met, fmt.Sprintf(i18n.T("%d roams in the last %d minutes"), n, r.Samples), i18n.T("Roaming is normal again"))
	}
}

// ---------- API ----------

func (s *Server) handleSurveyGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.agentVisible(r, id) {
		httpErr(w, 404, "the agent does not exist")
		return
	}
	at, _ := strconv.ParseInt(r.URL.Query().Get("t"), 10, 64)
	out := map[string]any{"interval": s.surveyMin(id)}
	if sc := s.loadSurvey(id, at); sc != nil {
		out["scan"] = surveyView(sc)
	}
	if cur := s.wifiLatest(id); cur != nil {
		out["current"] = cur
	}
	// tidpunkter för tidigare avsökningar (senaste veckan)
	times := []int64{}
	if rows, err := s.db.Query(`SELECT t FROM wifi_scans WHERE agent_id=? AND t>=? ORDER BY t DESC LIMIT 500`, id, now()-7*86400000); err == nil {
		for rows.Next() {
			var t int64
			if rows.Scan(&t) == nil {
				times = append(times, t)
			}
		}
		rows.Close()
	}
	out["times"] = times
	writeJSON(w, out)
}

func (s *Server) handleSurveyConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		IntervalMin int `json:"intervalMin"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if err := s.agentManaged(id); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if in.IntervalMin < 0 {
		in.IntervalMin = 0
	}
	if in.IntervalMin > 0 && in.IntervalMin < 2 {
		in.IntervalMin = 2
	}
	if _, err := s.db.Exec(`INSERT INTO agent_opts(agent_id,wifi_scan_min) VALUES(?,?) ON CONFLICT(agent_id) DO UPDATE SET wifi_scan_min=excluded.wifi_scan_min`, id, in.IntervalMin); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.bumpConfig(id)
	s.audit(r, "agent", id, i18n.T("changed the Wi-Fi scan schedule"))
	writeJSON(w, in)
}

func (s *Server) handleSurveyRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.agentVisible(r, id) {
		httpErr(w, 404, "the agent does not exist")
		return
	}
	res, err := s.runAgentCommand(id, agentproto.Command{Kind: agentproto.CmdWiFiScan}, 90*time.Second)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if res.Survey == nil {
		httpErr(w, 400, TEmpty(res.Err, "the agent cannot scan for Wi-Fi networks – update it"))
		return
	}
	s.ingestSurvey(id, *res.Survey)
	writeJSON(w, surveyView(res.Survey))
}

// TEmpty returnerar s, eller standardtexten om s är tom.
func TEmpty(s, def string) string {
	if s == "" || s == "unknown command" {
		return def
	}
	return s
}
