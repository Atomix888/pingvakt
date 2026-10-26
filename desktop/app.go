package main

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net"
	"os"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"pingvakt/core/alert"
	"pingvakt/core/config"
	"pingvakt/core/monitor"
	"pingvakt/core/probe"
	"pingvakt/core/trace"
)

// Version är programversionen (sätts vid bygge med -ldflags "-X main.Version=...").
var Version = "0.1.0"

// App är bryggan mellan gränssnittet och mätmotorn. Alla exporterade metoder
// kan anropas från JavaScript via window.go.main.App.
type App struct {
	ctx     context.Context
	mu      sync.Mutex
	cfg     config.Config
	cfgPath string
	mon     *monitor.Manager
	eng     *alert.Engine
	mailer  *alert.Mailer

	pmu     sync.Mutex
	pending map[string][]monitor.Sample

	tmu         sync.Mutex
	traceCancel context.CancelFunc
}

// NewApp skapar appen.
func NewApp() *App {
	return &App{cfgPath: config.Path(), pending: map[string][]monitor.Sample{}}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	cfg, err := config.Load(a.cfgPath)
	if err != nil {
		runtime.LogErrorf(ctx, "konfiguration: %v", err)
	}
	a.cfg = cfg
	a.mon = monitor.NewManager(cfg.Settings)
	a.mailer = alert.NewMailer(cfg.SMTP)
	a.mailer.OnError = func(err error) {
		runtime.EventsEmit(a.ctx, "error", "E-post kunde inte skickas: "+err.Error())
	}
	a.eng = alert.NewEngine(a.mon, a.mailer, cfg.AlertLog, func(ev alert.Event) {
		runtime.EventsEmit(a.ctx, "alert", ev)
	})
	a.eng.SetAlerts(cfg.Alerts)
	a.mon.Subscribe(func(u monitor.Update) {
		a.pmu.Lock()
		a.pending[u.TargetID] = append(a.pending[u.TargetID], u.Sample)
		a.pmu.Unlock()
	})
	for _, t := range cfg.Targets {
		a.mon.Upsert(t)
	}
	if err == nil {
		_ = a.save()
	}
	go a.tickLoop(ctx)
}

func (a *App) shutdown(context.Context) {
	_ = a.save()
	a.mon.Close()
}

// Tick är det paket som skickas till gränssnittet varje sekund.
type Tick struct {
	Stats   map[string]monitor.Stats    `json:"stats"`
	Samples map[string][]monitor.Sample `json:"samples"`
	Active  map[string]int64            `json:"active"`
}

func (a *App) tickLoop(ctx context.Context) {
	tk := time.NewTicker(time.Second)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
		a.pmu.Lock()
		samples := a.pending
		a.pending = map[string][]monitor.Sample{}
		a.pmu.Unlock()
		runtime.EventsEmit(ctx, "tick", Tick{Stats: a.mon.Snapshot(), Samples: samples, Active: a.eng.Active()})
	}
}

func (a *App) save() error {
	a.mu.Lock()
	a.cfg.Targets = a.mon.Targets()
	a.cfg.Settings = a.mon.Settings()
	c := a.cfg
	a.mu.Unlock()
	return config.Save(a.cfgPath, c)
}

// State är hela tillståndet som gränssnittet behöver vid start.
type State struct {
	Version     string                   `json:"version"`
	Platform    string                   `json:"platform"`
	ConfigPath  string                   `json:"configPath"`
	Targets     []monitor.Target         `json:"targets"`
	Stats       map[string]monitor.Stats `json:"stats"`
	Alerts      []alert.Alert            `json:"alerts"`
	Settings    monitor.Settings         `json:"settings"`
	Defaults    config.Defaults          `json:"defaults"`
	SMTP        alert.SMTP               `json:"smtp"`
	HasPassword bool                     `json:"hasPassword"`
	AlertLog    string                   `json:"alertLog"`
	UI          config.UI                `json:"ui"`
	Active      map[string]int64         `json:"active"`
}

