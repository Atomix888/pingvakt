package main

// Fjärråtkomst till enheter i kundens nät: webbsidor (HTTP/HTTPS) och SSH går genom kundens agent,
// så att man når dem från C4Pulse utan VPN. Inloggningsuppgifter per adress sparas krypterade.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/net/websocket"

	"pingvakt/core/agentproto"
	"pingvakt/core/i18n"
	"pingvakt/core/monitor"
	"pingvakt/core/tunnel"
)

// ---------- tunnlar till agenter ----------

var tunnels = struct {
	mu   sync.Mutex
	m    map[string]*tunnel.Mux
	wait map[string]chan struct{}
}{m: map[string]*tunnel.Mux{}, wait: map[string]chan struct{}{}}

// handleAgentTunnel tar emot agentens tunnel (WebSocket, agentens token).
func (s *Server) handleAgentTunnel(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	s.mu.RLock()
	agentID, ok := s.tokens[hashToken(strings.TrimPrefix(auth, "Bearer "))]
	kindOK := ok && s.agents[agentID] != nil && s.agents[agentID].Kind == agentproto.KindAgent
	s.mu.RUnlock()
	if !ok || !strings.HasPrefix(auth, "Bearer ") || !kindOK {
		httpErr(w, http.StatusUnauthorized, "unknown token")
		return
	}
	websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler: func(ws *websocket.Conn) {
			ws.PayloadType = websocket.BinaryFrame
			m := tunnel.New(tunnel.WS{C: ws}, nil)
			tunnels.mu.Lock()
			if old := tunnels.m[agentID]; old != nil {
				go old.Close()
			}
			tunnels.m[agentID] = m
			if ch := tunnels.wait[agentID]; ch != nil {
				close(ch)
				delete(tunnels.wait, agentID)
			}
			tunnels.mu.Unlock()
			_ = m.Run()
			tunnels.mu.Lock()
			if tunnels.m[agentID] == m {
				delete(tunnels.m, agentID)
			}
			tunnels.mu.Unlock()
		},
	}.ServeHTTP(w, r)
}

// agentDial öppnar en TCP-anslutning till addr genom agenten. Finns ingen tunnel ber vi agenten
// koppla upp en (det tar högst en rapportperiod, normalt några sekunder).
func (s *Server) agentDial(ctx context.Context, agentID, addr string) (net.Conn, error) {
	for attempt := 0; attempt < 2; attempt++ {
		tunnels.mu.Lock()
		m := tunnels.m[agentID]
		var ch chan struct{}
		if m == nil {
			ch = tunnels.wait[agentID]
			if ch == nil {
				ch = make(chan struct{})
				tunnels.wait[agentID] = ch
				traces.mu.Lock()
				traces.queue[agentID] = append(traces.queue[agentID], agentproto.Command{ID: monitor.NewID(), Kind: agentproto.CmdTunnel})
				traces.mu.Unlock()
			}
		}
		tunnels.mu.Unlock()
		if m == nil {
			wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			select {
			case <-ch:
				cancel()
				continue
			case <-wctx.Done():
				cancel()
				tunnels.mu.Lock()
				if tunnels.wait[agentID] == ch {
					delete(tunnels.wait, agentID)
				}
				tunnels.mu.Unlock()
				return nil, errors.New(i18n.T("the agent did not open the tunnel – it may be an older version or have remote access turned off"))
			}
		}
		c, err := m.Open(ctx, addr)
		if err != nil {
			select {
			case <-m.Done(): // tunneln gick ner – försök med en ny
				continue
			default:
			}
			return nil, err
		}
		return c, nil
	}
	return nil, errors.New(i18n.T("could not reach the agent"))
}

