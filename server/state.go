package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"pingvakt/core/agentproto"
	"pingvakt/core/alert"
	"pingvakt/core/hostinfo"
	"pingvakt/core/monitor"
)

// Server håller hela tillståndet i minnet (kunder, agenter, mål, statistik)
// och skriver ändringar och mätdata till databasen.
type Server struct {
	db        *DB
	version   string
	agentsDir string

	mu        sync.RWMutex
	settings  ServerSettings
	customers map[string]Customer
	sites     map[string]Site
	agents    map[string]*Agent
	tokens    map[string]string // token-hash -> agent-id
	targets   map[string]*Target
	trackers  map[string]*monitor.Tracker
	pending   map[string][]monitor.Sample
	rules     map[string]Rule
	offline   map[string]bool

	eng    *alert.Engine
	mailer *alert.Mailer
	host   *hostState

	subMu sync.Mutex
	subs  map[*subscriber]struct{}

	limiter *loginLimiter
}

// NewServer läser in allt från databasen.
func NewServer(db *DB, version, agentsDir string) (*Server, error) {
	s := &Server{db: db, version: version, agentsDir: agentsDir,
		customers: map[string]Customer{}, sites: map[string]Site{}, agents: map[string]*Agent{},
		tokens: map[string]string{}, targets: map[string]*Target{}, trackers: map[string]*monitor.Tracker{},
		pending: map[string][]monitor.Sample{}, rules: map[string]Rule{}, offline: map[string]bool{},
		subs: map[*subscriber]struct{}{}, limiter: newLoginLimiter(), host: newHostState()}
	s.settings = db.LoadSettings()
	s.mailer = alert.NewMailer(s.settings.SMTP)
	s.mailer.OnError = func(err error) { log.Printf("e-post: %v", err) }
	s.eng = alert.NewEngineFor(engineSource{s}, s.mailer, "", s.onAlertEvent)
	if err := s.reload(); err != nil {
		return nil, err
	}
	s.loadHostLatest()
	// värm upp statistiken med de senaste mätpunkterna
	for id, t := range s.targets {
		tr := monitor.NewTracker(s.settings.Monitor.HistorySize)
		if recent, err := db.RecentSamples(id, 600); err == nil {
			for _, x := range recent {
				tr.Add(x, "", "")
			}
		}
		tr.Recompute(s.settings.Monitor, t.WarnMs, t.Paused)
		s.trackers[id] = tr
	}
	// starttillstånd för offline – utan att larma vid omstart
	for id, a := range s.agents {
		s.offline[id] = !s.agentOnline(a)
	}
	return s, nil
}

func (s *Server) reload() error {
	cs, err := s.db.Customers()
	if err != nil {
		return err
	}
	ss, err := s.db.Sites()
	if err != nil {
		return err
	}
	as, err := s.db.Agents()
	if err != nil {
		return err
	}
	ts, err := s.db.Targets()
	if err != nil {
		return err
	}
	rs, err := s.db.Rules()
	if err != nil {
		return err
	}
	rows, err := s.db.Query(`SELECT id, token_hash FROM agents`)
	if err != nil {
		return err
	}
	tokens := map[string]string{}
	for rows.Next() {
		var id, h string
		if rows.Scan(&id, &h) == nil {
			tokens[h] = id
		}
	}
	rows.Close()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.customers = map[string]Customer{}
	for _, c := range cs {
		s.customers[c.ID] = c
	}
	s.sites = map[string]Site{}
	for _, x := range ss {
		s.sites[x.ID] = x
	}
	old := s.agents
	s.agents = map[string]*Agent{}
	for i := range as {
		a := as[i]
		if o, ok := old[a.ID]; ok && o.LastSeen > a.LastSeen {
			a.LastSeen = o.LastSeen
		}
		s.agents[a.ID] = &a
	}
	s.tokens = tokens
	s.targets = map[string]*Target{}
	for i := range ts {
		t := ts[i]
		s.targets[t.ID] = &t
		if _, ok := s.trackers[t.ID]; !ok {
			tr := monitor.NewTracker(s.settings.Monitor.HistorySize)
			s.trackers[t.ID] = tr
		}
		s.trackers[t.ID].Recompute(s.settings.Monitor, t.WarnMs, t.Paused)
	}
	for id := range s.trackers {
		if _, ok := s.targets[id]; !ok {
			delete(s.trackers, id)
		}
	}
	s.rules = map[string]Rule{}
	list := make([]alert.Alert, 0, len(rs))
	for _, r := range rs {
		s.rules[r.ID] = r
		list = append(list, r.Alert)
	}
	s.eng.SetAlerts(list)
	return nil
}

