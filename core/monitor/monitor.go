// Package monitor schemalägger mätningar mot en lista med mål, sparar historik
// i minnet och räknar fram statistik och status. Uppdateringar sänds till
// prenumeranter (gränssnitt, larmmotor och – i nästa etapp – uppladdning till server).
package monitor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"pingvakt/core/probe"
)

// Target är ett övervakat mål.
type Target struct {
	ID         string       `json:"id"`
	Host       string       `json:"host"`
	Name       string       `json:"name"`
	Kind       probe.Kind   `json:"kind"`
	Port       int          `json:"port,omitempty"`
	IntervalMs int          `json:"intervalMs"`
	TimeoutMs  int          `json:"timeoutMs"`
	Size       int          `json:"size"`
	IPMode     probe.IPMode `json:"ipMode"`
	Group      string       `json:"group,omitempty"`
	WarnMs     float64      `json:"warnMs,omitempty"` // 0 = använd globalt värde
	AlertIDs   []string     `json:"alertIds"`
	Paused     bool         `json:"paused"`
}

// Normalize fyller i standardvärden och rättar orimliga värden.
func (t *Target) Normalize() {
	t.Host = strings.TrimSpace(t.Host)
	t.Name = strings.TrimSpace(t.Name)
	if t.ID == "" {
		t.ID = NewID()
	}
	if t.Kind != probe.KindTCP {
		t.Kind = probe.KindICMP
	}
	if t.Kind == probe.KindTCP && (t.Port <= 0 || t.Port > 65535) {
		t.Port = 443
	}
	if t.Kind == probe.KindICMP {
		t.Port = 0
	}
	if t.IntervalMs < 200 {
		t.IntervalMs = 1000
	}
	if t.TimeoutMs < 100 {
		t.TimeoutMs = 2000
	}
	if t.Size <= 0 || t.Size > 65000 {
		t.Size = 32
	}
	switch t.IPMode {
	case probe.IPv4, probe.IPv6:
	default:
		t.IPMode = probe.IPAuto
	}
	if t.AlertIDs == nil {
		t.AlertIDs = []string{}
	}
}

// NewID skapar ett slumpmässigt ID.
func NewID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Sample är en mätpunkt. RTT i millisekunder, -1 betyder förlorat paket.
type Sample struct {
	T   int64   `json:"t"`
	RTT float32 `json:"r"`
}

// Lost anger om paketet förlorades.
func (s Sample) Lost() bool { return s.RTT < 0 }

// Status för ett mål.
const (
	StatusPending = "pending"
	StatusUp      = "up"
	StatusWarn    = "warn"
	StatusDown    = "down"
	StatusPaused  = "paused"
)

// Stats är aktuell statistik för ett mål, beräknad över statistikfönstret.
type Stats struct {
	Status     string  `json:"status"`
	Since      int64   `json:"since"` // när nuvarande status började (unix ms)
	Last       float64 `json:"last"`
	Avg        float64 `json:"avg"`
	Min        float64 `json:"min"`
	Max        float64 `json:"max"`
	Jitter     float64 `json:"jitter"`
	Loss       float64 `json:"loss"` // procent i fönstret
	Window     int     `json:"window"`
	Sent       int64   `json:"sent"`
	Lost       int64   `json:"lost"`
	ConsecLost int     `json:"consecLost"`
	Addr       string  `json:"addr"`
	Err        string  `json:"err,omitempty"`
}

// Settings är globala inställningar för mätningen.
type Settings struct {
	StatsWindow int     `json:"statsWindow"` // antal mätpunkter som statistiken räknas på
	WarnMs      float64 `json:"warnMs"`      // varning när snittlatens överstiger detta (0 = av)
	WarnLossPct float64 `json:"warnLossPct"` // varning vid förlust över detta i fönstret
	DownAfter   int     `json:"downAfter"`   // antal förlorade i rad innan "nere"
	HistorySize int     `json:"historySize"` // antal mätpunkter som sparas per mål
}

// DefaultSettings returnerar standardinställningarna.
func DefaultSettings() Settings {
	return Settings{StatsWindow: 60, WarnMs: 100, WarnLossPct: 2, DownAfter: 3, HistorySize: 21600}
}

func (s *Settings) normalize() {
	d := DefaultSettings()
	if s.StatsWindow < 5 {
		s.StatsWindow = d.StatsWindow
	}
	if s.DownAfter < 1 {
		s.DownAfter = d.DownAfter
	}
	if s.HistorySize < 300 {
		s.HistorySize = d.HistorySize
	}
	if s.WarnMs < 0 {
		s.WarnMs = 0
	}
	if s.WarnLossPct < 0 {
		s.WarnLossPct = 0
	}
}

