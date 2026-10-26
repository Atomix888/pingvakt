// Command pingvakt-agent mäter mot de mål som servern bestämmer och skickar
// resultatet till PingVakt-servern. Agenten ansluter alltid utåt (HTTPS),
// så den fungerar bakom NAT och brandvägg utan portöppning.
//
//	pingvakt-agent enroll --server https://pingvakt.exempel.se --token pva_...
//	pingvakt-agent install && pingvakt-agent start
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"time"

	"github.com/kardianos/service"

	"pingvakt/core/agentproto"
	"pingvakt/core/hostinfo"
	"pingvakt/core/monitor"
	"pingvakt/core/netscan"
	"pingvakt/core/wifi"
)

// Version sätts vid bygge.
var Version = "0.7.0"

type agentConfig struct {
	Server   string `json:"server"`
	Token    string `json:"token"`
	Insecure bool   `json:"insecure,omitempty"`
}

func defaultConfigPath() string {
	if p := os.Getenv("PINGVAKT_AGENT_CONFIG"); p != "" {
		return p
	}
	switch runtime.GOOS {
	case "windows":
		pd := os.Getenv("ProgramData")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		return filepath.Join(pd, "PingVakt", "agent.json")
	case "darwin":
		return "/Library/Application Support/PingVakt/agent.json"
	default:
		return "/etc/pingvakt/agent.json"
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `PingVakt agent %s

Usage:
  pingvakt-agent enroll --server URL --token TOKEN [--insecure]   enroll with the server
  pingvakt-agent install | uninstall | start | stop | restart     manage the service
  pingvakt-agent status                                           show configuration and service status
  pingvakt-agent run                                              run in the foreground (used by the service)
  pingvakt-agent version

Common flag: --config FILE (default %s)
`, Version, defaultConfigPath())
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "konfigurationsfil")
	server := fs.String("server", "", "the server address, e.g. https://pingvakt.example.com")
	token := fs.String("token", "", "the agent's token from the server")
	insecure := fs.Bool("insecure", false, "accept self-signed certificate (testing only)")
	_ = fs.Parse(os.Args[2:])

	svc, err := service.New(&program{cfgPath: *cfgPath}, &service.Config{
		Name:        "pingvakt-agent",
		DisplayName: "PingVakt Agent",
		Description: "Measures latency and packet loss and reports to the PingVakt server.",
		Arguments:   []string{"run", "--config", *cfgPath},
		Option: service.KeyValue{
			"Restart":                "always",
			"RestartSec":             5,
			"KeepAlive":              true,
			"RunAtLoad":              true,
			"OnFailure":              "restart",
			"OnFailureDelayDuration": "5s",
			"DelayedAutoStart":       true,
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	switch cmd {
	case "enroll":
		if err := enroll(*cfgPath, *server, *token, *insecure); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
	case "run":
		if err := svc.Run(); err != nil {
			log.Fatal(err)
		}
	case "install", "uninstall", "start", "stop", "restart":
		if err := service.Control(svc, cmd); err != nil {
			fmt.Fprintf(os.Stderr, "Error during %s: %v\n", cmd, err)
			os.Exit(1)
		}
		fmt.Println("OK:", cmd)
	case "status":
		c, err := loadConfig(*cfgPath)
		if err != nil {
			fmt.Println("Not enrolled:", err)
		} else {
			fmt.Println("Server:", c.Server)
		}
		st, err := svc.Status()
		switch {
		case err != nil:
			fmt.Println("Service: not installed")
		case st == service.StatusRunning:
			fmt.Println("Service: running")
		case st == service.StatusStopped:
			fmt.Println("Service: stopped")
		default:
			fmt.Println("Service: unknown status")
		}
	case "version", "--version", "-v":
		fmt.Println("pingvakt-agent", Version, runtime.GOOS+"/"+runtime.GOARCH)
	default:
		usage()
		os.Exit(2)
	}
}

func loadConfig(p string) (agentConfig, error) {
	var c agentConfig
	b, err := os.ReadFile(p)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	if c.Server == "" || c.Token == "" {
		return c, errors.New("server or token missing in " + p)
	}
	return c, nil
}

func enroll(path, server, token string, insecure bool) error {
	if server == "" || token == "" {
		return errors.New("specify --server and --token")
	}
	if _, err := agentproto.ReportURL(server); err != nil {
		return err
	}
	up := newUploader(agentConfig{Server: server, Token: token, Insecure: insecure})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := up.Send(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(agentConfig{Server: server, Token: token, Insecure: insecure}, "", "  ")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	st := up.Status()
	fmt.Printf("Enrolled as \"%s\" at %s / %s.\nConfiguration saved in %s\n", st.AgentName, st.Customer, st.Site, path)
	return nil
}

func newUploader(c agentConfig) *agentproto.Uploader {
	host, _ := os.Hostname()
	return &agentproto.Uploader{ServerURL: c.Server, Token: c.Token, Insecure: c.Insecure,
		Info: agentproto.AgentInfo{Version: Version, Hostname: host, OS: runtime.GOOS + "/" + runtime.GOARCH, Kind: agentproto.KindAgent}}
}

// ---------- tjänsten ----------

type program struct {
	cfgPath string
	cancel  context.CancelFunc
	done    chan struct{}
}

func (p *program) Start(s service.Service) error {
	c, err := loadConfig(p.cfgPath)
	if err != nil {
		return fmt.Errorf("the agent is not enrolled (%v) – run 'pingvakt-agent enroll' first", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})
	go func() {
		defer close(p.done)
		run(ctx, c, filepath.Join(filepath.Dir(p.cfgPath), "agent-cache.json"))
	}()
	return nil
}

func (p *program) Stop(s service.Service) error {
	if p.cancel != nil {
		p.cancel()
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
		}
	}
	return nil
}

func run(ctx context.Context, c agentConfig, cachePath string) {
	log.Printf("PingVakt agent %s starting, server %s", Version, c.Server)
	cleanupOld()
	mon := monitor.NewManager(monitor.DefaultSettings())
	defer mon.Close()
	up := newUploader(c)
	hc := hostinfo.New()
	up.Host = func() *hostinfo.Metrics { m := hc.Collect(); return &m }
	up.WiFi = wifi.New().Get
	up.Nets = netscan.Networks

	apply := func(cfg agentproto.Config) {
		mon.SetSettings(cfg.Settings)
		want := map[string]monitor.Target{}
		for _, t := range cfg.Targets {
			want[t.ID] = t
		}
		for _, t := range mon.Targets() {
			if _, ok := want[t.ID]; !ok {
				mon.Remove(t.ID)
			}
		}
		for _, t := range cfg.Targets {
			if old, ok := mon.Target(t.ID); ok {
				t.Normalize()
				if reflect.DeepEqual(old, t) {
					continue
				}
			}
			mon.Upsert(t)
		}
		ids := make([]string, 0, len(cfg.Targets))
		for _, t := range cfg.Targets {
			ids = append(ids, t.ID)
		}
		mon.Reorder(ids)
		log.Printf("configuration: %d targets", len(cfg.Targets))
	}
	// senaste kända konfiguration – agenten mäter även om servern inte nås vid start
	if b, err := os.ReadFile(cachePath); err == nil {
		var cfg agentproto.Config
		if json.Unmarshal(b, &cfg) == nil {
			apply(cfg)
		}
	}
	upd := &updater{up: up, server: c.Server, restart: func() {
		fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = up.SendAll(fctx)
		cancel()
		restartSelf()
	}}
	up.OnUpdate = upd.offer
	up.OnConfig = func(cfg agentproto.Config) {
		apply(cfg)
		if b, err := json.Marshal(cfg); err == nil {
			_ = os.WriteFile(cachePath, b, 0o600)
		}
	}
	mon.Subscribe(up.AddUpdate)

	go func() {
		// logga ändringar i anslutningen
		last := ""
		tk := time.NewTicker(10 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
			}
			st := up.Status()
			cur := "connected"
			if !st.Connected {
				cur = "not connected: " + st.LastError
			}
			if cur != last {
				log.Printf("server: %s (%d samples queued)", cur, st.Buffered)
				last = cur
			}
		}
	}()
	// första kontakten direkt
	_ = up.SendAll(ctx)
	up.Run(ctx)
	fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = up.SendAll(fctx)
	log.Printf("agent stopped")
}