// ---------- hjälpare ----------

func newID() string { return monitor.NewID() }

func newToken() (string, string) {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	tok := "pva_" + base64.RawURLEncoding.EncodeToString(b)
	return tok, hashToken(tok)
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func (s *Server) agentOnline(a *Agent) bool {
	limit := int64(s.settings.ReportSec) * 3000
	if limit < 30000 {
		limit = 30000
	}
	return a.LastSeen > 0 && now()-a.LastSeen < limit
}

// customerOf returnerar kunden för ett mål (anroparen håller mu).
func (s *Server) customerOfTarget(t *Target) string {
	if a, ok := s.agents[t.AgentID]; ok {
		return s.sites[a.SiteID].CustomerID
	}
	return ""
}

func (s *Server) customerOfAgent(a *Agent) string { return s.sites[a.SiteID].CustomerID }

// displayName ger "Kund · Site · Mål" för larm.
func (s *Server) displayName(t *Target) string {
	n := t.Name
	if n == "" {
		n = t.Host
	}
	if a, ok := s.agents[t.AgentID]; ok {
		site := s.sites[a.SiteID]
		c := s.customers[site.CustomerID]
		return c.Name + " · " + site.Name + " · " + n
	}
	return n
}

// engineSource gör att larmmotorn kan läsa serverns mål och mätpunkter.
type engineSource struct{ s *Server }

func (e engineSource) Target(id string) (monitor.Target, bool) {
	e.s.mu.RLock()
	defer e.s.mu.RUnlock()
	t, ok := e.s.targets[id]
	if !ok {
		return monitor.Target{}, false
	}
	cp := t.Target
	cp.Name = e.s.displayName(t)
	return cp, true
}

func (e engineSource) Recent(id string, n int) []monitor.Sample {
	e.s.mu.RLock()
	defer e.s.mu.RUnlock()
	if tr, ok := e.s.trackers[id]; ok {
		return tr.Recent(n)
	}
	return nil
}

func (s *Server) onAlertEvent(ev alert.Event) {
	s.mu.RLock()
	cust := ""
	if t, ok := s.targets[ev.TargetID]; ok {
		cust = s.customerOfTarget(t)
	} else if a, ok := s.agents[ev.TargetID]; ok {
		cust = s.customerOfAgent(a)
	}
	s.mu.RUnlock()
	if ev.Kind != "test" {
		s.db.AddEvent(cust, ev)
	}
	b, _ := json.Marshal(map[string]any{"type": "alert", "event": ev, "customerId": cust})
	s.broadcastRaw(cust, b)
}

// ---------- inkommande rapporter ----------

// Ingest tar emot en rapport från en agent och returnerar svaret.
func (s *Server) Ingest(ctx context.Context, agentID, ip string, rep agentproto.Report) (agentproto.Response, error) {
	s.mu.Lock()
	a, ok := s.agents[agentID]
	if !ok {
		s.mu.Unlock()
		return agentproto.Response{}, errNotFound
	}
	a.LastSeen = now()
	a.IP = ip
	a.Hostname, a.OS, a.Version = rep.Agent.Hostname, rep.Agent.OS, rep.Agent.Version
	kind := rep.Agent.Kind
	if kind != agentproto.KindDesktop {
		kind = agentproto.KindAgent
	}
	a.Kind = kind
	agentCopy := *a
	site := s.sites[a.SiteID]
	cust := s.customers[site.CustomerID]
	s.mu.Unlock()

	_, _ = s.db.Exec(`UPDATE agents SET last_seen=?, ip=?, hostname=?, os=?, version=?, kind=? WHERE id=?`,
		agentCopy.LastSeen, ip, agentCopy.Hostname, agentCopy.OS, agentCopy.Version, kind, agentID)

	resp := agentproto.Response{AgentID: agentID, AgentName: agentCopy.Name, Customer: cust.Name, Site: site.Name, ServerTime: now()}

	// skrivbordsappen rapporterar sina egna mål
	if kind == agentproto.KindDesktop {
		if rep.LocalTargets != nil {
			if err := s.syncLocalTargets(agentID, rep.LocalTargets, rep.LocalTargetsHash); err != nil {
				return resp, err
			}
		} else if rep.LocalTargetsHash != agentCopy.LocalHash {
			resp.NeedLocalTargets = true
		}
	}

	// mätpunkter
	var rows []storedSample
	touched := map[string]monitor.Update{}
	s.mu.Lock()
	settings := s.settings.Monitor
	for _, x := range rep.Samples {
		id := x.ID
		if kind == agentproto.KindDesktop {
			id = agentID + "." + x.ID
		}
		t, ok := s.targets[id]
		if !ok || t.AgentID != agentID {
			continue
		}
		tr := s.trackers[id]
		smp := monitor.Sample{T: x.T, RTT: x.R}
		tr.Add(smp, x.E, x.A)
		s.pending[id] = append(s.pending[id], smp)
		if len(s.pending[id]) > 2000 {
			s.pending[id] = s.pending[id][len(s.pending[id])-2000:]
		}
		rows = append(rows, storedSample{id: id, t: x.T, rtt: float64(x.R)})
		touched[id] = monitor.Update{TargetID: id, Sample: smp}
	}
	for id := range touched {
		t := s.targets[id]
		tr := s.trackers[id]
		tr.Recompute(settings, t.WarnMs, t.Paused)
		u := touched[id]
		u.Stats = tr.Stats()
		touched[id] = u
	}
	s.mu.Unlock()

	if err := s.db.InsertSamples(ctx, rows); err != nil {
		log.Printf("spara mätdata: %v", err)
		return resp, err
	}
	for _, u := range touched {
		s.eng.Handle(u)
	}
	if rep.Host != nil {
		s.ingestHost(agentID, *rep.Host)
	}

	// konfiguration till agenter som styrs från servern
	if kind == agentproto.KindAgent && rep.ConfigVersion != agentCopy.ConfigVersion {
		resp.ConfigVersion = agentCopy.ConfigVersion
		resp.Config = s.agentConfig(agentID)
	}
	return resp, nil
}

func (s *Server) agentConfig(agentID string) *agentproto.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var ts []Target
	for _, t := range s.targets {
		if t.AgentID == agentID && t.Source == "server" {
			ts = append(ts, *t)
		}
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].Sort < ts[j].Sort })
	m := s.settings.Monitor
	m.HistorySize = 600
	return &agentproto.Config{Targets: toProto(ts), Settings: m, ReportSec: s.settings.ReportSec}
}

