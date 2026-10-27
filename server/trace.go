package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"pingvakt/core/agentproto"
	"pingvakt/core/alert"
	"pingvakt/core/i18n"
	"pingvakt/core/monitor"
	"pingvakt/core/probe"
	"pingvakt/core/trace"
)

const traceSchema = `
CREATE TABLE IF NOT EXISTS traces (
  id TEXT PRIMARY KEY, target_id TEXT NOT NULL, agent_id TEXT NOT NULL, t INTEGER NOT NULL, t_end INTEGER NOT NULL,
  host TEXT NOT NULL, hops TEXT NOT NULL, err TEXT NOT NULL, path TEXT NOT NULL, auto INTEGER NOT NULL, changed INTEGER NOT NULL,
  prev_path TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS traces_target ON traces(target_id, t);
`

// TraceRun är en traceroute som körts (eller körs) från en agent.
type TraceRun struct {
	ID       string      `json:"id"`
	TargetID string      `json:"targetId"`
	AgentID  string      `json:"agentId"`
	Host     string      `json:"host"`
	T        int64       `json:"t"`
	End      int64       `json:"end"`
	Hops     []trace.Hop `json:"hops"`
	Err      string      `json:"err,omitempty"`
	Path     string      `json:"path,omitempty"`
	PrevPath string      `json:"prevPath,omitempty"`
	Auto     bool        `json:"auto"`
	Changed  bool        `json:"changed"`
	Pending  bool        `json:"pending,omitempty"`
}

type pathState struct {
	cur, cand string
	candN     int
}

type traceState struct {
	mu      sync.Mutex
	queue   map[string][]agentproto.Command // agent -> kommandon som inte hämtats
	pending map[string]*TraceRun            // kommando-ID -> körning
	last    map[string]int64                // mål -> senaste automatiska körning
	paths   map[string]*pathState           // mål -> känd väg
}

var traces = &traceState{queue: map[string][]agentproto.Command{}, pending: map[string]*TraceRun{},
	last: map[string]int64{}, paths: map[string]*pathState{}}

// traceHost tar fram vilken värd traceroute ska köras mot för ett mål.
func traceHost(t monitor.Target) string {
	switch t.Kind {
	case probe.KindHTTP:
		if u, err := url.Parse(t.Host); err == nil {
			return u.Hostname()
		}
	case probe.KindDNS:
		if t.DNSServer != "" {
			h := t.DNSServer
			if hh, _, err := net.SplitHostPort(h); err == nil {
				h = hh
			}
			return strings.Trim(h, "[]")
		}
	}
	return t.Host
}

// loadTraces läser in senaste kända väg per mål vid start.
func (s *Server) loadTraces() {
	rows, err := s.db.Query(`SELECT target_id, max(t), path FROM traces WHERE auto=1 AND path<>'' GROUP BY target_id`)
	if err != nil {
		return
	}
	defer rows.Close()
	traces.mu.Lock()
	defer traces.mu.Unlock()
	for rows.Next() {
		var id, p string
		var t int64
		if rows.Scan(&id, &t, &p) == nil {
			traces.last[id] = t
			traces.paths[id] = &pathState{cur: p}
		}
	}
}

// startTrace lägger ett traceroute-kommando i kö till målets agent.
func (s *Server) startTrace(targetID string, auto bool) (*TraceRun, error) {
	s.mu.RLock()
	t, ok := s.targets[targetID]
	var host, agentID string
	if ok {
		host, agentID = traceHost(t.Target), t.AgentID
	}
	s.mu.RUnlock()
	if !ok {
		return nil, errNotFound
	}
	traces.mu.Lock()
	defer traces.mu.Unlock()
	for _, p := range traces.pending {
		if p.TargetID == targetID && now()-p.T < 4*60000 {
			cp := *p
			return &cp, nil // pågår redan
		}
	}
	id := monitor.NewID()
	run := &TraceRun{ID: id, TargetID: targetID, AgentID: agentID, Host: host, T: now(), Hops: []trace.Hop{}, Auto: auto, Pending: true}
	traces.pending[id] = run
	traces.queue[agentID] = append(traces.queue[agentID], agentproto.Command{ID: id, Kind: agentproto.CmdTrace, TargetID: targetID, Host: host, MaxHops: 30})
	if auto {
		traces.last[targetID] = run.T
	}
	cp := *run
	return &cp, nil
}

