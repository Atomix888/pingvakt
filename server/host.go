package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"pingvakt/core/alert"
	"pingvakt/core/hostinfo"
)

// ServerHostID är det ID som serverns egna värden sparas under.
const ServerHostID = "_server"

const hostSchema = `
CREATE TABLE IF NOT EXISTS host_metrics (
  agent_id TEXT NOT NULL, t INTEGER NOT NULL, cpu REAL NOT NULL, cpu_max REAL NOT NULL,
  mem_used INTEGER NOT NULL, mem_total INTEGER NOT NULL, disks TEXT NOT NULL,
  PRIMARY KEY (agent_id, t)
) WITHOUT ROWID;
`

type hostPoint struct {
	t    int64
	cpu  float64
	mem  float64
	disk float64
}

// hostTrack håller senaste värden, minutackumulator och kort historik för larm.
type hostTrack struct {
	latest *hostinfo.Metrics
	minute int64
	n      int
	cpuSum float64
	cpuMax float64
	last   hostinfo.Metrics
	recent []hostPoint
}

type hostState struct {
	mu     sync.Mutex
	tracks map[string]*hostTrack
	alerts map[string]int64 // regel|agent -> sedan
	self   *hostinfo.Collector
}

func newHostState() *hostState {
	return &hostState{tracks: map[string]*hostTrack{}, alerts: map[string]int64{}, self: hostinfo.New()}
}

// ingestHost tar emot CPU/minne/disk från en agent (eller servern själv).
func (s *Server) ingestHost(agentID string, m hostinfo.Metrics) {
	m.T = now()
	minute := m.T - m.T%60000
	var flush *hostRow
	s.host.mu.Lock()
	tr := s.host.tracks[agentID]
	if tr == nil {
		tr = &hostTrack{}
		s.host.tracks[agentID] = tr
	}
	if tr.n > 0 && tr.minute != minute {
		r := tr.row(agentID)
		flush = &r
		tr.n, tr.cpuSum, tr.cpuMax = 0, 0, 0
	}
	tr.minute = minute
	tr.n++
	tr.cpuSum += m.CPU
	if m.CPU > tr.cpuMax {
		tr.cpuMax = m.CPU
	}
	tr.last = m
	cp := m
	tr.latest = &cp
	tr.recent = append(tr.recent, hostPoint{m.T, m.CPU, m.MemPct(), m.MaxDiskPct()})
	cut := m.T - 25*3600*1000
	k := 0
	for k < len(tr.recent) && tr.recent[k].t < cut {
		k++
	}
	if k > 0 {
		tr.recent = append(tr.recent[:0:0], tr.recent[k:]...)
	}
	s.host.mu.Unlock()
	if flush != nil {
		s.saveHostRow(*flush)
	}
	s.checkHostAlerts(agentID)
}

type hostRow struct {
	agent    string
	t        int64
	cpu, max float64
	memUsed  uint64
	memTotal uint64
	disks    []hostinfo.Disk
}

func (tr *hostTrack) row(agent string) hostRow {
	return hostRow{agent: agent, t: tr.minute, cpu: tr.cpuSum / float64(tr.n), max: tr.cpuMax,
		memUsed: tr.last.MemUsed, memTotal: tr.last.MemTotal, disks: tr.last.Disks}
}

func (s *Server) saveHostRow(r hostRow) {
	d, _ := json.Marshal(r.disks)
	if _, err := s.db.Exec(`INSERT OR REPLACE INTO host_metrics(agent_id,t,cpu,cpu_max,mem_used,mem_total,disks) VALUES(?,?,?,?,?,?,?)`,
		r.agent, r.t, round1(r.cpu), round1(r.max), int64(r.memUsed), int64(r.memTotal), string(d)); err != nil {
		log.Printf("spara systemvärden: %v", err)
	}
}

// flushHost sparar minuter som avslutats utan att nya värden kommit (t.ex. agent offline).
func (s *Server) flushHost(force bool) {
	cur := now() - now()%60000
	var rows []hostRow
	s.host.mu.Lock()
	for id, tr := range s.host.tracks {
		if tr.n > 0 && (force || tr.minute < cur) {
			rows = append(rows, tr.row(id))
			tr.n, tr.cpuSum, tr.cpuMax = 0, 0, 0
		}
	}
	s.host.mu.Unlock()
	for _, r := range rows {
		s.saveHostRow(r)
	}
}

func (s *Server) hostLatest(agentID string) *hostinfo.Metrics {
	s.host.mu.Lock()
	defer s.host.mu.Unlock()
	if tr := s.host.tracks[agentID]; tr != nil && tr.latest != nil {
		cp := *tr.latest
		return &cp
	}
	return nil
}