func (s *Server) syncLocalTargets(agentID string, local []monitor.Target, hash string) error {
	s.mu.RLock()
	existing := map[string]*Target{}
	for id, t := range s.targets {
		if t.AgentID == agentID && t.Source == "agent" {
			existing[id] = t
		}
	}
	s.mu.RUnlock()
	keep := map[string]bool{}
	for i, lt := range local {
		lt.Normalize()
		id := agentID + "." + lt.ID
		keep[id] = true
		t := Target{Target: lt, AgentID: agentID, Source: "agent", Sort: i}
		t.ID = id
		if old, ok := existing[id]; ok {
			t.AlertIDs = old.AlertIDs // larm väljs på servern, inte i appen
		} else {
			t.AlertIDs = s.defaultAlertIDs(agentID)
		}
		if err := s.db.SaveTarget(t); err != nil {
			return err
		}
	}
	for id := range existing {
		if !keep[id] {
			if err := s.db.DeleteTarget(id); err != nil {
				return err
			}
		}
	}
	_, _ = s.db.Exec(`UPDATE agents SET local_hash=? WHERE id=?`, hash, agentID)
	s.mu.Lock()
	if a, ok := s.agents[agentID]; ok {
		a.LocalHash = hash
	}
	s.mu.Unlock()
	return s.reload()
}