// takeCommands hämtar köade kommandon för agenten (anropas vid rapport).
func (s *Server) takeCommands(agentID string) []agentproto.Command {
	traces.mu.Lock()
	defer traces.mu.Unlock()
	q := traces.queue[agentID]
	delete(traces.queue, agentID)
	// rensa gamla körningar som aldrig besvarats
	for id, p := range traces.pending {
		if now()-p.T > 10*60000 {
			delete(traces.pending, id)
		}
	}
	return q
}

// scheduleTraces startar automatiska körningar för mål med vägbevakning.
func (s *Server) scheduleTraces() {
	var list []string
	s.mu.RLock()
	for id, t := range s.targets {
		if t.TraceMin <= 0 || t.Paused {
			continue
		}
		a, ok := s.agents[t.AgentID]
		if !ok || !s.agentOnline(a) {
			continue
		}
		list = append(list, id)
	}
	intervals := map[string]int64{}
	for _, id := range list {
		intervals[id] = int64(s.targets[id].TraceMin) * 60000
	}
	s.mu.RUnlock()
	for _, id := range list {
		traces.mu.Lock()
		last := traces.last[id]
		traces.mu.Unlock()
		if now()-last >= intervals[id] {
			_, _ = s.startTrace(id, true)
		}
	}
}

// pathSignature är vägen som en sträng av svarande hopp.
func pathSignature(hops []trace.Hop) string {
	var parts []string
	for _, h := range hops {
		if h.Addr != "" && h.Addr != "*" && h.RTT >= 0 {
			parts = append(parts, h.Addr)
		}
	}
	return strings.Join(parts, ">")
}

// observePath jämför en ny väg med den kända. En ändring räknas först när
// samma nya väg setts två gånger i rad, så att enstaka avvikelser inte larmar.
func observePath(ps *pathState, sig string) (changed bool, prev string) {
	switch {
	case sig == "":
		return false, ""
	case ps.cur == "":
		ps.cur = sig
	case sig == ps.cur:
		ps.cand, ps.candN = "", 0
	case sig == ps.cand:
		ps.candN++
		if ps.candN >= 2 {
			prev = ps.cur
			ps.cur, ps.cand, ps.candN = sig, "", 0
			return true, prev
		}
	default:
		ps.cand, ps.candN = sig, 1
	}
	return false, ""
}