// loadHostLatest läser senaste kända värden vid start.
func (s *Server) loadHostLatest() {
	rows, err := s.db.Query(`SELECT h.agent_id, h.t, h.cpu, h.mem_used, h.mem_total, h.disks FROM host_metrics h
		JOIN (SELECT agent_id, max(t) AS mt FROM host_metrics GROUP BY agent_id) x ON x.agent_id=h.agent_id AND x.mt=h.t`)
	if err != nil {
		return
	}
	defer rows.Close()
	s.host.mu.Lock()
	defer s.host.mu.Unlock()
	for rows.Next() {
		var id, disks string
		var m hostinfo.Metrics
		var mu, mt int64
		if rows.Scan(&id, &m.T, &m.CPU, &mu, &mt, &disks) != nil {
			continue
		}
		m.MemUsed, m.MemTotal = uint64(mu), uint64(mt)
		_ = json.Unmarshal([]byte(disks), &m.Disks)
		s.host.tracks[id] = &hostTrack{latest: &m}
	}
}

// selfLoop mäter serverns egen dator var tionde sekund.
func (s *Server) selfLoop(ctx context.Context) {
	tk := time.NewTicker(10 * time.Second)
	defer tk.Stop()
	for {
		m := s.host.self.Collect()
		s.ingestHost(ServerHostID, m)
		select {
		case <-ctx.Done():
			s.flushHost(true)
			return
		case <-tk.C:
		}
	}
}

// ---------- larm ----------

func (s *Server) checkHostAlerts(agentID string) {
	s.mu.RLock()
	cust := ""
	name := "PingVakt-servern"
	hostname := ""
	if agentID != ServerHostID {
		a, ok := s.agents[agentID]
		if !ok {
			s.mu.RUnlock()
			return
		}
		cust = s.customerOfAgent(a)
		site := s.sites[a.SiteID]
		name = s.customers[site.CustomerID].Name + " · " + site.Name + " · " + a.Name
		hostname = a.Hostname
	}
	var rules []Rule
	for _, r := range s.rules {
		switch r.Condition {
		case alert.CondCPU, alert.CondMem, alert.CondDisk:
			if r.CustomerID == "" || r.CustomerID == cust {
				rules = append(rules, r)
			}
		}
	}
	s.mu.RUnlock()
	if len(rules) == 0 {
		return
	}
	s.host.mu.Lock()
	tr := s.host.tracks[agentID]
	var recent []hostPoint
	var last hostinfo.Metrics
	if tr != nil {
		recent = append(recent, tr.recent...)
		last = tr.last
	}
	s.host.mu.Unlock()
	if len(recent) == 0 {
		return
	}
	t := now()
	for _, r := range rules {
		met, detail := evalHost(r.Alert, recent, last, t)
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
				detail = fmt.Sprintf("%s (larmet varade %s)", detail, time.Duration(t-since)*time.Millisecond/time.Second*time.Second)
			}
		}
		s.host.mu.Unlock()
		if kind == "" {
			continue
		}
		ev := alert.Event{Kind: kind, AlertID: r.ID, AlertName: r.Name, TargetID: agentID, TargetName: name, Host: hostname,
			Time: t, Detail: detail, Popup: r.Popup, Sound: r.Sound && kind == "fire", SoundName: r.SoundName}
		s.eng.Fire(r.Alert, ev)
	}
}

// evalHost avgör om en regel för CPU/minne/disk är uppfylld.
func evalHost(r alert.Alert, recent []hostPoint, last hostinfo.Metrics, t int64) (bool, string) {
	switch r.Condition {
	case alert.CondDisk:
		worst, pct := "", 0.0
		for _, d := range last.Disks {
			if p := d.Pct(); p > pct {
				worst, pct = d.Mount, p
			}
		}
		return pct > r.Threshold, fmt.Sprintf("Disk %s är %.0f %% full (%s ledigt)", worst, pct, gb(diskFree(last, worst)))
	case alert.CondCPU, alert.CondMem:
		window := int64(r.Samples) * 60000
		from := t - window
		var sum float64
		n, over := 0, true
		first := int64(0)
		for _, p := range recent {
			if p.t < from {
				continue
			}
			if first == 0 {
				first = p.t
			}
			v := p.cpu
			if r.Condition == alert.CondMem {
				v = p.mem
			}
			sum += v
			n++
			if v <= r.Threshold {
				over = false
			}
		}
		if n == 0 {
			return false, ""
		}
		what := "CPU"
		if r.Condition == alert.CondMem {
			what = "Minne"
		}
		avg := sum / float64(n)
		// kräver att hela tidsfönstret täcks av mätningar (minst 80 %)
		covered := t-first >= window*8/10 || r.Samples <= 1
		return over && covered, fmt.Sprintf("%s %.0f %% i snitt de senaste %d min", what, avg, r.Samples)
	}
	return false, ""
}

func diskFree(m hostinfo.Metrics, mount string) uint64 {
	for _, d := range m.Disks {
		if d.Mount == mount && d.Total >= d.Used {
			return d.Total - d.Used
		}
	}
	return 0
}