// defaultAlertIDs: nya mål får kundens (eller globala) "nere"-larm om ett sådant finns.
func (s *Server) defaultAlertIDs(agentID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cust := ""
	if a, ok := s.agents[agentID]; ok {
		cust = s.customerOfAgent(a)
	}
	var out []string
	for _, r := range s.rules {
		if r.Condition == alert.CondDown && (r.CustomerID == "" || r.CustomerID == cust) {
			out = append(out, r.ID)
		}
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

// ---------- bakgrundsjobb ----------

// Run startar sändning till webbläsare, offline-kontroll och gallring.
func (s *Server) Run(ctx context.Context) {
	go s.broadcastLoop(ctx)
	go s.selfLoop(ctx)
	go func() {
		tk := time.NewTicker(10 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				s.checkOffline()
				s.flushHost(false)
			}
		}
	}()
	go func() {
		for {
			s.mu.RLock()
			raw, agg := s.settings.RawDays, s.settings.AggDays
			s.mu.RUnlock()
			s.pruneHost(agg)
			if n, err := s.db.Prune(raw, agg); err != nil {
				log.Printf("gallring: %v", err)
			} else if n > 0 {
				log.Printf("gallring: tog bort %d rader", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Hour):
			}
		}
	}()
}

func (s *Server) checkOffline() {
	type change struct {
		a       Agent
		offline bool
		cust    string
		since   int64
	}
	var changes []change
	s.mu.Lock()
	for id, a := range s.agents {
		off := !s.agentOnline(a)
		if a.LastSeen == 0 {
			off = false // aldrig ansluten – inte "offline"
		}
		if s.offline[id] != off {
			s.offline[id] = off
			changes = append(changes, change{*a, off, s.customerOfAgent(a), a.LastSeen})
		}
	}
	rules := make([]Rule, 0)
	for _, r := range s.rules {
		if r.Condition == alert.CondOffline {
			rules = append(rules, r)
		}
	}
	names := map[string]string{}
	for _, c := range changes {
		site := s.sites[c.a.SiteID]
		names[c.a.ID] = s.customers[site.CustomerID].Name + " · " + site.Name + " · " + c.a.Name
	}
	s.mu.Unlock()

	for _, c := range changes {
		for _, r := range rules {
			if r.CustomerID != "" && r.CustomerID != c.cust {
				continue
			}
			kind, detail := "fire", "Agenten har inte rapporterat sedan "+time.UnixMilli(c.since).Format("2006-01-02 15:04:05")
			if !c.offline {
				if !r.OnClear {
					continue
				}
				kind, detail = "clear", "Agenten rapporterar igen"
			}
			ev := alert.Event{Kind: kind, AlertID: r.ID, AlertName: r.Name, TargetID: c.a.ID, TargetName: names[c.a.ID],
				Host: c.a.Hostname, Time: now(), Detail: detail, Popup: r.Popup, Sound: r.Sound && kind == "fire", SoundName: r.SoundName}
			s.eng.Fire(r.Alert, ev)
		}
	}
}

// ---------- liveflöde till webbläsare ----------

type subscriber struct {
	ch        chan []byte
	customers map[string]bool // nil = alla
}

func (s *Server) subscribe(customers map[string]bool) *subscriber {
	sub := &subscriber{ch: make(chan []byte, 16), customers: customers}
	s.subMu.Lock()
	s.subs[sub] = struct{}{}
	s.subMu.Unlock()
	return sub
}

func (s *Server) unsubscribe(sub *subscriber) {
	s.subMu.Lock()
	delete(s.subs, sub)
	s.subMu.Unlock()
}

func (s *Server) broadcastRaw(customer string, b []byte) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for sub := range s.subs {
		if sub.customers != nil && !sub.customers[customer] {
			continue
		}
		select {
		case sub.ch <- b:
		default:
		}
	}
}