// Update skickas till prenumeranter efter varje mätning.
type Update struct {
	TargetID string `json:"id"`
	Sample   Sample `json:"sample"`
	Stats    Stats  `json:"stats"`
}

type runner struct {
	mu     sync.Mutex
	target Target
	hist   *ring
	stats  Stats
	cancel context.CancelFunc
}

// Manager äger alla mål och deras mätloopar.
type Manager struct {
	mu       sync.RWMutex
	pinger   *probe.Pinger
	settings Settings
	runners  map[string]*runner
	order    []string
	subMu    sync.RWMutex
	subs     []func(Update)
	ctx      context.Context
	stop     context.CancelFunc
}

// NewManager skapar en Manager.
func NewManager(s Settings) *Manager {
	s.normalize()
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{pinger: probe.New(), settings: s, runners: map[string]*runner{}, ctx: ctx, stop: cancel}
}

// Close stoppar alla mätningar.
func (m *Manager) Close() {
	m.stop()
	m.pinger.Close()
}

// Subscribe registrerar en mottagare av uppdateringar.
func (m *Manager) Subscribe(fn func(Update)) {
	m.subMu.Lock()
	m.subs = append(m.subs, fn)
	m.subMu.Unlock()
}

func (m *Manager) publish(u Update) {
	m.subMu.RLock()
	subs := m.subs
	m.subMu.RUnlock()
	for _, fn := range subs {
		fn(u)
	}
}

// Settings returnerar nuvarande inställningar.
func (m *Manager) Settings() Settings {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.settings
}

// SetSettings byter inställningar och räknar om statistiken.
func (m *Manager) SetSettings(s Settings) {
	s.normalize()
	m.mu.Lock()
	m.settings = s
	rs := make([]*runner, 0, len(m.runners))
	for _, r := range m.runners {
		rs = append(rs, r)
	}
	m.mu.Unlock()
	for _, r := range rs {
		r.mu.Lock()
		r.hist.resize(s.HistorySize)
		r.recompute(s)
		r.mu.Unlock()
	}
}

// Targets returnerar målen i visningsordning.
func (m *Manager) Targets() []Target {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Target, 0, len(m.order))
	for _, id := range m.order {
		r := m.runners[id]
		r.mu.Lock()
		out = append(out, r.target)
		r.mu.Unlock()
	}
	return out
}

// Upsert lägger till eller ändrar ett mål. Ändrade mål startas om men behåller historiken
// så länge värd och mättyp är oförändrade.
func (m *Manager) Upsert(t Target) Target {
	t.Normalize()
	m.mu.Lock()
	r, exists := m.runners[t.ID]
	if !exists {
		r = &runner{target: t, hist: newRing(m.settings.HistorySize)}
		r.stats = Stats{Status: StatusPending, Since: nowMs(), Last: -1}
		m.runners[t.ID] = r
		m.order = append(m.order, t.ID)
	}
	settings := m.settings
	m.mu.Unlock()

	r.mu.Lock()
	if exists {
		old := r.target
		if old.Host != t.Host || old.Kind != t.Kind || old.Port != t.Port || old.IPMode != t.IPMode {
			r.hist = newRing(settings.HistorySize)
			r.stats = Stats{Status: StatusPending, Since: nowMs(), Last: -1}
		}
		r.target = t
		r.recompute(settings)
	}
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	if t.Paused {
		r.setStatus(StatusPaused)
	} else {
		if r.stats.Status == StatusPaused {
			r.setStatus(StatusPending)
		}
		ctx, cancel := context.WithCancel(m.ctx)
		r.cancel = cancel
		go m.loop(ctx, r)
	}
	r.mu.Unlock()
	return t
}

// Remove tar bort ett mål.
func (m *Manager) Remove(id string) {
	m.mu.Lock()
	r, ok := m.runners[id]
	if ok {
		delete(m.runners, id)
		for i, x := range m.order {
			if x == id {
				m.order = append(m.order[:i], m.order[i+1:]...)
				break
			}
		}
	}
	m.mu.Unlock()
	if ok {
		r.mu.Lock()
		if r.cancel != nil {
			r.cancel()
		}
		r.mu.Unlock()
	}
}

// Reorder sätter visningsordningen. Okända ID ignoreras, saknade läggs sist.
func (m *Manager) Reorder(ids []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if _, ok := m.runners[id]; ok && !seen[id] {
			out = append(out, id)
			seen[id] = true
		}
	}
	for _, id := range m.order {
		if !seen[id] {
			out = append(out, id)
		}
	}
	m.order = out
}