func gb(b uint64) string {
	v := float64(b) / (1 << 30)
	if v >= 100 {
		return fmt.Sprintf("%.0f GB", v)
	}
	return fmt.Sprintf("%.1f GB", v)
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

// ---------- historik ----------

// HostHistory är svaret för grafer över CPU, minne och disk.
type HostHistory struct {
	Bucket   int64            `json:"bucket"`
	T        []int64          `json:"t"`
	CPU      []*float64       `json:"cpu"`
	CPUMax   []*float64       `json:"cpuMax"`
	Mem      []*float64       `json:"mem"`
	MemTotal uint64           `json:"memTotal"`
	Disks    []HostDiskSeries `json:"disks"`
}

// HostDiskSeries är fyllnadsgraden för en volym över tid.
type HostDiskSeries struct {
	Mount string     `json:"mount"`
	Total uint64     `json:"total"`
	Pct   []*float64 `json:"pct"`
}

func (s *Server) hostHistory(agentID string, from, to int64) (HostHistory, error) {
	span := to - from
	bucket := int64(60000)
	switch {
	case span > 14*86400000:
		bucket = 3600000
	case span > 2*86400000:
		bucket = 900000
	}
	rows, err := s.db.Query(`SELECT t,cpu,cpu_max,mem_used,mem_total,disks FROM host_metrics WHERE agent_id=? AND t>? AND t<=? ORDER BY t`, agentID, from-bucket, to)
	if err != nil {
		return HostHistory{}, err
	}
	defer rows.Close()
	type acc struct {
		n             int
		cpu, max, mem float64
		disks         map[string]float64
		diskTotal     map[string]uint64
	}
	var order []int64
	buckets := map[int64]*acc{}
	mounts := map[string]uint64{}
	var memTotal uint64
	for rows.Next() {
		var t int64
		var cpu, cmax float64
		var mu, mt int64
		var dj string
		if err := rows.Scan(&t, &cpu, &cmax, &mu, &mt, &dj); err != nil {
			return HostHistory{}, err
		}
		b := t - t%bucket
		a := buckets[b]
		if a == nil {
			a = &acc{disks: map[string]float64{}, diskTotal: map[string]uint64{}}
			buckets[b] = a
			order = append(order, b)
		}
		a.n++
		a.cpu += cpu
		if cmax > a.max {
			a.max = cmax
		}
		if mt > 0 {
			a.mem += float64(mu) * 100 / float64(mt)
			memTotal = uint64(mt)
		}
		var ds []hostinfo.Disk
		_ = json.Unmarshal([]byte(dj), &ds)
		for _, d := range ds {
			if p := d.Pct(); p > a.disks[d.Mount] {
				a.disks[d.Mount] = p
			}
			mounts[d.Mount] = d.Total
		}
	}
	h := HostHistory{Bucket: bucket, T: []int64{}, CPU: []*float64{}, CPUMax: []*float64{}, Mem: []*float64{}, MemTotal: memTotal, Disks: []HostDiskSeries{}}
	names := make([]string, 0, len(mounts))
	for m := range mounts {
		names = append(names, m)
	}
	sort.Strings(names)
	for _, m := range names {
		h.Disks = append(h.Disks, HostDiskSeries{Mount: m, Total: mounts[m], Pct: []*float64{}})
	}
	pf := func(v float64) *float64 { v = round1(v); return &v }
	for _, b := range order {
		a := buckets[b]
		h.T = append(h.T, b)
		h.CPU = append(h.CPU, pf(a.cpu/float64(a.n)))
		h.CPUMax = append(h.CPUMax, pf(a.max))
		h.Mem = append(h.Mem, pf(a.mem/float64(a.n)))
		for i := range h.Disks {
			if v, ok := a.disks[h.Disks[i].Mount]; ok {
				h.Disks[i].Pct = append(h.Disks[i].Pct, pf(v))
			} else {
				h.Disks[i].Pct = append(h.Disks[i].Pct, nil)
			}
		}
	}
	return h, rows.Err()
}

func (s *Server) handleAgentMetrics(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u := userFrom(r)
	if id == ServerHostID {
		if u.Role != "admin" {
			httpErr(w, 403, "kräver administratör")
			return
		}
	} else {
		s.mu.RLock()
		a, ok := s.agents[id]
		vis := ok && canSee(u, s.customerOfAgent(a))
		s.mu.RUnlock()
		if !vis {
			httpErr(w, 404, "agenten finns inte")
			return
		}
	}
	to, _ := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	if to <= 0 {
		to = now()
	}
	if from <= 0 || from >= to {
		from = to - 86400000
	}
	s.flushHost(false)
	h, err := s.hostHistory(id, from, to)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"history": h, "latest": s.hostLatest(id)})
}

func (s *Server) pruneHost(aggDays int) {
	_, _ = s.db.Exec(`DELETE FROM host_metrics WHERE t < ?`, now()-int64(aggDays)*86400000)
	// värden för borttagna agenter
	_, _ = s.db.Exec(`DELETE FROM host_metrics WHERE agent_id <> ? AND agent_id NOT IN (SELECT id FROM agents)`, ServerHostID)
}

var _ = strings.TrimSpace
