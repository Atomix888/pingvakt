// Package alert utvärderar larmregler mot mätningarna och utför åtgärder
// (popup/ljud i gränssnittet, systemnotis, e-post och loggfil).
package alert

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"pingvakt/core/i18n"
	"pingvakt/core/monitor"
)

// Condition är typen av villkor.
type Condition string

const (
	CondDown    Condition = "down"    // förlorade paket
	CondLatency Condition = "latency" // latens över gränsvärde (förlorat paket räknas också)
	CondLoss    Condition = "loss"    // paketförlust i procent över fönstret
	CondOffline Condition = "offline" // agenten har slutat rapportera (bara på servern)
	CondCPU     Condition = "cpu"     // agentens CPU över gränsvärde (procent) i N minuter
	CondMem     Condition = "mem"     // agentens minne över gränsvärde (procent) i N minuter
	CondDisk    Condition = "disk"    // någon av agentens diskar fylld över gränsvärde (procent)
	CondRSSI    Condition = "rssi"    // agentens Wi-Fi-signal under gränsvärde (dBm) i N minuter
	CondPath    Condition = "path"    // vägen (traceroute) till ett bevakat mål har ändrats (bara på servern)
	CondCert    Condition = "cert"    // HTTPS-certifikatet går ut inom N dagar (bara på servern)
)

// ForTargets anger om villkoret gäller mätmål (annars agenten/datorn).
func (c Condition) ForTargets() bool {
	return c == CondDown || c == CondLatency || c == CondLoss
}

// Alert är en larmdefinition som kan kopplas till ett eller flera mål.
type Alert struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Condition Condition `json:"condition"`
	Threshold float64   `json:"threshold"` // ms (latency) eller % (loss)
	Count     int       `json:"count"`     // antal "dåliga" mätpunkter ...
	Samples   int       `json:"samples"`   // ... av de senaste N

	Popup     bool   `json:"popup"`
	Sound     bool   `json:"sound"`
	SoundName string `json:"soundName"`
	Notify    bool   `json:"notify"` // systemnotis
	Email     bool   `json:"email"`
	EmailTo   string `json:"emailTo"`
	Log       bool   `json:"log"`
	Webhook   bool   `json:"webhook"`
	HookURL   string `json:"hookUrl"` // tar emot JSON via POST (Teams, Slack, egen tjänst)

	// Serverns kanaler (används inte av skrivbordsappen)
	Push     bool   `json:"push,omitempty"`     // push till mobilen (ntfy, Pushover, webbappen)
	SMS      bool   `json:"sms,omitempty"`      // SMS till SMSTo
	SMSTo    string `json:"smsTo,omitempty"`    // telefonnummer, kommaseparerade
	Teams    bool   `json:"teams,omitempty"`    // Teams-kort till TeamsURL
	TeamsURL string `json:"teamsUrl,omitempty"` // Teams Workflows/inkommande webhook
	OnCall   string `json:"onCall,omitempty"`   // jourschema som också meddelas

	RepeatMin int  `json:"repeatMin"` // upprepa medan larmet är aktivt (0 = nej)
	OnClear   bool `json:"onClear"`   // meddela när larmet upphör
}

// Normalize sätter rimliga standardvärden.
func (a *Alert) Normalize() {
	if a.ID == "" {
		a.ID = monitor.NewID()
	}
	a.Name = strings.TrimSpace(a.Name)
	switch a.Condition {
	case CondLatency, CondLoss, CondOffline, CondCPU, CondMem, CondDisk, CondCert, CondRSSI, CondPath:
	default:
		a.Condition = CondDown
	}
	switch a.Condition {
	case CondCPU, CondMem, CondDisk:
		if a.Threshold <= 0 || a.Threshold > 100 {
			a.Threshold = 90
		}
		if a.Samples < 1 {
			a.Samples = map[Condition]int{CondCPU: 5, CondMem: 5, CondDisk: 1}[a.Condition]
		}
		if a.Samples > 1440 {
			a.Samples = 1440
		}
		a.Count = a.Samples
	}
	if a.Condition == CondRSSI {
		if a.Threshold >= 0 || a.Threshold < -100 {
			a.Threshold = -75
		}
		if a.Samples < 1 {
			a.Samples = 5
		}
		if a.Samples > 120 {
			a.Samples = 120
		}
		a.Count = a.Samples
	}
	if a.Condition == CondPath {
		a.Samples, a.Count, a.RepeatMin, a.OnClear = 1, 1, 0, false
	}
	if a.Condition == CondCert {
		if a.Threshold <= 0 {
			a.Threshold = 14
		}
		a.Samples, a.Count = 1, 1
	}
	a.HookURL = strings.TrimSpace(a.HookURL)
	a.TeamsURL = strings.TrimSpace(a.TeamsURL)
	a.SMSTo = strings.TrimSpace(a.SMSTo)
	if a.Samples < 1 {
		a.Samples = map[Condition]int{CondDown: 3, CondLatency: 10, CondLoss: 60}[a.Condition]
	}
	if a.Samples > 3600 {
		a.Samples = 3600
	}
	if a.Count < 1 || a.Count > a.Samples {
		a.Count = a.Samples
		if a.Condition == CondLatency {
			a.Count = (a.Samples + 1) / 2
		}
	}
	if a.Condition == CondLatency && a.Threshold <= 0 {
		a.Threshold = 100
	}
	if a.Condition == CondLoss && a.Threshold <= 0 {
		a.Threshold = 5
	}
	if a.Name == "" {
		a.Name = a.Describe()
	}
	if a.SoundName == "" {
		a.SoundName = "beep"
	}
	if a.RepeatMin < 0 {
		a.RepeatMin = 0
	}
}