// GetState returnerar tillståndet. SMTP-lösenordet skickas aldrig till gränssnittet.
func (a *App) GetState() State {
	a.mu.Lock()
	c := a.cfg
	a.mu.Unlock()
	smtp := c.SMTP
	hasPw := smtp.Password != ""
	smtp.Password = ""
	return State{
		Version: Version, Platform: goruntime.GOOS, ConfigPath: a.cfgPath,
		Targets: a.mon.Targets(), Stats: a.mon.Snapshot(), Alerts: c.Alerts,
		Settings: a.mon.Settings(), Defaults: c.Defaults, SMTP: smtp, HasPassword: hasPw,
		AlertLog: c.AlertLog, UI: c.UI, Active: a.eng.Active(),
	}
}

// AddInput är förval när flera mål läggs till på en gång.
type AddInput struct {
	Kind probe.Kind `json:"kind"`
	Port int        `json:"port"`
}

// AddHosts lägger till ett eller flera mål. Texten kan innehålla flera värdar
// separerade med komma, semikolon, mellanslag eller radbrytning. "värd:port" ger TCP-ping.
func (a *App) AddHosts(text string, in AddInput) ([]monitor.Target, error) {
	a.mu.Lock()
	d := a.cfg.Defaults
	a.mu.Unlock()
	var added []monitor.Target
	for _, tok := range strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\r' || r == '\t'
	}) {
		host, kind, port := parseHostToken(tok, in)
		if host == "" {
			continue
		}
		t := monitor.Target{Host: host, Kind: kind, Port: port, IntervalMs: d.IntervalMs, TimeoutMs: d.TimeoutMs,
			Size: d.Size, AlertIDs: append([]string{}, d.AlertIDs...)}
		added = append(added, a.mon.Upsert(t))
	}
	if len(added) == 0 {
		return nil, errors.New("ange minst ett värdnamn eller en IP-adress")
	}
	return added, a.save()
}

func parseHostToken(tok string, in AddInput) (string, probe.Kind, int) {
	tok = strings.TrimSpace(tok)
	for _, p := range []string{"tcp://", "icmp://", "http://", "https://"} {
		if strings.HasPrefix(strings.ToLower(tok), p) {
			tok = tok[len(p):]
		}
	}
	tok = strings.TrimSuffix(tok, "/")
	kind, port := in.Kind, in.Port
	if kind != probe.KindTCP {
		kind = probe.KindICMP
	}
	if h, p, err := net.SplitHostPort(tok); err == nil {
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
			return h, probe.KindTCP, n
		}
	}
	tok = strings.Trim(tok, "[]")
	if strings.ContainsAny(tok, " /\\\"'<>") {
		return "", kind, port
	}
	return tok, kind, port
}

// SaveTarget sparar ett nytt eller ändrat mål.
func (a *App) SaveTarget(t monitor.Target) (monitor.Target, error) {
	h, _, _ := parseHostToken(t.Host, AddInput{Kind: t.Kind, Port: t.Port})
	if h == "" {
		return t, errors.New("ogiltig värd")
	}
	t.Host = h
	t = a.mon.Upsert(t)
	return t, a.save()
}

// RemoveTargets tar bort mål.
func (a *App) RemoveTargets(ids []string) error {
	for _, id := range ids {
		a.mon.Remove(id)
	}
	return a.save()
}

// SetPaused pausar eller startar mål.
func (a *App) SetPaused(ids []string, paused bool) error {
	for _, id := range ids {
		a.mon.SetPaused(id, paused)
	}
	return a.save()
}

// PauseAll pausar eller startar alla mål.
func (a *App) PauseAll(paused bool) error {
	for _, t := range a.mon.Targets() {
		if t.Paused != paused {
			a.mon.SetPaused(t.ID, paused)
		}
	}
	return a.save()
}

// Reorder sparar ny visningsordning.
func (a *App) Reorder(ids []string) error {
	a.mon.Reorder(ids)
	return a.save()
}

