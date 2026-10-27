package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"pingvakt/core/alert"
	"pingvakt/core/i18n"
	"pingvakt/core/logrx"
)

const logsSchema = `
CREATE TABLE IF NOT EXISTS logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT, customer_id TEXT NOT NULL, agent_id TEXT NOT NULL, t INTEGER NOT NULL,
  src TEXT NOT NULL, kind TEXT NOT NULL, sev INTEGER NOT NULL, host TEXT NOT NULL, app TEXT NOT NULL, text TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS logs_ct ON logs(customer_id, t);
`

// LogRow är ett lagrat loggmeddelande.
type LogRow struct {
	ID         int64  `json:"id"`
	CustomerID string `json:"customerId"`
	AgentID    string `json:"agentId,omitempty"`
	logrx.Message
}

var logState = struct {
	sync.Mutex
	rx       map[string]string // agent -> fel från mottagaren
	lastPush map[string]int64  // kund -> senaste SSE
	active   map[string]int64  // larmnyckel -> senaste matchning
	re       map[string]*regexp.Regexp
}{rx: map[string]string{}, lastPush: map[string]int64{}, active: map[string]int64{}, re: map[string]*regexp.Regexp{}}

func (s *Server) ingestLogs(agentID, customerID string, msgs []logrx.Message) {
	if len(msgs) == 0 {
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	st, err := tx.Prepare(`INSERT INTO logs(customer_id,agent_id,t,src,kind,sev,host,app,text) VALUES(?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return
	}
	for _, m := range msgs {
		if m.Host == "" {
			m.Host = m.Src
		}
		_, _ = st.Exec(customerID, agentID, m.T, m.Src, m.Kind, m.Sev, m.Host, m.App, m.Text)
	}
	st.Close()
	if err := tx.Commit(); err != nil {
		log.Printf("save logs: %v", err)
	}
	s.checkLogAlerts(customerID, msgs)
	t := now()
	logState.Lock()
	push := t-logState.lastPush[customerID] > 2000
	if push {
		logState.lastPush[customerID] = t
	}
	logState.Unlock()
	if push {
		b, _ := json.Marshal(map[string]any{"type": "logs", "customerId": customerID})
		s.broadcastRaw(customerID, b)
	}
}

func logMatcher(match string) func(string) bool {
	if match == "" {
		return func(string) bool { return true }
	}
	if len(match) > 2 && strings.HasPrefix(match, "/") && strings.HasSuffix(match, "/") {
		logState.Lock()
		re, ok := logState.re[match]
		if !ok {
			re, _ = regexp.Compile("(?i)" + match[1:len(match)-1])
			logState.re[match] = re
		}
		logState.Unlock()
		if re != nil {
			return re.MatchString
		}
	}
	low := strings.ToLower(match)
	return func(s string) bool { return strings.Contains(strings.ToLower(s), low) }
}

func (s *Server) checkLogAlerts(customerID string, msgs []logrx.Message) {
	s.mu.RLock()
	var rules []Rule
	for _, r := range s.rules {
		if r.Condition == alert.CondLog && (r.CustomerID == "" || r.CustomerID == customerID) {
			rules = append(rules, r)
		}
	}
	cname := s.customers[customerID].Name
	s.mu.RUnlock()
	for _, r := range rules {
		match := logMatcher(r.Match)
		for _, m := range msgs {
			if m.Sev > int(r.Threshold) || !match(m.Text+" "+m.App) {
				continue
			}
			host := m.Host
			if host == "" {
				host = m.Src
			}
			tid := "log:" + customerID + ":" + host
			logState.Lock()
			logState.active[r.ID+"|"+tid] = m.T
			logState.Unlock()
			name := cname + " · " + host
			if cname == "" {
				name = host
			}
			s.setHostAlert(r, tid, name, m.Src, true, fmt.Sprintf("[%s] %s", logrx.SevNames[clampSev(m.Sev)], m.Text), "")
		}
	}
}

func clampSev(n int) int {
	if n < 0 || n > 7 {
		return 7
	}
	return n
}

// sweepLogAlerts släcker logglarm när inget matchande kommit på Samples minuter.
func (s *Server) sweepLogAlerts() {
	t := now()
	logState.Lock()
	keys := map[string]int64{}
	for k, v := range logState.active {
		keys[k] = v
	}
	logState.Unlock()
	if len(keys) == 0 {
		return
	}
	s.mu.RLock()
	rules := map[string]Rule{}
	for id, r := range s.rules {
		rules[id] = r
	}
	s.mu.RUnlock()
	for k, last := range keys {
		rid, tid, _ := strings.Cut(k, "|")
		r, ok := rules[rid]
		if !ok {
			logState.Lock()
			delete(logState.active, k)
			logState.Unlock()
			continue
		}
		if t-last < int64(r.Samples)*60000 {
			continue
		}
		logState.Lock()
		delete(logState.active, k)
		logState.Unlock()
		name, _, _ := s.logInfo(tid)
		s.setHostAlert(r, tid, name, "", false, "", fmt.Sprintf(i18n.T("No matching log messages for %d minutes"), r.Samples))
	}
}

// logInfo ger namn och kund för "log:<kund>:<värd>".
func (s *Server) logInfo(id string) (name, cust string, ok bool) {
	rest, found := strings.CutPrefix(id, "log:")
	if !found {
		return "", "", false
	}
	cust, host, _ := strings.Cut(rest, ":")
	s.mu.RLock()
	cname := s.customers[cust].Name
	s.mu.RUnlock()
	if cname == "" {
		return host, cust, true
	}
	return cname + " · " + host, cust, true
}

func (s *Server) pruneLogs() {
	_, _ = s.db.Exec(`DELETE FROM logs WHERE t < ?`, now()-30*86400000)
}

// ---------- serverns egen mottagare (valfri) ----------

// startLogReceiver tar emot syslog/traps direkt på servern. Avsändaren kopplas till en kund
// genom SNMP-enheter, pingmål och integrationer med samma IP-adress.
func (s *Server) startLogReceiver(ctx context.Context, syslogAddr, trapAddr string) {
	if syslogAddr == "" && trapAddr == "" {
		return
	}
	var mu sync.Mutex
	buf := map[string][]logrx.Message{}
	rx := logrx.NewReceiver(func(m logrx.Message) {
		cust := s.customerOfIP(m.Src)
		mu.Lock()
		buf[cust] = append(buf[cust], m)
		mu.Unlock()
	})
	if syslogAddr != "" {
		if err := rx.ListenSyslog(ctx, syslogAddr); err != nil {
			log.Printf("syslog %s: %v", syslogAddr, err)
		} else {
			log.Printf("receiving syslog on %s", syslogAddr)
		}
	}
	if trapAddr != "" {
		if err := rx.ListenTraps(ctx, trapAddr); err != nil {
			log.Printf("SNMP traps %s: %v", trapAddr, err)
		} else {
			log.Printf("receiving SNMP traps on %s", trapAddr)
		}
	}
	go func() {
		tk := time.NewTicker(2 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
			}
			mu.Lock()
			cur := buf
			buf = map[string][]logrx.Message{}
			mu.Unlock()
			for cust, l := range cur {
				s.ingestLogs("", cust, l)
			}
		}
	}()
}

func (s *Server) customerOfIP(ip string) string {
	snmps.mu.Lock()
	var agentID string
	for _, d := range snmps.devs {
		if d.Host == ip {
			agentID = d.AgentID
			break
		}
	}
	snmps.mu.Unlock()
	s.mu.RLock()
	if agentID != "" {
		if a, ok := s.agents[agentID]; ok {
			c := s.customerOfAgent(a)
			s.mu.RUnlock()
			return c
		}
	}
	for _, t := range s.targets {
		if t.Host == ip {
			if a, ok := s.agents[t.AgentID]; ok {
				c := s.customerOfAgent(a)
				s.mu.RUnlock()
				return c
			}
		}
	}
	s.mu.RUnlock()
	integs.Lock()
	defer integs.Unlock()
	for id, st := range integs.st {
		for _, d := range st.devices {
			if d.IP == ip {
				if in, ok := integs.list[id]; ok {
					return in.CustomerID
				}
			}
		}
	}
	return ""
}

// ---------- API ----------

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	q := r.URL.Query()
	cust := q.Get("customer")
	if u.Role != "admin" {
		cust = u.CustomerID
	}
	where := []string{"1=1"}
	var args []any
	if cust != "" || q.Has("customer") {
		where = append(where, "customer_id=?")
		args = append(args, cust)
	}
	if v, err := strconv.Atoi(q.Get("sev")); err == nil && v >= 0 && v < 7 {
		where = append(where, "sev<=?")
		args = append(args, v)
	}
	if k := q.Get("kind"); k == "syslog" || k == "trap" {
		where = append(where, "kind=?")
		args = append(args, k)
	}
	if h := strings.TrimSpace(q.Get("host")); h != "" {
		where = append(where, "(host=? OR src=?)")
		args = append(args, h, h)
	}
	if t := strings.TrimSpace(q.Get("q")); t != "" {
		where = append(where, "(text LIKE ? OR app LIKE ? OR host LIKE ?)")
		like := "%" + strings.ReplaceAll(strings.ReplaceAll(t, "%", ""), "_", "") + "%"
		args = append(args, like, like, like)
	}
	if b, err := strconv.ParseInt(q.Get("before"), 10, 64); err == nil && b > 0 {
		where = append(where, "id<?")
		args = append(args, b)
	}
	limit := 200
	if l, err := strconv.Atoi(q.Get("limit")); err == nil && l > 0 && l <= 1000 {
		limit = l
	}
	rows, err := s.db.Query(`SELECT id,customer_id,agent_id,t,src,kind,sev,host,app,text FROM logs WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT `+strconv.Itoa(limit), args...)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []LogRow{}
	for rows.Next() {
		var l LogRow
		if rows.Scan(&l.ID, &l.CustomerID, &l.AgentID, &l.T, &l.Src, &l.Kind, &l.Sev, &l.Host, &l.App, &l.Text) == nil {
			out = append(out, l)
		}
	}
	writeJSON(w, out)
}

// AgentOpts är agentens inställningar för Wi-Fi-avsökning och loggmottagning.
type AgentOpts struct {
	WiFiScanMin   int      `json:"wifiScanMin"`
	Syslog        bool     `json:"syslog"`
	Traps         bool     `json:"traps"`
	TrapCommunity string   `json:"trapCommunity"`
	LogRx         string   `json:"logRx,omitempty"`
	Addrs         []string `json:"addrs,omitempty"` // agentens adresser i nätet
}

func (s *Server) agentOpts(agentID string) AgentOpts {
	var o AgentOpts
	var sl, tp int
	_ = s.db.QueryRow(`SELECT wifi_scan_min, syslog, traps, trap_community FROM agent_opts WHERE agent_id=?`, agentID).Scan(&o.WiFiScanMin, &sl, &tp, &o.TrapCommunity)
	o.Syslog, o.Traps = sl == 1, tp == 1
	logState.Lock()
	o.LogRx = logState.rx[agentID]
	logState.Unlock()
	scans.mu.Lock()
	for _, n := range scans.nets[agentID] {
		o.Addrs = append(o.Addrs, n.Addr)
	}
	scans.mu.Unlock()
	return o
}

func (s *Server) handleAgentOpts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.agentVisible(r, id) {
		httpErr(w, 404, "the agent does not exist")
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, s.agentOpts(id))
		return
	}
	var in AgentOpts
	if !readJSON(w, r, &in) {
		return
	}
	if err := s.agentManaged(id); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	in.TrapCommunity = strings.TrimSpace(in.TrapCommunity)
	if _, err := s.db.Exec(`INSERT INTO agent_opts(agent_id,syslog,traps,trap_community) VALUES(?,?,?,?)
		ON CONFLICT(agent_id) DO UPDATE SET syslog=excluded.syslog, traps=excluded.traps, trap_community=excluded.trap_community`, id, b2i(in.Syslog), b2i(in.Traps), in.TrapCommunity); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.bumpConfig(id)
	s.audit(r, "agent", id, i18n.T("changed syslog and trap reception"))
	writeJSON(w, s.agentOpts(id))
}