// SetPaused pausar eller startar ett mål.
func (m *Manager) SetPaused(id string, paused bool) {
	m.mu.RLock()
	r, ok := m.runners[id]
	m.mu.RUnlock()
	if !ok {
		return
	}
	r.mu.Lock()
	t := r.target
	r.mu.Unlock()
	t.Paused = paused
	m.Upsert(t)
}

// Snapshot returnerar aktuell statistik för alla mål.
func (m *Manager) Snapshot() map[string]Stats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]Stats, len(m.runners))
	for id, r := range m.runners {
		r.mu.Lock()
		out[id] = r.stats
		r.mu.Unlock()
	}
	return out
}

// History returnerar mätpunkter nyare än sinceMs (0 = allt).
func (m *Manager) History(id string, sinceMs int64) []Sample {
	m.mu.RLock()
	r, ok := m.runners[id]
	m.mu.RUnlock()
	if !ok {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hist.since(sinceMs)
}

// Recent returnerar de n senaste mätpunkterna.
func (m *Manager) Recent(id string, n int) []Sample {
	m.mu.RLock()
	r, ok := m.runners[id]
	m.mu.RUnlock()
	if !ok {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hist.last(n)
}

// Target returnerar ett mål.
func (m *Manager) Target(id string) (Target, bool) {
	m.mu.RLock()
	r, ok := m.runners[id]
	m.mu.RUnlock()
	if !ok {
		return Target{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.target, true
}

func (m *Manager) loop(ctx context.Context, r *runner) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		r.mu.Lock()
		t := r.target
		r.mu.Unlock()
		start := time.Now()
		res := m.pinger.Do(ctx, probe.Request{
			Host: t.Host, Kind: t.Kind, Port: t.Port, Size: t.Size, IPMode: t.IPMode,
			Timeout: time.Duration(t.TimeoutMs) * time.Millisecond,
		})
		if ctx.Err() != nil {
			return
		}
		s := Sample{T: res.Time.UnixMilli(), RTT: -1}
		if res.OK {
			s.RTT = float32(math.Round(res.RTT.Seconds()*1e6) / 1e3)
		}
		settings := m.Settings()
		r.mu.Lock()
		r.hist.push(s)
		r.stats.Sent++
		if s.Lost() {
			r.stats.Lost++
			r.stats.ConsecLost++
			r.stats.Err = res.Err
		} else {
			r.stats.ConsecLost = 0
			r.stats.Err = ""
		}
		if res.Addr != "" {
			r.stats.Addr = res.Addr
		}
		r.recompute(settings)
		u := Update{TargetID: t.ID, Sample: s, Stats: r.stats}
		r.mu.Unlock()
		m.publish(u)

		next := time.Duration(t.IntervalMs)*time.Millisecond - time.Since(start)
		if next < 0 {
			next = 0
		}
		timer.Reset(next)
	}
}

// recompute räknar om statistik och status. Anroparen håller r.mu.
func (r *runner) recompute(s Settings) {
	if r.target.Paused {
		r.setStatus(StatusPaused)
		return
	}
	win := r.hist.last(s.StatsWindow)
	st := &r.stats
	st.Window = len(win)
	if len(win) == 0 {
		return
	}
	st.Last = float64(win[len(win)-1].RTT)
	var sum, jsum float64
	var ok, lost, jn int
	st.Min, st.Max = 0, 0
	prev := -1.0
	for _, x := range win {
		if x.Lost() {
			lost++
			continue
		}
		v := float64(x.RTT)
		if ok == 0 || v < st.Min {
			st.Min = v
		}
		if v > st.Max {
			st.Max = v
		}
		sum += v
		ok++
		if prev >= 0 {
			jsum += math.Abs(v - prev)
			jn++
		}
		prev = v
	}
	st.Avg = 0
	if ok > 0 {
		st.Avg = round2(sum / float64(ok))
	}
	st.Jitter = 0
	if jn > 0 {
		st.Jitter = round2(jsum / float64(jn))
	}
	st.Loss = round2(float64(lost) * 100 / float64(len(win)))

	warnMs := s.WarnMs
	if r.target.WarnMs > 0 {
		warnMs = r.target.WarnMs
	}
	status := StatusUp
	switch {
	case st.ConsecLost >= s.DownAfter:
		status = StatusDown
	case st.ConsecLost > 0,
		s.WarnLossPct > 0 && st.Loss > s.WarnLossPct,
		warnMs > 0 && ok > 0 && st.Avg > warnMs:
		status = StatusWarn
	}
	r.setStatus(status)
}

func (r *runner) setStatus(s string) {
	if r.stats.Status != s {
		r.stats.Status = s
		r.stats.Since = nowMs()
	}
}

func nowMs() int64 { return time.Now().UnixMilli() }

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// SortedIDs är en hjälpfunktion för tester.
func SortedIDs(m map[string]Stats) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
