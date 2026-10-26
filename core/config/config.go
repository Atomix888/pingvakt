// Package config läser och sparar programmets inställningar som JSON i
// användarens konfigurationsmapp.
package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"pingvakt/core/alert"
	"pingvakt/core/i18n"
	"pingvakt/core/monitor"
	"pingvakt/core/probe"
)

// UI är inställningar för gränssnittet.
type UI struct {
	Theme   string `json:"theme"`   // auto, dark, light
	Columns int    `json:"columns"` // 0 = automatiskt
	Density string `json:"density"` // normal, compact
	Lang    string `json:"lang"`    // en, sv
}

// Defaults är förval för nya mål.
type Defaults struct {
	IntervalMs int      `json:"intervalMs"`
	TimeoutMs  int      `json:"timeoutMs"`
	Size       int      `json:"size"`
	AlertIDs   []string `json:"alertIds"`
}

// ServerLink är kopplingen till en PingVakt-server.
type ServerLink struct {
	Enabled  bool   `json:"enabled"`
	URL      string `json:"url"`
	Token    string `json:"token"`
	Insecure bool   `json:"insecure,omitempty"`
}

// Config är hela programmets konfiguration.
type Config struct {
	Version  int              `json:"version"`
	Settings monitor.Settings `json:"settings"`
	Defaults Defaults         `json:"defaults"`
	Targets  []monitor.Target `json:"targets"`
	Alerts   []alert.Alert    `json:"alerts"`
	SMTP     alert.SMTP       `json:"smtp"`
	AlertLog string           `json:"alertLog"`
	UI       UI               `json:"ui"`
	Server   ServerLink       `json:"server"`
}

// Dir returnerar mappen där konfigurationen sparas.
func Dir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	return filepath.Join(base, "PingVakt")
}

// Path returnerar sökvägen till konfigurationsfilen.
func Path() string { return filepath.Join(Dir(), "config.json") }

// Default skapar en standardkonfiguration för första start.
func Default() Config {
	down := alert.Alert{ID: "down", Name: i18n.T("Down"), Condition: alert.CondDown, Count: 3, Samples: 3,
		Popup: true, Sound: true, SoundName: "alarm", Log: true, OnClear: true}
	lat := alert.Alert{ID: "latency", Name: i18n.T("High latency"), Condition: alert.CondLatency, Threshold: 150, Count: 6, Samples: 10,
		Popup: true, Log: true, OnClear: true}
	loss := alert.Alert{ID: "loss", Name: i18n.T("Packet loss"), Condition: alert.CondLoss, Threshold: 5, Samples: 60,
		Popup: true, Log: true, OnClear: true}
	for _, a := range []*alert.Alert{&down, &lat, &loss} {
		a.Normalize()
	}
	c := Config{
		Version:  1,
		Settings: monitor.DefaultSettings(),
		Defaults: Defaults{IntervalMs: 1000, TimeoutMs: 2000, Size: 32, AlertIDs: []string{"down"}},
		Alerts:   []alert.Alert{down, lat, loss},
		SMTP:     alert.SMTP{Port: 587, Security: "starttls", MinMinutes: 5},
		AlertLog: filepath.Join(Dir(), i18n.T("alertlog.txt")),
		UI:       UI{Theme: "auto", Density: "normal"},
	}
	for _, h := range []struct{ host, name string }{{"1.1.1.1", "Cloudflare DNS"}, {"8.8.8.8", "Google DNS"}} {
		t := monitor.Target{Host: h.host, Name: h.name, Kind: probe.KindICMP, AlertIDs: []string{"down"}}
		t.Normalize()
		c.Targets = append(c.Targets, t)
	}
	return c
}

// Load läser konfigurationen. Saknas filen returneras standardvärden.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Default(), err
	}
	c := Default()
	c.Targets, c.Alerts = nil, nil
	if err := json.Unmarshal(b, &c); err != nil {
		// spara undan den trasiga filen så att inget går förlorat
		_ = os.WriteFile(path+".trasig", b, 0o600)
		return Default(), err
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(b, &keys) == nil {
		if _, ok := keys["alerts"]; !ok {
			c.Alerts = Default().Alerts
		}
	}
	for i := range c.Targets {
		c.Targets[i].Normalize()
	}
	for i := range c.Alerts {
		c.Alerts[i].Normalize()
	}
	if c.Targets == nil {
		c.Targets = []monitor.Target{}
	}
	if c.Alerts == nil {
		c.Alerts = []alert.Alert{}
	}
	return c, nil
}

// Save skriver konfigurationen atomiskt (via temporär fil).
func Save(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