// LiveStats är statistik med hänsyn till om agenten är offline.
type liveAgent struct {
	LastSeen int64             `json:"lastSeen"`
	Online   bool              `json:"online"`
	Host     *hostinfo.Metrics `json:"host,omitempty"`
}

func (s *Server) broadcastLoop(ctx context.Context) {
	tk := time.NewTicker(2 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
		s.subMu.Lock()
		subs := make([]*subscriber, 0, len(s.subs))
		for sub := range s.subs {
			subs = append(subs, sub)
		}
		s.subMu.Unlock()

		s.mu.Lock()
		pending := s.pending
		s.pending = map[string][]monitor.Sample{}
		s.mu.Unlock()
		if len(subs) == 0 {
			continue
		}
		snap := s.snapshot(nil)
		active := s.activeAlerts()
		self := s.hostLatest(ServerHostID)
		for _, sub := range subs {
			msg := map[string]any{"type": "tick"}
			stats := map[string]monitor.Stats{}
			samples := map[string][]monitor.Sample{}
			for id, st := range snap.Stats {
				if sub.customers == nil || sub.customers[snap.targetCustomer[id]] {
					stats[id] = st
					if p := pending[id]; len(p) > 0 {
						samples[id] = p
					}
				}
			}
			agents := map[string]liveAgent{}
			for id, la := range snap.Agents {
				if sub.customers == nil || sub.customers[snap.agentCustomer[id]] {
					agents[id] = la
				}
			}
			act := map[string]int64{}
			for k, v := range active {
				if i := strings.IndexByte(k, '|'); i >= 0 {
					id := k[i+1:]
					_, isT := stats[id]
					_, isA := agents[id]
					if isT || isA {
						act[k] = v
					}
				}
			}
			msg["stats"], msg["samples"], msg["agents"], msg["active"] = stats, samples, agents, act
			if sub.customers == nil {
				msg["server"] = self
			}
			b, _ := json.Marshal(msg)
			select {
			case sub.ch <- b:
			default:
			}
		}
	}
}

type snapshot struct {
	Stats          map[string]monitor.Stats
	Agents         map[string]liveAgent
	targetCustomer map[string]string
	agentCustomer  map[string]string
}

// snapshot returnerar statistik för alla mål (filtrerat på kunder om angivet).
func (s *Server) snapshot(customers map[string]bool) snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := snapshot{Stats: map[string]monitor.Stats{}, Agents: map[string]liveAgent{},
		targetCustomer: map[string]string{}, agentCustomer: map[string]string{}}
	for id, a := range s.agents {
		c := s.customerOfAgent(a)
		if customers != nil && !customers[c] {
			continue
		}
		out.agentCustomer[id] = c
		out.Agents[id] = liveAgent{LastSeen: a.LastSeen, Online: s.agentOnline(a), Host: s.hostLatest(id)}
	}
	for id, t := range s.targets {
		c := s.customerOfTarget(t)
		if customers != nil && !customers[c] {
			continue
		}
		st := s.trackers[id].Stats()
		if a, ok := s.agents[t.AgentID]; ok && !t.Paused && !s.agentOnline(a) {
			st.Status = monitor.StatusOffline
		}
		out.Stats[id] = st
		out.targetCustomer[id] = c
	}
	return out
}

// activeAlerts slår ihop aktiva larm för mål och för datorer (CPU/minne/disk).
func (s *Server) activeAlerts() map[string]int64 {
	out := s.eng.Active()
	s.host.mu.Lock()
	for k, v := range s.host.alerts {
		out[k] = v
	}
	s.host.mu.Unlock()
	return out
}
