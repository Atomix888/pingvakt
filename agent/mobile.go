package main

// Mobilläget används av Android-appen. Appen startar agenten som en process
// ("pingvakt-agent mobile …") och pratar med den över en lokal HTTP-port:
//   - appen skickar Wi-Fi-anslutning, nät, DNS-servrar och avsökningar (POST /api/wifi, /api/survey)
//   - webbvyn i appen visar status och Wi-Fi-survey (GET /, /api/status)
// Alla anrop kräver nyckeln som appen ger i miljövariabeln PV_KEY.

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"pingvakt/core/agentproto"
	"pingvakt/core/i18n"
	"pingvakt/core/monitor"
	"pingvakt/core/netscan"
	"pingvakt/core/wifi"
)

//go:embed mobileui
var mobileUI embed.FS

type mobileState struct {
	mu      sync.Mutex
	dir     string
	device  string
	lang    string
	cfg     *agentConfig
	cancel  context.CancelFunc
	done    chan struct{}
	up      *agentproto.Uploader
	mon     *monitor.Manager
	stats   map[string]monitor.Stats
	wifi    *wifi.Info
	wifiT   time.Time
	nets    []netscan.Network
	scan    *wifi.Scan
	scanReq time.Time // webbvyn eller servern vill ha en ny avsökning
	dns     []string
}