// History är historiken för ett mål i kolumnform (snålt att skicka).
type History struct {
	T []int64   `json:"t"`
	R []float32 `json:"r"`
}

// GetHistory returnerar mätpunkter nyare än sinceMs.
func (a *App) GetHistory(id string, sinceMs int64) History {
	s := a.mon.History(id, sinceMs)
	h := History{T: make([]int64, len(s)), R: make([]float32, len(s))}
	for i, x := range s {
		h.T[i], h.R[i] = x.T, x.RTT
	}
	return h
}

// SettingsInput är det som sparas från inställningsdialogen.
type SettingsInput struct {
	Settings    monitor.Settings `json:"settings"`
	Defaults    config.Defaults  `json:"defaults"`
	SMTP        alert.SMTP       `json:"smtp"`
	NewPassword bool             `json:"newPassword"` // false = behåll sparat lösenord
	AlertLog    string           `json:"alertLog"`
	UI          config.UI        `json:"ui"`
}

// SaveSettings sparar inställningar.
func (a *App) SaveSettings(in SettingsInput) error {
	a.mu.Lock()
	if !in.NewPassword {
		in.SMTP.Password = a.cfg.SMTP.Password
	}
	in.SMTP.Host = strings.TrimSpace(in.SMTP.Host)
	if in.SMTP.MinMinutes < 0 {
		in.SMTP.MinMinutes = 0
	}
	a.cfg.Defaults = in.Defaults
	a.cfg.SMTP = in.SMTP
	a.cfg.AlertLog = strings.TrimSpace(in.AlertLog)
	a.cfg.UI = in.UI
	a.mu.Unlock()
	a.mon.SetSettings(in.Settings)
	a.mailer.SetConfig(in.SMTP)
	a.eng.SetLogPath(in.AlertLog)
	return a.save()
}

// SaveAlerts sparar larmdefinitionerna. Kopplingar till borttagna larm tas bort från målen.
func (a *App) SaveAlerts(list []alert.Alert) ([]alert.Alert, error) {
	ids := map[string]bool{}
	for i := range list {
		list[i].Normalize()
		ids[list[i].ID] = true
	}
	a.mu.Lock()
	a.cfg.Alerts = list
	var defs []string
	for _, id := range a.cfg.Defaults.AlertIDs {
		if ids[id] {
			defs = append(defs, id)
		}
	}
	a.cfg.Defaults.AlertIDs = defs
	a.mu.Unlock()
	a.eng.SetAlerts(list)
	for _, t := range a.mon.Targets() {
		var keep []string
		for _, id := range t.AlertIDs {
			if ids[id] {
				keep = append(keep, id)
			}
		}
		if len(keep) != len(t.AlertIDs) {
			t.AlertIDs = keep
			a.mon.Upsert(t)
		}
	}
	return list, a.save()
}

// AssignAlert kopplar (eller kopplar bort) ett larm till alla angivna mål.
func (a *App) AssignAlert(alertID string, ids []string, on bool) error {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	for _, t := range a.mon.Targets() {
		if !want[t.ID] {
			continue
		}
		has := false
		var out []string
		for _, x := range t.AlertIDs {
			if x == alertID {
				has = true
				if !on {
					continue
				}
			}
			out = append(out, x)
		}
		if on && !has {
			out = append(out, alertID)
		}
		t.AlertIDs = out
		a.mon.Upsert(t)
	}
	return a.save()
}

// TestAlert kör larmets åtgärder en gång med en testhändelse.
func (a *App) TestAlert(al alert.Alert) error {
	al.Normalize()
	return a.eng.Test(al)
}

// TestEmail skickar ett testmejl med nuvarande (eller angivna) SMTP-inställningar.
func (a *App) TestEmail(to string, smtp alert.SMTP, newPassword bool) error {
	if !newPassword {
		a.mu.Lock()
		smtp.Password = a.cfg.SMTP.Password
		a.mu.Unlock()
	}
	rcpt := alert.SplitAddresses(to)
	if len(rcpt) == 0 {
		return errors.New("ange en mottagaradress")
	}
	return alert.Send(smtp, rcpt, "[PingVakt] Testmejl",
		"Det här är ett testmejl från PingVakt.\r\n\r\nOm du läser detta fungerar e-postinställningarna.\r\n")
}