// handleCommandResult tar emot svar på kommandon från agenter.
func (s *Server) handleCommandResult(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	s.mu.RLock()
	agentID, ok := s.tokens[hashToken(strings.TrimPrefix(auth, "Bearer "))]
	s.mu.RUnlock()
	if !ok || !strings.HasPrefix(auth, "Bearer ") {
		httpErr(w, http.StatusUnauthorized, "unknown token")
		return
	}
	var res agentproto.CommandResult
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&res); err != nil {
		httpErr(w, 400, "invalid response")
		return
	}
	if deliverWaiter(res) {
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	if res.Kind == agentproto.CmdScan {
		s.scanResult(w, agentID, res)
		return
	}
	traces.mu.Lock()
	run, ok := traces.pending[res.ID]
	if ok && run.AgentID == agentID {
		delete(traces.pending, res.ID)
	}
	traces.mu.Unlock()
	if !ok || run.AgentID != agentID {
		httpErr(w, 404, "unknown command")
		return
	}
	run.Pending = false
	run.End = res.End
	if res.Hops != nil {
		run.Hops = res.Hops
	}
	if len(run.Hops) > 64 {
		run.Hops = run.Hops[:64]
	}
	run.Err = res.Err
	run.Path = pathSignature(run.Hops)
	s.mu.RLock()
	t, exists := s.targets[run.TargetID]
	tracking := exists && t.TraceMin > 0
	var tc Target
	if exists {
		tc = *t
	}
	s.mu.RUnlock()
	if tracking && run.Err == "" {
		traces.mu.Lock()
		ps := traces.paths[run.TargetID]
		if ps == nil {
			ps = &pathState{}
			traces.paths[run.TargetID] = ps
		}
		run.Changed, run.PrevPath = observePath(ps, run.Path)
		traces.mu.Unlock()
	}
	hops, _ := json.Marshal(run.Hops)
	if _, err := s.db.Exec(`INSERT OR REPLACE INTO traces(id,target_id,agent_id,t,t_end,host,hops,err,path,auto,changed,prev_path) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		run.ID, run.TargetID, agentID, run.T, run.End, run.Host, string(hops), run.Err, run.Path, b2i(run.Auto), b2i(run.Changed), run.PrevPath); err != nil {
		log.Printf("save traceroute: %v", err)
	}
	cust := ""
	if exists {
		s.mu.RLock()
		cust = s.customerOfTarget(&tc)
		s.mu.RUnlock()
	}
	b, _ := json.Marshal(map[string]any{"type": "trace", "trace": run})
	s.broadcastRaw(cust, b)
	if run.Changed {
		s.firePathChange(tc, cust, run)
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Server) firePathChange(t Target, cust string, run *TraceRun) {
	s.mu.RLock()
	var rules []Rule
	for _, r := range s.rules {
		if r.Condition == alert.CondPath && (r.CustomerID == "" || r.CustomerID == cust) {
			rules = append(rules, r)
		}
	}
	name := s.displayName(&t)
	s.mu.RUnlock()
	old, cur := strings.Split(run.PrevPath, ">"), strings.Split(run.Path, ">")
	detail := i18n.T("The path has changed: ") + diffPath(old, cur)
	for _, r := range rules {
		s.eng.Fire(r.Alert, alert.Event{Kind: "fire", AlertID: r.ID, AlertName: r.Name, TargetID: t.ID, TargetName: name, Host: t.Host,
			Time: now(), Detail: detail, Popup: r.Popup, Sound: r.Sound, SoundName: r.SoundName})
	}
}

// diffPath beskriver första skillnaden mellan två vägar.
func diffPath(old, cur []string) string {
	i := 0
	for i < len(old) && i < len(cur) && old[i] == cur[i] {
		i++
	}
	switch {
	case i < len(old) && i < len(cur):
		return fmt.Sprintf(i18n.T("hop %d %s → %s (%d → %d hops)"), i+1, old[i], cur[i], len(old), len(cur))
	case len(cur) > len(old):
		return fmt.Sprintf(i18n.T("new hops after hop %d (%d → %d hops)"), i, len(old), len(cur))
	default:
		return fmt.Sprintf(i18n.T("shorter path (%d → %d hops)"), len(old), len(cur))
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// ---------- API för webbgränssnittet ----------

func (s *Server) handleStartTrace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.canSeeTarget(r, id) {
		httpErr(w, 404, "the target does not exist")
		return
	}
	run, err := s.startTrace(id, false)
	if err != nil {
		httpErr(w, 404, "the target does not exist")
		return
	}
	writeJSON(w, run)
}

func (s *Server) handleTraces(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.canSeeTarget(r, id) {
		httpErr(w, 404, "the target does not exist")
		return
	}
	out := []TraceRun{}
	traces.mu.Lock()
	for _, p := range traces.pending {
		if p.TargetID == id {
			out = append(out, *p)
		}
	}
	traces.mu.Unlock()
	q := `SELECT id,agent_id,t,t_end,host,hops,err,path,auto,changed,prev_path FROM traces WHERE target_id=? ORDER BY t DESC LIMIT 50`
	if r.URL.Query().Get("changes") == "1" {
		q = `SELECT id,agent_id,t,t_end,host,hops,err,path,auto,changed,prev_path FROM traces WHERE target_id=? AND changed=1 ORDER BY t DESC LIMIT 50`
	}
	rows, err := s.db.Query(q, id)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	for rows.Next() {
		var t TraceRun
		var hops string
		var auto, changed int
		if rows.Scan(&t.ID, &t.AgentID, &t.T, &t.End, &t.Host, &hops, &t.Err, &t.Path, &auto, &changed, &t.PrevPath) == nil {
			t.TargetID, t.Auto, t.Changed = id, auto == 1, changed == 1
			_ = json.Unmarshal([]byte(hops), &t.Hops)
			out = append(out, t)
		}
	}
	writeJSON(w, out)
}

// canSeeTarget kontrollerar att användaren får se målet.
func (s *Server) canSeeTarget(r *http.Request, id string) bool {
	u := userFrom(r)
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.targets[id]
	return ok && canSee(u, s.customerOfTarget(t))
}

func (s *Server) pruneTraces(aggDays int) {
	_, _ = s.db.Exec(`DELETE FROM traces WHERE t < ?`, now()-int64(aggDays)*86400000)
	_, _ = s.db.Exec(`DELETE FROM traces WHERE target_id NOT IN (SELECT id FROM targets)`)
}
