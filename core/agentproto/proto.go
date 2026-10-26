// Package agentproto beskriver protokollet mellan agent/skrivbordsapp och server
// och innehåller klienten (Uploader) som skickar mätdata.
//
// Agenten ansluter alltid utåt till servern med HTTPS och en token. Var
// N:e sekund skickas de mätpunkter som samlats sedan förra gången. Svaret
// innehåller ny konfiguration (mål och inställningar) om den har ändrats.
package agentproto

import "pingvakt/core/monitor"

// ReportPath är serverns endpoint för rapporter.
const ReportPath = "/api/agent/v1/report"

// Kind för klienten.
const (
	KindAgent   = "agent"   // målen styrs från servern
	KindDesktop = "desktop" // skrivbordsappen rapporterar sina egna mål
)

// AgentInfo beskriver klienten.
type AgentInfo struct {
	Version  string `json:"version"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Kind     string `json:"kind"`
}

// Sample är en mätpunkt för ett mål. R = -1 betyder förlorat paket.
type Sample struct {
	ID string  `json:"id"`
	T  int64   `json:"t"`
	R  float32 `json:"r"`
	E  string  `json:"e,omitempty"` // fel för förlorat paket
	A  string  `json:"a,omitempty"` // uppslagen IP-adress (skickas när den ändras)
}

// Report skickas från klienten.
type Report struct {
	Agent            AgentInfo        `json:"agent"`
	ConfigVersion    int64            `json:"configVersion"`
	LocalTargetsHash string           `json:"localTargetsHash,omitempty"`
	LocalTargets     []monitor.Target `json:"localTargets,omitempty"`
	Samples          []Sample         `json:"samples"`
}

// Config är det servern bestämmer för en agent.
type Config struct {
	Targets   []monitor.Target `json:"targets"`
	Settings  monitor.Settings `json:"settings"`
	ReportSec int              `json:"reportSec"`
}

// Response är serverns svar.
type Response struct {
	AgentID          string  `json:"agentId"`
	AgentName        string  `json:"agentName"`
	Customer         string  `json:"customer"`
	Site             string  `json:"site"`
	ServerTime       int64   `json:"serverTime"`
	ConfigVersion    int64   `json:"configVersion"`
	Config           *Config `json:"config,omitempty"`
	NeedLocalTargets bool    `json:"needLocalTargets,omitempty"`
}