// remoteAgentFor väljer agent för att nå en adress: nätets skanningsagent, annars en annan
// agent hos kunden som är online (helst på samma plats).
func (s *Server) remoteAgentFor(sn *Subnet) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ok := func(a *Agent) bool {
		return a != nil && a.Kind == agentproto.KindAgent && s.agentOnline(a) && s.customerOfAgent(a) == sn.CustomerID && !strings.HasPrefix(a.OS, "android")
	}
	if a := s.agents[sn.ScanAgent]; ok(a) {
		return a.ID, nil
	}
	best := ""
	for _, a := range s.agents {
		if !ok(a) {
			continue
		}
		if a.SiteID == sn.SiteID && sn.SiteID != "" {
			return a.ID, nil
		}
		if best == "" {
			best = a.ID
		}
	}
	if best == "" {
		return "", errors.New(i18n.T("no agent at the customer is online"))
	}
	return best, nil
}

// ---------- krypterade lösenord ----------

var vaultOnce sync.Once
var vaultAEAD cipher.AEAD
var vaultErr error

// vault ger krypteringen för lösenord. Nyckeln tas från PINGVAKT_SECRET_KEY (base64, 32 byte)
// eller från filen secret.key i datamappen, som skapas första gången.
func (s *Server) vault() (cipher.AEAD, error) {
	vaultOnce.Do(func() {
		var key []byte
		if v := os.Getenv("PINGVAKT_SECRET_KEY"); v != "" {
			key, vaultErr = base64.StdEncoding.DecodeString(strings.TrimSpace(v))
			if vaultErr == nil && len(key) != 32 {
				vaultErr = errors.New("PINGVAKT_SECRET_KEY must be 32 bytes in base64")
			}
		} else {
			p := filepath.Join(s.dataDir, "secret.key")
			b, err := os.ReadFile(p)
			switch {
			case err == nil:
				key, vaultErr = base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
			case os.IsNotExist(err):
				key = make([]byte, 32)
				if _, vaultErr = rand.Read(key); vaultErr == nil {
					vaultErr = os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600)
					if vaultErr == nil {
						log.Printf("created %s – keep it with the database backups, saved passwords cannot be read without it", p)
					}
				}
			default:
				vaultErr = err
			}
		}
		if vaultErr != nil {
			return
		}
		blk, err := aes.NewCipher(key)
		if err != nil {
			vaultErr = err
			return
		}
		vaultAEAD, vaultErr = cipher.NewGCM(blk)
	})
	return vaultAEAD, vaultErr
}

