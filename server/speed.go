package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"pingvakt/core/agentproto"
	"pingvakt/core/alert"
	"pingvakt/core/i18n"
	"pingvakt/core/speedtest"
)

const speedSchema = `
CREATE TABLE IF NOT EXISTS speedtests (
  agent_id TEXT NOT NULL, t INTEGER NOT NULL, target TEXT NOT NULL, down REAL NOT NULL, up REAL NOT NULL,
  latency REAL NOT NULL, jitter REAL NOT NULL, err TEXT NOT NULL,
  PRIMARY KEY (agent_id, t)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS speed_cfg (agent_id TEXT PRIMARY KEY, data TEXT NOT NULL);
`

const maxSpeedBytes = 512 << 20

// agentFromToken returnerar agenten för en Bearer-token.
func (s *Server) agentFromToken(r *http.Request) (string, bool) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.tokens[hashToken(strings.TrimPrefix(auth, "Bearer "))]
	return id, ok
}

var speedBlock = func() []byte {
	b := make([]byte, 64<<10)
	_, _ = rand.Read(b) // slumpdata så att ingen komprimering på vägen förvränger resultatet
	return b
}()

// handleSpeedDown skickar slumpdata till agenten.
func (s *Server) handleSpeedDown(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.agentFromToken(r); !ok {
		httpErr(w, 401, "unknown token")
		return
	}
	n, _ := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
	if n < 0 {
		n = 0
	}
	if n > maxSpeedBytes {
		n = maxSpeedBytes
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
	for n > 0 {
		k := int64(len(speedBlock))
		if k > n {
			k = n
		}
		if _, err := w.Write(speedBlock[:k]); err != nil {
			return
		}
		n -= k
	}
}

// handleSpeedUp tar emot och kastar data från agenten.
func (s *Server) handleSpeedUp(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.agentFromToken(r); !ok {
		httpErr(w, 401, "unknown token")
		return
	}
	n, _ := io.Copy(io.Discard, io.LimitReader(r.Body, maxSpeedBytes))
	writeJSON(w, map[string]int64{"bytes": n})
}

func (s *Server) speedConfig(agentID string) *speedtest.Config {
	var data string
	if s.db.QueryRow(`SELECT data FROM speed_cfg WHERE agent_id=?`, agentID).Scan(&data) != nil {
		return nil
	}
	var c speedtest.Config
	if json.Unmarshal([]byte(data), &c) != nil || c.IntervalMin <= 0 {
		return nil
	}
	return &c
}

func (s *Server) ingestSpeed(agentID string, res []speedtest.Result) {
	for _, r := range res {
		if r.T == 0 {
			continue
		}
		_, _ = s.db.Exec(`INSERT OR REPLACE INTO speedtests(agent_id,t,target,down,up,latency,jitter,err) VALUES(?,?,?,?,?,?,?,?)`,
			agentID, r.T, r.Target, round1(r.Down), round1(r.Up), round1(r.Latency), round1(r.Jitter), r.Err)
	}
	if len(res) == 0 {
		return
	}
	s.checkSpeedAlerts(agentID)
	s.mu.RLock()
	cust := ""
	if a, ok := s.agents[agentID]; ok {
		cust = s.customerOfAgent(a)
	}
	s.mu.RUnlock()
	b, _ := json.Marshal(map[string]any{"type": "speed", "agentId": agentID})
	s.broadcastRaw(cust, b)
}

func (s *Server) recentSpeed(agentID string, n int) []speedtest.Result {
	rows, err := s.db.Query(`SELECT t,target,down,up,latency,jitter,err FROM speedtests WHERE agent_id=? ORDER BY t DESC LIMIT ?`, agentID, n)
	out := []speedtest.Result{}
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var r speedtest.Result
		if rows.Scan(&r.T, &r.Target, &r.Down, &r.Up, &r.Latency, &r.Jitter, &r.Err) == nil {
			out = append(out, r)
		}
	}
	return out
}

func (s *Server) checkSpeedAlerts(agentID string) {
	s.mu.RLock()
	a, ok := s.agents[agentID]
	if !ok {
		s.mu.RUnlock()
		return
	}
	cust := s.customerOfAgent(a)
	site := s.sites[a.SiteID]
	name := s.customers[site.CustomerID].Name + " · " + site.Name + " · " + a.Name
	var rules []Rule
	for _, r := range s.rules {
		if r.Condition == alert.CondSpeed && (r.CustomerID == "" || r.CustomerID == cust) {
			rules = append(rules, r)
		}
	}
	s.mu.RUnlock()
	if len(rules) == 0 {
		return
	}
	recent := s.recentSpeed(agentID, 20)
	var ok20 []speedtest.Result
	for _, r := range recent {
		if r.Err == "" {
			ok20 = append(ok20, r)
		}
	}
	t := now()
	for _, r := range rules {
		if len(ok20) == 0 {
			continue
		}
		need := r.Samples
		if need < 1 {
			need = 2
		}
		low, high := 0, 0
		for i := 0; i < len(ok20) && i < need; i++ {
			if ok20[i].Down < r.Threshold {
				low++
			} else {
				high++
			}
		}
		key := r.ID + "|speed:" + agentID
		s.host.mu.Lock()
		since, active := s.host.alerts[key]
		kind, detail := "", ""
		last := ok20[0]
		switch {
		case low >= need && !active:
			s.host.alerts[key] = t
			kind = "fire"
			detail = fmt.Sprintf(i18n.T("Speed test: %.1f Mbit/s down, %.1f up (below %g Mbit/s)"), last.Down, last.Up, r.Threshold)
		case active && ok20[0].Down >= r.Threshold:
			delete(s.host.alerts, key)
			if r.OnClear {
				kind = "clear"
				detail = fmt.Sprintf(i18n.T("Speed is normal again: %.1f Mbit/s (the alert lasted %s)"), last.Down, time.Duration(t-since)*time.Millisecond/time.Second*time.Second)
			}
		}
		_ = high
		s.host.mu.Unlock()
		if kind != "" {
			s.eng.Fire(r.Alert, alert.Event{Kind: kind, AlertID: r.ID, AlertName: r.Name, TargetID: "speed:" + agentID, TargetName: name,
				Time: t, Detail: detail, Popup: r.Popup, Sound: r.Sound && kind == "fire", SoundName: r.SoundName})
		}
	}
}

