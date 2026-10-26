package agentproto

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"pingvakt/core/hostinfo"
	"pingvakt/core/monitor"
)

// ErrUnauthorized betyder att servern inte godkänner token (borttagen eller fel).
var ErrUnauthorized = errors.New("servern godkänner inte token – agenten kan ha tagits bort")

// Status beskriver anslutningen till servern.
type Status struct {
	Enabled   bool   `json:"enabled"`
	Connected bool   `json:"connected"`
	LastOK    int64  `json:"lastOk"`
	LastError string `json:"lastError,omitempty"`
	Buffered  int    `json:"buffered"`
	AgentName string `json:"agentName,omitempty"`
	Customer  string `json:"customer,omitempty"`
	Site      string `json:"site,omitempty"`
}

// Uploader samlar mätpunkter och skickar dem till servern.
type Uploader struct {
	ServerURL string
	Token     string
	Insecure  bool // hoppa över certifikatkontroll (bara för test/egna certifikat)
	Info      AgentInfo
	MaxBuffer int // max antal mätpunkter i minnet när servern inte nås

	// OnConfig anropas när servern skickar ny konfiguration (agenter).
	OnConfig func(Config)
	// OnUpdate anropas när servern erbjuder en nyare agentversion.
	OnUpdate func(UpdateInfo)
	// LocalTargets returnerar klientens egna mål (skrivbordsappen).
	LocalTargets func() []monitor.Target
	// Host returnerar datorns CPU, minne och disk (agenter).
	Host func() *hostinfo.Metrics

	mu            sync.Mutex
	buf           []Sample
	lastAddr      map[string]string
	configVersion int64
	sentHash      string
	needTargets   bool
	reportSec     int
	status        Status
	client        *http.Client
}

// Add lägger en mätpunkt i kö.
func (u *Uploader) Add(targetID string, s monitor.Sample, errText, addr string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.lastAddr == nil {
		u.lastAddr = map[string]string{}
	}
	x := Sample{ID: targetID, T: s.T, R: s.RTT}
	if s.Lost() {
		x.E = errText
	}
	if addr != "" && u.lastAddr[targetID] != addr {
		x.A = addr
		u.lastAddr[targetID] = addr
	}
	u.buf = append(u.buf, x)
	max := u.MaxBuffer
	if max <= 0 {
		max = 500000
	}
	if len(u.buf) > max {
		drop := len(u.buf) - max
		u.buf = append(u.buf[:0:0], u.buf[drop:]...)
	}
}

// Status returnerar anslutningsstatus.
func (u *Uploader) Status() Status {
	u.mu.Lock()
	defer u.mu.Unlock()
	st := u.status
	st.Enabled = true
	st.Buffered = len(u.buf)
	return st
}