func (s *Server) seal(plain string) (string, error) {
	g, err := s.vault()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return "v1:" + base64.StdEncoding.EncodeToString(g.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func (s *Server) unseal(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	g, err := s.vault()
	if err != nil {
		return "", err
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(enc, "v1:"))
	if err != nil || len(b) < g.NonceSize() {
		return "", errors.New(i18n.T("the saved password cannot be read"))
	}
	p, err := g.Open(nil, b[:g.NonceSize()], b[g.NonceSize():], nil)
	if err != nil {
		return "", errors.New(i18n.T("the saved password cannot be read (was secret.key replaced?)"))
	}
	return string(p), nil
}

// addrFor ger en kopia av adressen (eller en tom) och subnätet.
func addrFor(subnetID, ip string) (IPAddr, bool) {
	ipam.mu.Lock()
	defer ipam.mu.Unlock()
	if a := ipam.addrs[subnetID][ip]; a != nil {
		return *a, true
	}
	return IPAddr{IP: ip}, false
}

func (s *Server) handleAddrSecret(w http.ResponseWriter, r *http.Request) {
	sn, ok := s.subnetVisible(r, r.PathValue("id"))
	if !ok {
		httpErr(w, 404, "the subnet does not exist")
		return
	}
	a, _ := addrFor(sn.ID, r.PathValue("ip"))
	pw, err := s.unseal(a.Secret)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.audit(r, "ipam", sn.ID, i18n.T("showed the password for ")+a.IP)
	writeJSON(w, map[string]string{"user": a.User, "password": pw})
}

// ---------- webbsidor via agenten ----------

type remoteSess struct {
	id, code  string
	codeExp   time.Time
	agentID   string
	target    *url.URL
	label     string
	user      string
	created   time.Time
	last      time.Time
	transport *http.Transport
}

var remotes = struct {
	mu sync.Mutex
	m  map[string]*remoteSess
}{m: map[string]*remoteSess{}}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// remoteBase är adressen där fjärrsidorna öppnas (egen port = eget ursprung, så att enhetens
// sidor inte kan läsa C4Pulses inloggning).
func (s *Server) remoteBase(r *http.Request) string {
	s.mu.RLock()
	base := strings.TrimRight(strings.TrimSpace(s.settings.RemoteURL), "/")
	s.mu.RUnlock()
	if base != "" {
		return base
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	scheme := "http"
	if r.TLS != nil || (trustProxy && r.Header.Get("X-Forwarded-Proto") == "https") {
		scheme = "https"
	}
	if scheme == "https" {
		return "https://" + host + ":8443"
	}
	return "http://" + host + ":8081"
}

func (s *Server) handleRemoteWeb(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SubnetID string `json:"subnetId"`
		IP       string `json:"ip"`
		Scheme   string `json:"scheme"`
		Port     int    `json:"port"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	sn, ok := s.subnetVisible(r, in.SubnetID)
	if !ok {
		httpErr(w, 404, "the subnet does not exist")
		return
	}
	ip := net.ParseIP(in.IP).To4()
	n, _ := parseSubnet(sn.CIDR)
	if ip == nil || n == nil || !n.Contains(ip) {
		httpErr(w, 400, "invalid address")
		return
	}
	if in.Scheme != "http" && in.Scheme != "https" {
		httpErr(w, 400, "invalid address")
		return
	}
	if in.Port <= 0 || in.Port > 65535 {
		in.Port = map[string]int{"http": 80, "https": 443}[in.Scheme]
	}
	agentID, err := s.remoteAgentFor(sn)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	target := &url.URL{Scheme: in.Scheme, Host: net.JoinHostPort(ip.String(), strconv.Itoa(in.Port))}
	sess := &remoteSess{id: randHex(24), code: randHex(16), codeExp: time.Now().Add(2 * time.Minute), agentID: agentID, target: target,
		label: target.String(), user: userFrom(r).Email, created: time.Now(), last: time.Now()}
	sess.transport = &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			remotes.mu.Lock()
			addr := sess.target.Host
			remotes.mu.Unlock()
			return s.agentDial(ctx, sess.agentID, addr)
		},
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // enheter har nästan alltid egna certifikat
		MaxIdleConnsPerHost:   6,
		IdleConnTimeout:       60 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		TLSHandshakeTimeout:   20 * time.Second,
	}
	remotes.mu.Lock()
	remotes.m[sess.id] = sess
	remotes.mu.Unlock()
	s.audit(r, "ipam", sn.ID, i18n.T("opened the web page ")+target.String())
	writeJSON(w, map[string]string{"url": s.remoteBase(r) + "/__pingvakt/open?c=" + sess.code})
}

// pruneRemotes tar bort fjärrsessioner som inte använts på en timme (eller är äldre än 12 timmar).
func pruneRemotes() {
	remotes.mu.Lock()
	defer remotes.mu.Unlock()
	for id, x := range remotes.m {
		if time.Since(x.last) > time.Hour || time.Since(x.created) > 12*time.Hour {
			x.transport.CloseIdleConnections()
			delete(remotes.m, id)
		}
	}
}

const remoteCookie = "pv_remote"

func remotePage(w http.ResponseWriter, code int, title, text string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>C4Pulse</title>
<body style="font:15px system-ui,sans-serif;background:#0f1216;color:#e6e9ee;display:grid;place-items:center;min-height:90vh;margin:0;padding:16px">
<div style="max-width:520px"><h2 style="margin:0 0 8px">%s</h2><p style="color:#8b95a3;line-height:1.5">%s</p></div></body>`, htmlEsc(title), htmlEsc(text))
}

func htmlEsc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// RemoteHandler serverar fjärrsidorna på den egna porten.
func (s *Server) RemoteHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secure := r.TLS != nil || (trustProxy && r.Header.Get("X-Forwarded-Proto") == "https")
		if r.URL.Path == "/__pingvakt/open" {
			code := r.URL.Query().Get("c")
			remotes.mu.Lock()
			var sess *remoteSess
			for _, x := range remotes.m {
				if code != "" && x.code == code && time.Now().Before(x.codeExp) {
					sess = x
					x.code = "" // engångskod
				}
			}
			remotes.mu.Unlock()
			if sess == nil {
				remotePage(w, 403, i18n.T("The link has expired"), i18n.T("Open the device again from IP addresses in C4Pulse."))
				return
			}
			http.SetCookie(w, &http.Cookie{Name: remoteCookie, Value: sess.id, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: 12 * 3600})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		c, err := r.Cookie(remoteCookie)
		var sess *remoteSess
		if err == nil {
			remotes.mu.Lock()
			sess = remotes.m[c.Value]
			if sess != nil {
				sess.last = time.Now()
			}
			remotes.mu.Unlock()
		}
		if sess == nil {
			remotePage(w, 403, i18n.T("No remote session"), i18n.T("Open the device from IP addresses in C4Pulse. Sessions end after an hour without use."))
			return
		}
		s.proxyRemote(w, r, sess)
	})
}