// Describe ger en läsbar beskrivning av villkoret.
func (a Alert) Describe() string {
	switch a.Condition {
	case CondLatency:
		return fmt.Sprintf(i18n.T("Latency > %g ms (%d of %d)"), a.Threshold, a.Count, a.Samples)
	case CondLoss:
		return fmt.Sprintf(i18n.T("Loss > %g %% (last %d)"), a.Threshold, a.Samples)
	case CondOffline:
		return "Agent offline"
	case CondCPU:
		return fmt.Sprintf(i18n.T("CPU > %g %% for %d min"), a.Threshold, a.Samples)
	case CondMem:
		return fmt.Sprintf(i18n.T("Memory > %g %% for %d min"), a.Threshold, a.Samples)
	case CondDisk:
		return fmt.Sprintf("Disk > %g %% full", a.Threshold)
	case CondRSSI:
		return fmt.Sprintf(i18n.T("Wi-Fi signal < %g dBm for %d min"), a.Threshold, a.Samples)
	case CondPath:
		return i18n.T("Path to target changes")
	case CondCert:
		return fmt.Sprintf(i18n.T("Certificate expires within %g days"), a.Threshold)
	}
	return fmt.Sprintf(i18n.T("Down (%d of %d lost)"), a.Count, a.Samples)
}

// Evaluate avgör om villkoret är uppfyllt för mätpunkterna (senaste sist).
func (a Alert) Evaluate(samples []monitor.Sample) (bool, string) {
	if len(samples) > a.Samples {
		samples = samples[len(samples)-a.Samples:]
	}
	if len(samples) == 0 || !a.Condition.ForTargets() {
		return false, ""
	}
	switch a.Condition {
	case CondLoss:
		if len(samples) < a.Samples && len(samples) < 10 {
			return false, ""
		}
		lost := 0
		for _, s := range samples {
			if s.Lost() {
				lost++
			}
		}
		pct := float64(lost) * 100 / float64(len(samples))
		return pct > a.Threshold, fmt.Sprintf(i18n.T("Packet loss %.1f %% (%d of %d)"), pct, lost, len(samples))
	case CondLatency:
		bad, n := 0, 0
		var sum float64
		for _, s := range samples {
			if s.Lost() || float64(s.RTT) > a.Threshold {
				bad++
			}
			if !s.Lost() {
				sum += float64(s.RTT)
				n++
			}
		}
		avg := "–"
		if n > 0 {
			avg = fmt.Sprintf("%.1f ms", sum/float64(n))
		}
		return bad >= a.Count, fmt.Sprintf(i18n.T("%d of %d over %g ms or lost (avg %s)"), bad, len(samples), a.Threshold, avg)
	default:
		bad := 0
		for _, s := range samples {
			if s.Lost() {
				bad++
			}
		}
		return bad >= a.Count, fmt.Sprintf(i18n.T("%d of %d latest packets lost"), bad, len(samples))
	}
}

// Event beskriver ett larm som utlöstes, upprepades eller upphörde.
type Event struct {
	Kind       string `json:"kind"` // fire, repeat, clear, test
	AlertID    string `json:"alertId"`
	AlertName  string `json:"alertName"`
	TargetID   string `json:"targetId"`
	TargetName string `json:"targetName"`
	Host       string `json:"host"`
	Time       int64  `json:"time"`
	Detail     string `json:"detail"`
	Popup      bool   `json:"popup"`
	Sound      bool   `json:"sound"`
	SoundName  string `json:"soundName"`
	Note       string `json:"note,omitempty"`       // t.ex. "Servicefönster" eller "Kvitterat"
	Suppressed bool   `json:"suppressed,omitempty"` // inga åtgärder utfördes (e-post, webhook …)
}