// AlertHistory returnerar senaste larmhändelser.
func (a *App) AlertHistory() []alert.Event { return a.eng.History() }

// StartTrace startar en traceroute. Hopp skickas som händelsen "trace:hop",
// och "trace:done" när den är klar.
func (a *App) StartTrace(host string) error {
	a.StopTrace()
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Minute)
	a.tmu.Lock()
	a.traceCancel = cancel
	a.tmu.Unlock()
	go func() {
		defer cancel()
		err := trace.Run(ctx, host, 30, func(h trace.Hop) { runtime.EventsEmit(a.ctx, "trace:hop", h) })
		msg := ""
		if err != nil && !errors.Is(err, context.Canceled) {
			msg = err.Error()
			if errors.Is(err, context.DeadlineExceeded) {
				msg = "Traceroute tog för lång tid och avbröts"
			}
		}
		runtime.EventsEmit(a.ctx, "trace:done", msg)
	}()
	return nil
}

// StopTrace avbryter pågående traceroute.
func (a *App) StopTrace() {
	a.tmu.Lock()
	if a.traceCancel != nil {
		a.traceCancel()
		a.traceCancel = nil
	}
	a.tmu.Unlock()
}

// ExportCSV sparar historiken för angivna mål till en CSV-fil som användaren väljer.
func (a *App) ExportCSV(ids []string, sinceMs int64) (string, error) {
	name := "pingvakt-" + time.Now().Format("2006-01-02-1504") + ".csv"
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title: "Exportera mätdata", DefaultFilename: name,
		Filters: []runtime.FileFilter{{DisplayName: "CSV (*.csv)", Pattern: "*.csv"}},
	})
	if err != nil || path == "" {
		return "", err
	}
	if !strings.HasSuffix(strings.ToLower(path), ".csv") {
		path += ".csv"
	}
	return path, a.writeCSV(path, ids, sinceMs)
}

func (a *App) writeCSV(path string, ids []string, sinceMs int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, _ = f.Write([]byte{0xEF, 0xBB, 0xBF}) // BOM så att Excel läser åäö rätt
	w := csv.NewWriter(f)
	w.Comma = ';'
	_ = w.Write([]string{"Tid", "Mål", "Värd", "Typ", "Latens (ms)", "Förlorad"})
	type row struct {
		t    int64
		cols []string
	}
	var rows []row
	for _, id := range ids {
		t, ok := a.mon.Target(id)
		if !ok {
			continue
		}
		name := t.Name
		if name == "" {
			name = t.Host
		}
		kind := "ICMP"
		if t.Kind == probe.KindTCP {
			kind = fmt.Sprintf("TCP %d", t.Port)
		}
		for _, s := range a.mon.History(id, sinceMs) {
			lat, lost := strings.Replace(strconv.FormatFloat(float64(s.RTT), 'f', 3, 32), ".", ",", 1), "0"
			if s.Lost() {
				lat, lost = "", "1"
			}
			rows = append(rows, row{s.T, []string{time.UnixMilli(s.T).Format("2006-01-02 15:04:05.000"), name, t.Host, kind, lat, lost}})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].t < rows[j].t })
	for _, r := range rows {
		_ = w.Write(r.cols)
	}
	w.Flush()
	return w.Error()
}

// OpenPath öppnar en fil eller mapp i operativsystemet (t.ex. larmloggen).
func (a *App) OpenPath(p string) {
	if p == "" {
		return
	}
	if _, err := os.Stat(p); err != nil {
		runtime.EventsEmit(a.ctx, "error", "Filen finns inte ännu: "+p)
		return
	}
	runtime.BrowserOpenURL(a.ctx, "file://"+toURLPath(p))
}

func toURLPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}