func (s *Server) proxyRemote(w http.ResponseWriter, r *http.Request, sess *remoteSess) {
	remotes.mu.Lock()
	target := *sess.target
	remotes.mu.Unlock()
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&target)
			pr.Out.Host = target.Host
			// skicka inte C4Pulses egna kakor till enheten (kakor skiljer inte på portar,
			// så webbläsaren skickar även inloggningen till C4Pulse hit)
			var keep []string
			for _, ck := range pr.In.Cookies() {
				if ck.Name != remoteCookie && ck.Name != sessionCookie {
					keep = append(keep, ck.Name+"="+ck.Value)
				}
			}
			pr.Out.Header.Del("Cookie")
			if len(keep) > 0 {
				pr.Out.Header.Set("Cookie", strings.Join(keep, "; "))
			}
			pr.Out.Header.Del("X-Forwarded-For")
			pr.Out.Header.Del("X-Forwarded-Host")
			pr.Out.Header.Del("X-Forwarded-Proto")
			if o := pr.Out.Header.Get("Origin"); o != "" {
				pr.Out.Header.Set("Origin", target.Scheme+"://"+target.Host)
			}
			if ref := pr.Out.Header.Get("Referer"); ref != "" {
				if u, err := url.Parse(ref); err == nil {
					u.Scheme, u.Host = target.Scheme, target.Host
					pr.Out.Header.Set("Referer", u.String())
				}
			}
		},
		Transport:     sess.transport,
		FlushInterval: -1,
		ModifyResponse: func(res *http.Response) error {
			// omdirigeringar till enheten själv görs relativa (och byte mellan http och https följs)
			if loc := res.Header.Get("Location"); loc != "" {
				if u, err := url.Parse(loc); err == nil && u.IsAbs() && u.Hostname() == target.Hostname() {
					if u.Scheme != target.Scheme || u.Host != target.Host {
						remotes.mu.Lock()
						sess.target = &url.URL{Scheme: u.Scheme, Host: u.Host}
						if u.Port() == "" {
							sess.target.Host = net.JoinHostPort(u.Hostname(), map[string]string{"http": "80", "https": "443"}[u.Scheme])
						}
						remotes.mu.Unlock()
					}
					rel := u.EscapedPath()
					if rel == "" {
						rel = "/"
					}
					if u.RawQuery != "" {
						rel += "?" + u.RawQuery
					}
					res.Header.Set("Location", rel)
				}
			}
			if cks := res.Header.Values("Set-Cookie"); len(cks) > 0 {
				res.Header.Del("Set-Cookie")
				for _, ck := range cks {
					// enheten får inte skriva över C4Pulses kakor
					name, _, _ := strings.Cut(strings.TrimSpace(ck), "=")
					if name == sessionCookie || name == remoteCookie {
						continue
					}
					parts := strings.Split(ck, ";")
					out := parts[:1]
					for _, p := range parts[1:] {
						if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(p)), "domain=") {
							out = append(out, p)
						}
					}
					res.Header.Add("Set-Cookie", strings.Join(out, ";"))
				}
			}
			res.Header.Del("Strict-Transport-Security")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			remotePage(w, 502, i18n.T("Could not reach the device"), fmt.Sprintf(i18n.T("%s via the agent: %v"), target.String(), err))
		},
	}
	rp.ServeHTTP(w, r)
}