// speedInfo ger namn och kund för ett "speed:<agent>"-ID.
func (s *Server) speedInfo(id string) (name, cust string, ok bool) {
	agentID, found := strings.CutPrefix(id, "speed:")
	if !found {
		return "", "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok2 := s.agents[agentID]
	if !ok2 {
		return id, "", true
	}
	site := s.sites[a.SiteID]
	return s.customers[site.CustomerID].Name + " · " + site.Name + " · " + a.Name + " · " + i18n.T("Speed test"), site.CustomerID, true
}

// extraInfo löser upp namn/kund för larm-ID som inte är mål eller agenter.
func (s *Server) extraInfo(id string) (name, cust string, ok bool) {
	if n, c, ok := s.snmpInfo(id); ok {
		return n, c, true
	}
	if n, c, ok := s.integInfo(id); ok {
		return n, c, true
	}
	if n, c, ok := s.logInfo(id); ok {
		return n, c, true
	}
	return s.speedInfo(id)
}

// ---------- API ----------

// agentManaged kontrollerar att agenten styrs från servern (inte skrivbordsappen).
func (s *Server) agentManaged(id string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.agents[id]
	if !ok {
		return fmt.Errorf("%s", i18n.T("unknown agent"))
	}
	if a.Kind == agentproto.KindDesktop {
		return fmt.Errorf("%s", i18n.T("SNMP and speed tests need an installed agent, not the desktop app"))
	}
	return nil
}

func (s *Server) agentVisible(r *http.Request, id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.agents[id]
	return ok && canSee(userFrom(r), s.customerOfAgent(a))
}

func (s *Server) handleSpeedGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.agentVisible(r, id) {
		httpErr(w, 404, "the agent does not exist")
		return
	}
	to, _ := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	if to <= 0 {
		to = now()
	}
	if from <= 0 || from >= to {
		from = to - 30*86400000
	}
	rows, err := s.db.Query(`SELECT t,target,down,up,latency,jitter,err FROM speedtests WHERE agent_id=? AND t>=? AND t<=? ORDER BY t LIMIT 5000`, id, from, to)
	list := []speedtest.Result{}
	if err == nil {
		for rows.Next() {
			var x speedtest.Result
			if rows.Scan(&x.T, &x.Target, &x.Down, &x.Up, &x.Latency, &x.Jitter, &x.Err) == nil {
				list = append(list, x)
			}
		}
		rows.Close()
	}
	cfg := s.speedConfig(id)
	if cfg == nil {
		cfg = &speedtest.Config{Target: "cloudflare", Seconds: 8}
	}
	writeJSON(w, map[string]any{"config": cfg, "results": list})
}

func (s *Server) handleSpeedConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in speedtest.Config
	if !readJSON(w, r, &in) {
		return
	}
	if err := s.agentManaged(id); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	in.Normalize()
	b, _ := json.Marshal(in)
	if _, err := s.db.Exec(`INSERT INTO speed_cfg(agent_id,data) VALUES(?,?) ON CONFLICT(agent_id) DO UPDATE SET data=excluded.data`, id, string(b)); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.bumpConfig(id)
	s.audit(r, "agent", id, i18n.T("changed the speed test schedule"))
	writeJSON(w, in)
}

// handleSpeedRun kör ett hastighetstest nu och väntar på resultatet.
func (s *Server) handleSpeedRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.agentVisible(r, id) {
		httpErr(w, 404, "the agent does not exist")
		return
	}
	var in struct {
		Target string `json:"target"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
	if in.Target != "server" {
		in.Target = "cloudflare"
	}
	res, err := s.runAgentCommand(id, agentproto.Command{Kind: agentproto.CmdSpeed, Host: in.Target}, 2*time.Minute)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if res.Speed == nil {
		httpErr(w, 400, res.Err)
		return
	}
	writeJSON(w, res.Speed)
}

func (s *Server) pruneSpeed(aggDays int) {
	_, _ = s.db.Exec(`DELETE FROM speedtests WHERE t < ?`, now()-int64(aggDays)*86400000)
}