func runMobile(args []string) {
	fs := flag.NewFlagSet("mobile", flag.ExitOnError)
	dir := fs.String("dir", ".", "folder for configuration")
	listen := fs.String("listen", "127.0.0.1:47811", "local address for the app")
	device := fs.String("device", "", "device name shown on the server")
	lang := fs.String("lang", "en", "language for the interface")
	_ = fs.Parse(args)
	key := os.Getenv("PV_KEY")
	if len(key) < 16 {
		log.Fatal("PV_KEY is missing")
	}
	ms := &mobileState{dir: *dir, device: *device, lang: i18n.Norm(*lang, "en"), stats: map[string]monitor.Stats{}}
	i18n.SetLang(ms.lang)
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: ms.dialDNS}
	wifi.SurveyHook = ms.surveyHook
	if c, err := loadConfig(ms.cfgPath()); err == nil {
		ms.start(c)
	}
	mux := http.NewServeMux()
	sub, _ := fs2(mobileUI)
	files := http.FileServer(http.FS(sub))
	mux.Handle("/", files)
	mux.HandleFunc("GET /i18n.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		_, _ = w.Write(i18n.Script(ms.lang))
	})
	mux.HandleFunc("GET /api/status", ms.handleStatus)
	mux.HandleFunc("POST /api/enroll", ms.handleEnroll)
	mux.HandleFunc("POST /api/unenroll", ms.handleUnenroll)
	mux.HandleFunc("POST /api/wifi", ms.handleWiFi)
	mux.HandleFunc("POST /api/survey", ms.handleSurvey)
	mux.HandleFunc("POST /api/survey/request", func(w http.ResponseWriter, r *http.Request) {
		ms.mu.Lock()
		ms.scanReq = time.Now()
		ms.mu.Unlock()
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /api/survey/send", ms.handleSendSurvey)
	srv := &http.Server{Addr: *listen, Handler: keyCheck(key, mux), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("PingVakt mobile agent %s listening on %s", Version, *listen)
	log.Fatal(srv.ListenAndServe())
}

func fs2(f embed.FS) (fs.FS, error) { return fs.Sub(f, "mobileui") }

// keyCheck släpper bara igenom anrop med rätt nyckel (header, cookie eller ?k= som sätter cookien).
func keyCheck(key string, next http.Handler) http.Handler {
	ok := func(v string) bool { return subtle.ConstantTimeCompare([]byte(v), []byte(key)) == 1 }
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if k := r.URL.Query().Get("k"); k != "" && ok(k) {
			http.SetCookie(w, &http.Cookie{Name: "pvk", Value: key, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			next.ServeHTTP(w, r)
			return
		}
		if ok(r.Header.Get("X-PV-Key")) {
			next.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie("pvk"); err == nil && ok(c.Value) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	})
}

func (ms *mobileState) cfgPath() string { return filepath.Join(ms.dir, "agent.json") }

// dialDNS använder DNS-servrarna appen rapporterar (Android har ingen /etc/resolv.conf).
func (ms *mobileState) dialDNS(ctx context.Context, network, _ string) (net.Conn, error) {
	ms.mu.Lock()
	servers := append([]string(nil), ms.dns...)
	ms.mu.Unlock()
	servers = append(servers, "1.1.1.1", "8.8.8.8")
	var d net.Dialer
	var last error
	for _, s := range servers {
		c, err := d.DialContext(ctx, network, net.JoinHostPort(s, "53"))
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

// surveyHook ger servern den senaste avsökningen från appen (ber om en ny om den är gammal).
func (ms *mobileState) surveyHook(ctx context.Context) ([]wifi.BSS, string, error) {
	ms.mu.Lock()
	ms.scanReq = time.Now()
	ms.mu.Unlock()
	deadline := time.Now().Add(20 * time.Second)
	for {
		ms.mu.Lock()
		sc := ms.scan
		ms.mu.Unlock()
		if sc != nil && time.Since(time.UnixMilli(sc.T)) < 45*time.Second {
			return append([]wifi.BSS(nil), sc.BSS...), "android", nil
		}
		if time.Now().After(deadline) {
			if sc != nil && time.Since(time.UnixMilli(sc.T)) < 10*time.Minute {
				return append([]wifi.BSS(nil), sc.BSS...), "android", nil
			}
			return nil, "android", errors.New("no Wi-Fi scan from the phone – open the PingVakt app and allow location access")
		}
		select {
		case <-ctx.Done():
			return nil, "android", ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (ms *mobileState) start(c agentConfig) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	ms.cancel, ms.done, ms.cfg = cancel, make(chan struct{}), &c
	ro := runOpts{mobile: true, device: ms.device,
		wifi: func() *wifi.Info {
			ms.mu.Lock()
			defer ms.mu.Unlock()
			if ms.wifi == nil || time.Since(ms.wifiT) > time.Minute {
				return nil
			}
			cp := *ms.wifi
			return &cp
		},
		nets: func() []netscan.Network {
			ms.mu.Lock()
			defer ms.mu.Unlock()
			return append([]netscan.Network(nil), ms.nets...)
		},
		onStart: func(up *agentproto.Uploader, mon *monitor.Manager) {
			ms.mu.Lock()
			ms.up, ms.mon = up, mon
			ms.mu.Unlock()
			mon.Subscribe(func(u monitor.Update) {
				ms.mu.Lock()
				ms.stats[u.TargetID] = u.Stats
				ms.mu.Unlock()
			})
		}}
	done := ms.done
	go func() {
		defer close(done)
		run(ctx, c, filepath.Join(ms.dir, "agent-cache.json"), ro)
	}()
}

func (ms *mobileState) stop() {
	ms.mu.Lock()
	cancel, done := ms.cancel, ms.done
	ms.cancel, ms.up, ms.mon, ms.cfg = nil, nil, nil, nil
	ms.stats = map[string]monitor.Stats{}
	ms.mu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	}
}

func writeJSONm(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func errJSON(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

type mobileTarget struct {
	ID    string        `json:"id"`
	Name  string        `json:"name"`
	Host  string        `json:"host"`
	Kind  string        `json:"kind"`
	Port  int           `json:"port,omitempty"`
	Stats monitor.Stats `json:"stats"`
}

func (ms *mobileState) handleStatus(w http.ResponseWriter, r *http.Request) {
	ms.mu.Lock()
	out := map[string]any{"version": Version, "device": ms.device, "enrolled": ms.cfg != nil}
	if ms.cfg != nil {
		out["server"] = ms.cfg.Server
	}
	if ms.wifi != nil && time.Since(ms.wifiT) < time.Minute {
		out["wifi"] = ms.wifi
	}
	if ms.scan != nil {
		out["scan"] = surveyWithVendors(*ms.scan)
	}
	out["scanRequested"] = !ms.scanReq.IsZero() && time.Since(ms.scanReq) < 30*time.Second
	up, mon := ms.up, ms.mon
	stats := map[string]monitor.Stats{}
	for k, v := range ms.stats {
		stats[k] = v
	}
	ms.mu.Unlock()
	if up != nil {
		out["link"] = up.Status()
	}
	targets := []mobileTarget{}
	if mon != nil {
		for _, t := range mon.Targets() {
			targets = append(targets, mobileTarget{ID: t.ID, Name: t.Name, Host: t.Host, Kind: string(t.Kind), Port: t.Port, Stats: stats[t.ID]})
		}
	}
	out["targets"] = targets
	writeJSONm(w, out)
}

// surveyWithVendors lägger till tillverkare för visning (samma format som servern använder).
func surveyWithVendors(sc wifi.Scan) map[string]any {
	type bss struct {
		wifi.BSS
		Vendor string `json:"vendor,omitempty"`
	}
	list := make([]bss, 0, len(sc.BSS))
	for _, b := range sc.BSS {
		list = append(list, bss{BSS: b, Vendor: netscan.Vendor(b.BSSID)})
	}
	return map[string]any{"t": sc.T, "tool": sc.Tool, "err": sc.Err, "bss": list}
}

func (ms *mobileState) handleEnroll(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Server, Token string
		Insecure      bool
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in); err != nil {
		errJSON(w, 400, "invalid request")
		return
	}
	in.Server, in.Token = strings.TrimSpace(in.Server), strings.TrimSpace(in.Token)
	if in.Server != "" && !strings.Contains(in.Server, "://") {
		in.Server = "https://" + in.Server
	}
	ms.stop()
	path := ms.cfgPath()
	up := newUploader(agentConfig{Server: in.Server, Token: in.Token, Insecure: in.Insecure})
	if ms.device != "" {
		up.Info.Hostname = ms.device
	}
	up.Info.OS = "android/" + runtime.GOARCH
	if in.Server == "" || in.Token == "" {
		errJSON(w, 400, "enter the server address and the token")
		return
	}
	if _, err := agentproto.ReportURL(in.Server); err != nil {
		errJSON(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if _, err := up.Send(ctx); err != nil {
		errJSON(w, 400, err.Error())
		return
	}
	_ = os.MkdirAll(ms.dir, 0o700)
	c := agentConfig{Server: in.Server, Token: in.Token, Insecure: in.Insecure}
	b, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	ms.start(c)
	st := up.Status()
	writeJSONm(w, map[string]string{"agent": st.AgentName, "customer": st.Customer, "site": st.Site})
}

func (ms *mobileState) handleUnenroll(w http.ResponseWriter, r *http.Request) {
	ms.stop()
	_ = os.Remove(ms.cfgPath())
	_ = os.Remove(filepath.Join(ms.dir, "agent-cache.json"))
	w.WriteHeader(204)
}

// handleWiFi tar emot anslutningen, näten och DNS-servrarna från appen (var 10:e sekund).
func (ms *mobileState) handleWiFi(w http.ResponseWriter, r *http.Request) {
	var in struct {
		WiFi *wifi.Info        `json:"wifi"`
		Nets []netscan.Network `json:"nets"`
		DNS  []string          `json:"dns"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		errJSON(w, 400, "invalid request")
		return
	}
	ms.mu.Lock()
	if in.WiFi != nil && in.WiFi.RSSI < 0 {
		in.WiFi.T = time.Now().UnixMilli()
		if in.WiFi.Ch == 0 {
			in.WiFi.Ch = wifi.ChannelOf(in.WiFi.Freq)
		}
		ms.wifi, ms.wifiT = in.WiFi, time.Now()
	} else {
		ms.wifi = nil
	}
	ms.nets = in.Nets
	var dns []string
	for _, d := range in.DNS {
		if ip := net.ParseIP(strings.TrimSpace(d)); ip != nil {
			dns = append(dns, ip.String())
		}
	}
	ms.dns = dns
	for i := range ms.nets {
		ms.nets[i].DNS = dns
	}
	req := !ms.scanReq.IsZero() && time.Since(ms.scanReq) < 30*time.Second
	ms.mu.Unlock()
	writeJSONm(w, map[string]bool{"scan": req})
}

// handleSurvey tar emot en avsökning från appen.
func (ms *mobileState) handleSurvey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		BSS []wifi.BSS `json:"bss"`
		Err string     `json:"err"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&in); err != nil {
		errJSON(w, 400, "invalid request")
		return
	}
	sc := wifi.Scan{T: time.Now().UnixMilli(), Tool: "android", Err: in.Err, BSS: in.BSS}
	for i := range sc.BSS {
		b := &sc.BSS[i]
		b.BSSID = strings.ToLower(b.BSSID)
		if b.Ch == 0 {
			b.Ch = wifi.ChannelOf(b.Freq)
		}
		if b.Width == 0 {
			b.Width = 20
		}
	}
	sort.Slice(sc.BSS, func(i, j int) bool { return sc.BSS[i].RSSI > sc.BSS[j].RSSI })
	if sc.BSS == nil {
		sc.BSS = []wifi.BSS{}
	}
	ms.mu.Lock()
	if in.Err == "" || len(in.BSS) > 0 {
		ms.scan = &sc
	}
	ms.scanReq = time.Time{}
	ms.mu.Unlock()
	writeJSONm(w, surveyWithVendors(sc))
}

// handleSendSurvey skickar den senaste avsökningen till servern direkt.
func (ms *mobileState) handleSendSurvey(w http.ResponseWriter, r *http.Request) {
	ms.mu.Lock()
	up, sc := ms.up, ms.scan
	ms.mu.Unlock()
	if up == nil {
		errJSON(w, 400, "the phone is not connected to a server")
		return
	}
	if sc == nil {
		errJSON(w, 400, "no scan yet")
		return
	}
	up.AddSurvey(*sc)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := up.SendAll(ctx); err != nil {
		errJSON(w, 400, err.Error())
		return
	}
	w.WriteHeader(204)
}