// ---------- SSH i webbläsaren ----------

type sshHello struct {
	User     string `json:"user"`
	Password string `json:"password"`
	Save     bool   `json:"save"`
	Cols     int    `json:"cols"`
	Rows     int    `json:"rows"`
	Trust    string `json:"trust"` // godkänd ny värdnyckel (fingeravtryck)
}

func wsSendJSON(ws *websocket.Conn, v any) {
	b, _ := json.Marshal(v)
	_ = websocket.Message.Send(ws, string(b))
}

func (s *Server) handleSSH(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sn, ok := s.subnetVisible(r, q.Get("subnet"))
	if !ok {
		httpErr(w, 404, "the subnet does not exist")
		return
	}
	ip := net.ParseIP(q.Get("ip")).To4()
	n, _ := parseSubnet(sn.CIDR)
	if ip == nil || n == nil || !n.Contains(ip) {
		httpErr(w, 400, "invalid address")
		return
	}
	port, _ := strconv.Atoi(q.Get("port"))
	if port <= 0 || port > 65535 {
		port = 22
	}
	host := r.Host
	who := userFrom(r).Email
	websocket.Server{
		// bara från C4Pulses egen sida
		Handshake: func(cfg *websocket.Config, req *http.Request) error {
			o, err := url.Parse(req.Header.Get("Origin"))
			if err != nil || o.Host != host {
				return errors.New("origin")
			}
			return nil
		},
		Handler: func(ws *websocket.Conn) {
			defer ws.Close()
			s.runSSH(ws, r, sn, ip.String(), port, who)
		},
	}.ServeHTTP(w, r)
}