// KindText är händelsetypen på svenska.
func (e Event) KindText() string {
	switch e.Kind {
	case "fire":
		return i18n.T("FIRED")
	case "repeat":
		return i18n.T("ONGOING")
	case "clear":
		return i18n.T("CLEARED")
	}
	return "TEST"
}

type state struct {
	active   bool
	since    time.Time
	lastSent time.Time
}

// Source ger larmmotorn tillgång till mål och mätpunkter. monitor.Manager
// uppfyller gränssnittet; servern har en egen implementation.
type Source interface {
	Target(id string) (monitor.Target, bool)
	Recent(id string, n int) []monitor.Sample
}

// Engine utvärderar larm vid varje mätning.
type Engine struct {
	mu      sync.Mutex
	alerts  map[string]Alert
	state   map[string]*state // nyckel: alertID|targetID
	mon     Source
	mailer  *Mailer
	logPath string
	onEvent func(Event)
	history []Event

	// Filter anropas före åtgärderna. Returnerar den false utförs inga åtgärder
	// (händelsen skickas ändå till onEvent med Suppressed = true).
	Filter func(a Alert, ev *Event) bool
	// Extra anropas för varje händelse som inte stoppats av Filter (serverns egna kanaler).
	Extra func(a Alert, ev Event)
	// TestExtra anropas när en regel testas och provar serverns egna kanaler.
	TestExtra func(a Alert, ev Event) error
}

// NewEngine skapar en larmmotor och kopplar den till monitorn.
func NewEngine(mon *monitor.Manager, mailer *Mailer, logPath string, onEvent func(Event)) *Engine {
	e := NewEngineFor(mon, mailer, logPath, onEvent)
	mon.Subscribe(e.Handle)
	return e
}

// NewEngineFor skapar en larmmotor för en godtycklig källa. Anroparen matar den med Handle.
func NewEngineFor(src Source, mailer *Mailer, logPath string, onEvent func(Event)) *Engine {
	return &Engine{alerts: map[string]Alert{}, state: map[string]*state{}, mon: src, mailer: mailer, logPath: logPath, onEvent: onEvent}
}

// Alert returnerar en larmdefinition.
func (e *Engine) Alert(id string) (Alert, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.alerts[id]
	return a, ok
}

// Alerts returnerar alla larmdefinitioner.
func (e *Engine) Alerts() []Alert {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Alert, 0, len(e.alerts))
	for _, a := range e.alerts {
		out = append(out, a)
	}
	return out
}

// SetAlerts byter larmdefinitioner. Aktiva larm för borttagna definitioner nollställs.
func (e *Engine) SetAlerts(list []Alert) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.alerts = map[string]Alert{}
	for _, a := range list {
		e.alerts[a.ID] = a
	}
	for k := range e.state {
		if _, ok := e.alerts[strings.SplitN(k, "|", 2)[0]]; !ok {
			delete(e.state, k)
		}
	}
}

// SetLogPath byter fil för larmloggen.
func (e *Engine) SetLogPath(p string) {
	e.mu.Lock()
	e.logPath = p
	e.mu.Unlock()
}

// Clear nollställer ett aktivt larm (t.ex. när målet tas bort).
func (e *Engine) Clear(key string) {
	e.mu.Lock()
	delete(e.state, key)
	e.mu.Unlock()
}

// Active returnerar nycklar (alertID|targetID) för aktiva larm.
func (e *Engine) Active() map[string]int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[string]int64{}
	for k, s := range e.state {
		if s.active {
			out[k] = s.since.UnixMilli()
		}
	}
	return out
}

// History returnerar de senaste larmhändelserna (nyast först).
func (e *Engine) History() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Event, len(e.history))
	for i, ev := range e.history {
		out[len(e.history)-1-i] = ev
	}
	return out
}

