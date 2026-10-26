// Package agentproto beskriver protokollet mellan agent/skrivbordsapp och server
// och innehåller klienten (Uploader) som skickar mätdata.
//
// Agenten ansluter alltid utåt till servern med HTTPS och en token. Var
// N:e sekund skickas de mätpunkter som samlats sedan förra gången. Svaret
// innehåller ny konfiguration (mål och inställningar) om den har ändrats.
package agentproto

import (
	"strconv"
	"strings"

	"pingvakt/core/hostinfo"
	"pingvakt/core/monitor"
	"pingvakt/core/trace"
	"pingvakt/core/wifi"
)

// ReportPath är serverns endpoint för rapporter.
const ReportPath = "/api/agent/v1/report"

// ResultPath tar emot svar på kommandon (t.ex. traceroute).
const ResultPath = "/api/agent/v1/result"

// Command är ett uppdrag från servern som agenten utför och svarar på via ResultPath.
type Command struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"` // "trace"
	TargetID string `json:"targetId,omitempty"`
	Host     string `json:"host"`
	MaxHops  int    `json:"maxHops,omitempty"`
}

// CommandResult är svaret på ett Command.
type CommandResult struct {
	ID       string      `json:"id"`
	Kind     string      `json:"kind"`
	TargetID string      `json:"targetId,omitempty"`
	Start    int64       `json:"start"`
	End      int64       `json:"end"`
	Hops     []trace.Hop `json:"hops,omitempty"`
	Err      string      `json:"err,omitempty"`
}

// CmdTrace är kommandot för traceroute.
const CmdTrace = "trace"

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
	H  int     `json:"h,omitempty"` // HTTP-statuskod
	C  *int    `json:"c,omitempty"` // dagar tills certifikatet går ut
}

// Report skickas från klienten.
type Report struct {
	Agent            AgentInfo         `json:"agent"`
	ConfigVersion    int64             `json:"configVersion"`
	LocalTargetsHash string            `json:"localTargetsHash,omitempty"`
	LocalTargets     []monitor.Target  `json:"localTargets,omitempty"`
	Samples          []Sample          `json:"samples"`
	Host             *hostinfo.Metrics `json:"host,omitempty"` // CPU, minne och disk (agenter)
	WiFi             *wifi.Info        `json:"wifi,omitempty"` // trådlös anslutning (om datorn har en)
}

// Config är det servern bestämmer för en agent.
type Config struct {
	Targets   []monitor.Target `json:"targets"`
	Settings  monitor.Settings `json:"settings"`
	ReportSec int              `json:"reportSec"`
}

// Response är serverns svar.
type Response struct {
	AgentID          string      `json:"agentId"`
	AgentName        string      `json:"agentName"`
	Customer         string      `json:"customer"`
	Site             string      `json:"site"`
	ServerTime       int64       `json:"serverTime"`
	ConfigVersion    int64       `json:"configVersion"`
	Config           *Config     `json:"config,omitempty"`
	NeedLocalTargets bool        `json:"needLocalTargets,omitempty"`
	Update           *UpdateInfo `json:"update,omitempty"`
	Commands         []Command   `json:"commands,omitempty"`
}

// UpdateInfo erbjuder agenten en nyare version.
type UpdateInfo struct {
	Version string `json:"version"`
	Path    string `json:"path"` // relativ sökväg på servern, t.ex. /download/agent/pingvakt-agent-linux-amd64
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

// AgentFileName returnerar filnamnet för agenten på en plattform ("linux/arm" → pingvakt-agent-linux-armv7).
func AgentFileName(osArch string) string {
	os, arch, _ := strings.Cut(osArch, "/")
	if arch == "arm" {
		arch = "armv7"
	}
	name := "pingvakt-agent-" + os + "-" + arch
	if os == "windows" {
		name += ".exe"
	}
	return name
}

// NewerVersion avgör om a är en nyare version än b (jämför bara siffrorna, "0.4.0-edge" = "0.4.0").
func NewerVersion(a, b string) bool {
	pa, pb := versionParts(a), versionParts(b)
	if pa == nil || pb == nil {
		return false
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func versionParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+ "); i >= 0 {
		v = v[:i]
	}
	f := strings.Split(v, ".")
	if len(f) < 2 {
		return nil
	}
	out := make([]int, 3)
	for i := 0; i < 3 && i < len(f); i++ {
		n, err := strconv.Atoi(f[i])
		if err != nil {
			return nil
		}
		out[i] = n
	}
	return out
}