func (s *Server) runSSH(ws *websocket.Conn, r *http.Request, sn *Subnet, ip string, port int, who string) {
	var hello sshHello
	var first string
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Minute))
	if err := websocket.Message.Receive(ws, &first); err != nil || json.Unmarshal([]byte(first), &hello) != nil {
		return
	}
	_ = ws.SetReadDeadline(time.Time{})
	fail := func(msg string) { wsSendJSON(ws, map[string]string{"t": "err", "m": msg}) }
	a, _ := addrFor(sn.ID, ip)
	user, pw := strings.TrimSpace(hello.User), hello.Password
	if user == "" {
		user = a.User
	}
	if pw == "" && a.Secret != "" && (hello.User == "" || hello.User == a.User) {
		var err error
		if pw, err = s.unseal(a.Secret); err != nil {
			fail(err.Error())
			return
		}
	}
	if user == "" {
		wsSendJSON(ws, map[string]string{"t": "login"})
		return
	}
	agentID, err := s.remoteAgentFor(sn)
	if err != nil {
		fail(err.Error())
		return
	}
	wsSendJSON(ws, map[string]string{"t": "status", "m": i18n.T("Connecting through the agent…")})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	conn, err := s.agentDial(ctx, agentID, net.JoinHostPort(ip, strconv.Itoa(port)))
	cancel()
	if err != nil {
		fail(err.Error())
		return
	}
	defer conn.Close()
	var newKey string
	cfg := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{ssh.Password(pw), ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
			ans := make([]string, len(qs))
			for i := range ans {
				ans[i] = pw
			}
			return ans, nil
		})},
		// värdnyckeln sparas första gången och kontrolleras sedan (som known_hosts)
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fp := ssh.FingerprintSHA256(key)
			if a.HostKey != "" && a.HostKey != fp && hello.Trust != fp {
				wsSendJSON(ws, map[string]string{"t": "hostkey", "fp": fp, "old": a.HostKey})
				return errors.New("host key changed")
			}
			newKey = fp
			return nil
		},
		Timeout: 20 * time.Second,
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	sc, chans, reqs, err := ssh.NewClientConn(conn, net.JoinHostPort(ip, strconv.Itoa(port)), cfg)
	if err != nil {
		if strings.Contains(err.Error(), "host key changed") {
			return
		}
		if strings.Contains(err.Error(), "unable to authenticate") {
			wsSendJSON(ws, map[string]string{"t": "login", "m": i18n.T("Wrong user name or password.")})
			return
		}
		fail(err.Error())
		return
	}
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(sc, chans, reqs)
	defer client.Close()
	// spara värdnyckel och (om man bad om det) inloggningen
	if newKey != "" || hello.Save {
		ipam.mu.Lock()
		x := ipam.addrs[sn.ID][ip]
		if x == nil && ipam.addrs[sn.ID] != nil {
			x = &IPAddr{IP: ip}
			ipam.addrs[sn.ID][ip] = x
		}
		if x != nil {
			if newKey != "" {
				x.HostKey = newKey
			}
			if hello.Save {
				x.User = user
				if enc, err := s.seal(pw); err == nil {
					x.Secret = enc
				}
			}
			markDirty(sn.ID, ip)
		}
		ipam.mu.Unlock()
		s.flushIPAM()
	}
	sess, err := client.NewSession()
	if err != nil {
		fail(err.Error())
		return
	}
	defer sess.Close()
	cols, rows := hello.Cols, hello.Rows
	if cols < 20 || cols > 500 {
		cols = 100
	}
	if rows < 5 || rows > 200 {
		rows = 30
	}
	if err := sess.RequestPty("xterm-256color", rows, cols, ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 115200, ssh.TTY_OP_OSPEED: 115200}); err != nil {
		fail(err.Error())
		return
	}
	stdin, _ := sess.StdinPipe()
	stdout, _ := sess.StdoutPipe()
	stderr, _ := sess.StderrPipe()
	if err := sess.Shell(); err != nil {
		fail(err.Error())
		return
	}
	s.auditAs(who, clientIP(r), "ipam", sn.ID, fmt.Sprintf(i18n.T("SSH to %s as %s"), ip, user))
	wsSendJSON(ws, map[string]string{"t": "ok", "fp": newKey})
	var wmu sync.Mutex
	pump := func(rd io.Reader) {
		buf := make([]byte, 16<<10)
		for {
			n, err := rd.Read(buf)
			if n > 0 {
				wmu.Lock()
				e := websocket.Message.Send(ws, buf[:n])
				wmu.Unlock()
				if e != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	go pump(stdout)
	go pump(stderr)
	done := make(chan struct{})
	go func() {
		_ = sess.Wait()
		close(done)
		wmu.Lock()
		wsSendJSON(ws, map[string]string{"t": "closed"})
		wmu.Unlock()
		ws.Close()
	}()
	for {
		var msg string
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			return
		}
		var in struct {
			D string `json:"d"`
			R []int  `json:"r"`
		}
		if json.Unmarshal([]byte(msg), &in) != nil {
			continue
		}
		if in.D != "" {
			if _, err := stdin.Write([]byte(in.D)); err != nil {
				return
			}
		}
		if len(in.R) == 2 && in.R[0] > 0 && in.R[1] > 0 {
			_ = sess.WindowChange(in.R[1], in.R[0])
		}
		select {
		case <-done:
			return
		default:
		}
	}
}