// Handle utvärderar larmen för målet i uppdateringen.
func (e *Engine) Handle(u monitor.Update) {
	t, ok := e.mon.Target(u.TargetID)
	if !ok || t.Paused || len(t.AlertIDs) == 0 {
		return
	}
	now := time.Now()
	var events []Event
	var fire []Alert
	e.mu.Lock()
	for _, aid := range t.AlertIDs {
		a, ok := e.alerts[aid]
		if !ok || !a.Condition.ForTargets() {
			continue
		}
		met, detail := a.Evaluate(e.mon.Recent(t.ID, a.Samples))
		key := aid + "|" + t.ID
		st := e.state[key]
		if st == nil {
			st = &state{}
			e.state[key] = st
		}
		kind := ""
		switch {
		case met && !st.active:
			st.active, st.since, st.lastSent = true, now, now
			kind = "fire"
		case met && st.active && a.RepeatMin > 0 && now.Sub(st.lastSent) >= time.Duration(a.RepeatMin)*time.Minute:
			st.lastSent = now
			kind = "repeat"
		case !met && st.active:
			st.active = false
			if a.OnClear {
				dur := now.Sub(st.since).Round(time.Second)
				detail = fmt.Sprintf(i18n.T("The alert lasted %s"), dur)
				kind = "clear"
			}
		}
		if kind == "" {
			continue
		}
		name := t.Name
		if name == "" {
			name = t.Host
		}
		ev := Event{Kind: kind, AlertID: a.ID, AlertName: a.Name, TargetID: t.ID, TargetName: name, Host: t.Host,
			Time: now.UnixMilli(), Detail: detail, Popup: a.Popup, Sound: a.Sound && kind != "clear", SoundName: a.SoundName}
		e.history = append(e.history, ev)
		if len(e.history) > 500 {
			e.history = e.history[len(e.history)-500:]
		}
		events = append(events, ev)
		fire = append(fire, a)
	}
	logPath := e.logPath
	e.mu.Unlock()

	for i, ev := range events {
		e.dispatch(fire[i], ev, logPath)
	}
}

// Fire registrerar en händelse som upptäckts utanför motorn (t.ex. agent offline)
// och utför larmets åtgärder.
func (e *Engine) Fire(a Alert, ev Event) {
	e.mu.Lock()
	e.history = append(e.history, ev)
	if len(e.history) > 500 {
		e.history = e.history[len(e.history)-500:]
	}
	logPath := e.logPath
	e.mu.Unlock()
	e.dispatch(a, ev, logPath)
}

func (e *Engine) dispatch(a Alert, ev Event, logPath string) {
	if e.Filter != nil && !e.Filter(a, &ev) {
		ev.Suppressed, ev.Popup, ev.Sound = true, false, false
		if e.onEvent != nil {
			e.onEvent(ev)
		}
		return
	}
	if e.onEvent != nil {
		e.onEvent(ev)
	}
	if a.Log && logPath != "" {
		_ = appendLog(logPath, ev)
	}
	if a.Notify {
		go Notify(fmt.Sprintf("%s: %s", ev.KindText(), ev.TargetName), ev.AlertName+" – "+ev.Detail)
	}
	if a.Email && e.mailer != nil {
		to := SplitAddresses(a.EmailTo)
		if len(to) > 0 {
			e.mailer.Queue(a.ID, to, ev)
		}
	}
	if a.Webhook && a.HookURL != "" {
		go func() { _ = PostWebhook(a.HookURL, ev) }()
	}
	if e.Extra != nil {
		e.Extra(a, ev)
	}
}

// Test skickar en testhändelse genom alla åtgärder för larmet.
func (e *Engine) Test(a Alert) error {
	ev := Event{Kind: "test", AlertID: a.ID, AlertName: a.Name, TargetName: i18n.T("Test target"), Host: "192.0.2.1",
		Time: time.Now().UnixMilli(), Detail: i18n.T("This is a test of the alert"), Popup: a.Popup, Sound: a.Sound, SoundName: a.SoundName}
	e.mu.Lock()
	logPath := e.logPath
	e.mu.Unlock()
	if e.onEvent != nil {
		e.onEvent(ev)
	}
	if a.Log && logPath != "" {
		if err := appendLog(logPath, ev); err != nil {
			return fmt.Errorf("log file: %w", err)
		}
	}
	if a.Notify {
		go Notify("TEST: "+a.Name, ev.Detail)
	}
	if a.Webhook && a.HookURL != "" {
		if err := PostWebhook(a.HookURL, ev); err != nil {
			return fmt.Errorf("webhook: %w", err)
		}
	}
	if e.TestExtra != nil {
		if err := e.TestExtra(a, ev); err != nil {
			return err
		}
	}
	if a.Email && e.mailer != nil {
		to := SplitAddresses(a.EmailTo)
		if len(to) == 0 {
			return fmt.Errorf("no recipient specified")
		}
		return e.mailer.SendNow(to, []Event{ev})
	}
	return nil
}

func appendLog(path string, ev Event) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\t%s\t%s\t%s (%s)\t%s\n",
		time.UnixMilli(ev.Time).Format("2006-01-02 15:04:05"), ev.KindText(), ev.AlertName, ev.TargetName, ev.Host, ev.Detail)
	return err
}

// SplitAddresses delar upp en lista med e-postadresser.
func SplitAddresses(s string) []string {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' })
	sort.Strings(f)
	out := f[:0]
	for i, x := range f {
		if strings.Contains(x, "@") && (i == 0 || f[i-1] != x) {
			out = append(out, x)
		}
	}
	return out
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