// Run skickar rapporter tills ctx avbryts. Vid fel görs nya försök med ökande väntetid.
func (u *Uploader) Run(ctx context.Context) {
	backoff := time.Duration(0)
	for {
		wait := u.interval()
		if backoff > 0 {
			wait = backoff
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		err := u.SendAll(ctx)
		switch {
		case err == nil:
			backoff = 0
		case errors.Is(err, ErrUnauthorized):
			backoff = 5 * time.Minute
		case backoff == 0:
			backoff = 5 * time.Second
		default:
			backoff *= 2
			if backoff > time.Minute {
				backoff = time.Minute
			}
		}
	}
}

func (u *Uploader) interval() time.Duration {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.reportSec <= 0 {
		return 5 * time.Second
	}
	return time.Duration(u.reportSec) * time.Second
}

// SendAll skickar allt som ligger i kö (i omgångar om det är mycket).
func (u *Uploader) SendAll(ctx context.Context) error {
	for i := 0; i < 50; i++ {
		more, err := u.Send(ctx)
		if err != nil || !more {
			return err
		}
	}
	return nil
}

const maxBatch = 20000

// Send skickar en rapport. more = true om det finns fler mätpunkter kvar i kö.
func (u *Uploader) Send(ctx context.Context) (more bool, err error) {
	u.mu.Lock()
	n := len(u.buf)
	if n > maxBatch {
		n = maxBatch
	}
	batch := append([]Sample(nil), u.buf[:n]...)
	rep := Report{Agent: u.Info, ConfigVersion: u.configVersion, Samples: batch}
	needTargets := u.needTargets
	u.mu.Unlock()

	if u.Host != nil {
		rep.Host = u.Host()
	}
	var hash string
	if u.LocalTargets != nil {
		ts := u.LocalTargets()
		hash = targetsHash(ts)
		rep.LocalTargetsHash = hash
		if needTargets || hash != u.sentHash {
			rep.LocalTargets = ts
			if rep.LocalTargets == nil {
				rep.LocalTargets = []monitor.Target{}
			}
		}
	}

	resp, err := u.post(ctx, rep)
	u.mu.Lock()
	defer u.mu.Unlock()
	if err != nil {
		u.status.Connected = false
		u.status.LastError = err.Error()
		return false, err
	}
	// ta bort det som skickats (nya mätpunkter kan ha lagts till under tiden)
	u.buf = append(u.buf[:0:0], u.buf[n:]...)
	u.status.Connected = true
	u.status.LastOK = time.Now().UnixMilli()
	u.status.LastError = ""
	u.status.AgentName, u.status.Customer, u.status.Site = resp.AgentName, resp.Customer, resp.Site
	if rep.LocalTargets != nil {
		u.sentHash = hash
	}
	u.needTargets = resp.NeedLocalTargets
	if resp.Update != nil && u.OnUpdate != nil {
		upd := *resp.Update
		go u.OnUpdate(upd)
	}
	if resp.Config != nil {
		u.configVersion = resp.ConfigVersion
		if resp.Config.ReportSec > 0 {
			u.reportSec = resp.Config.ReportSec
		}
		if u.OnConfig != nil {
			cfg := *resp.Config
			go u.OnConfig(cfg)
		}
	}
	return len(u.buf) > 0 && n == maxBatch, nil
}

func (u *Uploader) post(ctx context.Context, rep Report) (*Response, error) {
	endpoint, err := ReportURL(u.ServerURL)
	if err != nil {
		return nil, err
	}
	var body bytes.Buffer
	gz := gzip.NewWriter(&body)
	if err := json.NewEncoder(gz).Encode(rep); err != nil {
		return nil, err
	}
	_ = gz.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+u.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("User-Agent", "PingVakt/"+u.Info.Version+" ("+u.Info.Kind+")")
	res, err := u.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("kunde inte nå servern: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return nil, ErrUnauthorized
	}
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return nil, fmt.Errorf("servern svarade %d: %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	var out Response
	if err := json.NewDecoder(io.LimitReader(res.Body, 50<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("ogiltigt svar från servern: %w", err)
	}
	return &out, nil
}

func (u *Uploader) httpClient() *http.Client {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.client == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		if u.Insecure {
			tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // uttryckligt val av användaren
		}
		u.client = &http.Client{Timeout: 60 * time.Second, Transport: tr}
	}
	return u.client
}

// ReportURL bygger rapport-URL:en från serverns basadress.
func ReportURL(server string) (string, error) {
	server = strings.TrimSpace(server)
	if server == "" {
		return "", errors.New("serveradress saknas")
	}
	if !strings.Contains(server, "://") {
		server = "https://" + server
	}
	p, err := url.Parse(server)
	if err != nil || p.Host == "" {
		return "", fmt.Errorf("ogiltig serveradress %q", server)
	}
	if p.Scheme != "https" && p.Scheme != "http" {
		return "", fmt.Errorf("serveradressen måste börja med https://")
	}
	p.Path = strings.TrimSuffix(p.Path, "/") + ReportPath
	return p.String(), nil
}

func targetsHash(ts []monitor.Target) string {
	cp := append([]monitor.Target(nil), ts...)
	sort.Slice(cp, func(i, j int) bool { return cp[i].ID < cp[j].ID })
	b, _ := json.Marshal(cp)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

// BaseURL returnerar serverns basadress (utan sökväg till rapporter).
func BaseURL(server string) (string, error) {
	r, err := ReportURL(server)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(r, ReportPath), nil
}

// HTTPClient returnerar klientens HTTP-klient (samma certifikatinställning som rapporterna).
func (u *Uploader) HTTPClient() *http.Client { return u.httpClient() }
