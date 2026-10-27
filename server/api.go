package main

import (
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"pingvakt/core/agentproto"
	"pingvakt/core/alert"
	"pingvakt/core/hostinfo"
	"pingvakt/core/i18n"
	"pingvakt/core/monitor"
	"pingvakt/core/probe"
)

// ---------- JSON-hjälpare ----------

func jsonEnc(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = jsonEnc(w, v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = jsonEnc(w, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	if err := dec.Decode(v); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// Routes kopplar alla endpoints.
func (s *Server) Routes(web http.Handler) http.Handler {
	mux := http.NewServeMux()
	// inloggning
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/me", s.handleMe)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/me/password", need(false, s.handleChangePassword))
	mux.HandleFunc("POST /api/login/totp", s.handleLoginTOTP)
	mux.HandleFunc("POST /api/me/totp/setup", need(false, s.handleTOTPSetup))
	mux.HandleFunc("POST /api/me/totp/enable", need(false, s.handleTOTPEnable))
	mux.HandleFunc("POST /api/me/totp/disable", need(false, s.handleTOTPDisable))
	mux.HandleFunc("POST /api/users/{id}/totp/reset", need(true, s.handleTOTPReset))
	mux.HandleFunc("GET /api/audit", need(true, s.handleAudit))
	// data
	mux.HandleFunc("GET /api/state", need(false, s.handleState))
	mux.HandleFunc("GET /api/live", need(false, s.handleLive))
	mux.HandleFunc("GET /api/targets/{id}/history", need(false, s.handleHistory))
	mux.HandleFunc("GET /api/targets/{id}/export.csv", need(false, s.handleExport))
	mux.HandleFunc("GET /api/events", need(false, s.handleEvents))
	mux.HandleFunc("GET /api/agents/{id}/metrics", need(false, s.handleAgentMetrics))
	mux.HandleFunc("GET /i18n.js", s.handleI18n)
	mux.HandleFunc("GET /api/auth/providers", s.handleAuthProviders)
	mux.HandleFunc("GET /auth/{provider}/login", s.handleOIDCStart)
	mux.HandleFunc("GET /auth/{provider}/callback", s.handleOIDCCallback)
	mux.HandleFunc("GET /api/oncall", need(true, s.handleOnCallList))
	mux.HandleFunc("POST /api/oncall", need(true, s.handleOnCallSave))
	mux.HandleFunc("PUT /api/oncall/{id}", need(true, s.handleOnCallSave))
	mux.HandleFunc("DELETE /api/oncall/{id}", need(true, s.handleOnCallDelete))
	mux.HandleFunc("GET /api/me/notify", need(false, s.handleMyNotifyGet))
	mux.HandleFunc("PUT /api/me/notify", need(false, s.handleMyNotify))
	mux.HandleFunc("POST /api/me/notify/test", need(false, s.handleNotifyTest))
	mux.HandleFunc("POST /api/push/subscribe", need(false, s.handlePushSubscribe))
	mux.HandleFunc("POST /api/push/unsubscribe", need(false, s.handlePushUnsubscribe))
	mux.HandleFunc("GET /api/agents/{id}/scan", need(true, s.handleScanInfo))
	mux.HandleFunc("POST /api/agents/{id}/scan", need(true, s.handleStartScan))
	mux.HandleFunc("GET /api/snmp", need(false, s.handleSNMPList))
	mux.HandleFunc("POST /api/snmp", need(true, s.handleSNMPSave))
	mux.HandleFunc("POST /api/snmp/test", need(true, s.handleSNMPTest))
	mux.HandleFunc("GET /api/snmp/{id}", need(false, s.handleSNMPGet))
	mux.HandleFunc("PUT /api/snmp/{id}", need(true, s.handleSNMPSave))
	mux.HandleFunc("DELETE /api/snmp/{id}", need(true, s.handleSNMPDelete))
	mux.HandleFunc("GET /api/snmp/{id}/history", need(false, s.handleSNMPHistory))
	mux.HandleFunc("GET /api/agents/{id}/speed", need(false, s.handleSpeedGet))
	mux.HandleFunc("PUT /api/agents/{id}/speed", need(true, s.handleSpeedConfig))
	mux.HandleFunc("POST /api/agents/{id}/speed/run", need(false, s.handleSpeedRun))
	mux.HandleFunc("GET "+agentproto.SpeedPath+"/down", s.handleSpeedDown)
	mux.HandleFunc("POST "+agentproto.SpeedPath+"/up", s.handleSpeedUp)
	mux.HandleFunc("GET /api/agents/{id}/survey", need(false, s.handleSurveyGet))
	mux.HandleFunc("PUT /api/agents/{id}/survey", need(true, s.handleSurveyConfig))
	mux.HandleFunc("POST /api/agents/{id}/survey/run", need(false, s.handleSurveyRun))
	mux.HandleFunc("GET /api/integrations", need(false, s.handleIntegrations))
	mux.HandleFunc("POST /api/integrations", need(true, s.handleIntegrationSave))
	mux.HandleFunc("POST /api/integrations/test", need(true, s.handleIntegrationTest))
	mux.HandleFunc("GET /api/integrations/{id}", need(false, s.handleIntegrationGet))
	mux.HandleFunc("PUT /api/integrations/{id}", need(true, s.handleIntegrationSave))
	mux.HandleFunc("DELETE /api/integrations/{id}", need(true, s.handleIntegrationDelete))
	mux.HandleFunc("GET /api/templates", need(false, s.handleTemplates))
	mux.HandleFunc("POST /api/templates", need(true, s.handleSaveTemplate))
	mux.HandleFunc("PUT /api/templates/{id}", need(true, s.handleSaveTemplate))
	mux.HandleFunc("DELETE /api/templates/{id}", need(true, s.handleDeleteTemplate))
	mux.HandleFunc("POST /api/templates/{id}/apply", need(true, s.handleApplyTemplate))
	mux.HandleFunc("PUT /api/me/lang", need(false, s.handleMyLang))
	mux.HandleFunc("POST /api/targets/{id}/trace", need(false, s.handleStartTrace))
	mux.HandleFunc("GET /api/targets/{id}/traces", need(false, s.handleTraces))
	mux.HandleFunc("POST "+agentproto.ResultPath, s.handleCommandResult)
	mux.HandleFunc("GET /api/customers/{id}/report.pdf", need(false, s.handleCustomerReport))
	mux.HandleFunc("POST /api/customers/{id}/report/send", need(true, s.handleSendReport))
	mux.HandleFunc("GET /api/maintenance", need(false, s.handleListMaintenance))
	mux.HandleFunc("POST /api/maintenance", need(true, s.handleSaveMaintenance))
	mux.HandleFunc("PUT /api/maintenance/{id}", need(true, s.handleSaveMaintenance))
	mux.HandleFunc("DELETE /api/maintenance/{id}", need(true, s.handleDeleteMaintenance))
	mux.HandleFunc("GET /api/alerts/active", need(false, s.handleActiveAlerts))
	mux.HandleFunc("POST /api/alerts/ack", need(true, s.handleAck))
	mux.HandleFunc("GET /api/backups", need(true, s.handleListBackups))
	mux.HandleFunc("GET /api/backup", need(true, s.handleDownloadBackup))
	// administration
	mux.HandleFunc("POST /api/customers", need(true, s.handleSaveCustomer))
	mux.HandleFunc("PUT /api/customers/{id}", need(true, s.handleSaveCustomer))
	mux.HandleFunc("DELETE /api/customers/{id}", need(true, s.handleDeleteCustomer))
	mux.HandleFunc("POST /api/sites", need(true, s.handleSaveSite))
	mux.HandleFunc("PUT /api/sites/{id}", need(true, s.handleSaveSite))
	mux.HandleFunc("DELETE /api/sites/{id}", need(true, s.handleDeleteSite))
	mux.HandleFunc("POST /api/agents", need(true, s.handleCreateAgent))
	mux.HandleFunc("PUT /api/agents/{id}", need(true, s.handleUpdateAgent))
	mux.HandleFunc("DELETE /api/agents/{id}", need(true, s.handleDeleteAgent))
	mux.HandleFunc("POST /api/agents/{id}/token", need(true, s.handleNewToken))
	mux.HandleFunc("POST /api/targets", need(true, s.handleAddTargets))
	mux.HandleFunc("PUT /api/targets/{id}", need(true, s.handleUpdateTarget))
	mux.HandleFunc("DELETE /api/targets/{id}", need(true, s.handleDeleteTarget))
	mux.HandleFunc("POST /api/targets/order", need(true, s.handleOrder))
	mux.HandleFunc("POST /api/rules", need(true, s.handleSaveRule))
	mux.HandleFunc("DELETE /api/rules/{id}", need(true, s.handleDeleteRule))
	mux.HandleFunc("POST /api/rules/test", need(true, s.handleTestRule))
	mux.HandleFunc("POST /api/rules/{id}/assign", need(true, s.handleAssignRule))
	mux.HandleFunc("GET /api/users", need(true, s.handleUsers))
	mux.HandleFunc("POST /api/users", need(true, s.handleSaveUser))
	mux.HandleFunc("PUT /api/users/{id}", need(true, s.handleSaveUser))
	mux.HandleFunc("DELETE /api/users/{id}", need(true, s.handleDeleteUser))
	mux.HandleFunc("GET /api/settings", need(true, s.handleGetSettings))
	mux.HandleFunc("PUT /api/settings", need(true, s.handleSaveSettings))
	mux.HandleFunc("POST /api/settings/test-email", need(true, s.handleTestEmail))
	// agenter
	mux.HandleFunc("POST "+agentproto.ReportPath, s.handleReport)
	mux.HandleFunc("GET /install.sh", s.handleInstallSh)
	mux.HandleFunc("GET /install.ps1", s.handleInstallPs1)
	mux.HandleFunc("GET /download/agent/{file}", s.handleDownload)
	mux.HandleFunc("GET /api/public/status/{key}", s.handlePublicStatus)
	mux.HandleFunc("POST /api/customers/{id}/status-key", need(true, s.handleStatusKey))
	mux.HandleFunc("GET /status/{key}", s.serveStatusPage(web))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.Handle("/", web)
	return securityHeaders(s.withUser(mux))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// ---------- tillstånd ----------

type stateOut struct {
	Me        *User                    `json:"me"`
	Version   string                   `json:"version"`
	Customers []Customer               `json:"customers"`
	Sites     []Site                   `json:"sites"`
	Agents    []Agent                  `json:"agents"`
	Targets   []Target                 `json:"targets"`
	Rules     []Rule                   `json:"rules"`
	Stats     map[string]monitor.Stats `json:"stats"`
	Live      map[string]liveAgent     `json:"live"`
	Active    map[string]int64         `json:"active"`
	Monitor   monitor.Settings         `json:"monitor"`
	ReportSec int                      `json:"reportSec"`
	ServerURL string                   `json:"serverUrl"`
	Server    *hostinfo.Metrics        `json:"server,omitempty"`
	Maint     map[string]string        `json:"maint"`
	Acks      map[string]Ack           `json:"acks"`
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	vis := visibleCustomers(u)
	ok := func(c string) bool { return vis == nil || vis[c] }
	snap := s.snapshot(vis)
	s.mu.RLock()
	out := stateOut{Me: u, Version: s.version, Customers: []Customer{}, Sites: []Site{}, Agents: []Agent{}, Targets: []Target{},
		Rules: []Rule{}, Stats: snap.Stats, Live: snap.Agents, Monitor: s.settings.Monitor, ReportSec: s.settings.ReportSec,
		ServerURL: s.publicURL(r)}
	for _, c := range s.customers {
		if ok(c.ID) {
			out.Customers = append(out.Customers, c)
		}
	}
	for _, x := range s.sites {
		if ok(x.CustomerID) {
			out.Sites = append(out.Sites, x)
		}
	}
	for _, a := range s.agents {
		if ok(s.customerOfAgent(a)) {
			out.Agents = append(out.Agents, *a)
		}
	}
	for _, t := range s.targets {
		if ok(s.customerOfTarget(t)) {
			out.Targets = append(out.Targets, *t)
		}
	}
	for _, rl := range s.rules {
		if vis == nil || (rl.CustomerID != "" && vis[rl.CustomerID]) {
			if u.Role != "admin" {
				rl.EmailTo, rl.HookURL = "", ""
			}
			out.Rules = append(out.Rules, rl)
		}
	}
	s.mu.RUnlock()
	sort.Slice(out.Customers, func(i, j int) bool {
		return strings.ToLower(out.Customers[i].Name) < strings.ToLower(out.Customers[j].Name)
	})
	sort.Slice(out.Sites, func(i, j int) bool { return strings.ToLower(out.Sites[i].Name) < strings.ToLower(out.Sites[j].Name) })
	sort.Slice(out.Agents, func(i, j int) bool { return strings.ToLower(out.Agents[i].Name) < strings.ToLower(out.Agents[j].Name) })
	sort.Slice(out.Targets, func(i, j int) bool {
		if out.Targets[i].Sort != out.Targets[j].Sort {
			return out.Targets[i].Sort < out.Targets[j].Sort
		}
		return out.Targets[i].ID < out.Targets[j].ID
	})
	act := s.activeAlerts()
	out.Active = map[string]int64{}
	for k, v := range act {
		if i := strings.IndexByte(k, '|'); i >= 0 {
			_, isT := snap.Stats[k[i+1:]]
			_, isA := snap.Agents[k[i+1:]]
			if isT || isA {
				out.Active[k] = v
			}
		}
	}
	if u.Role == "admin" {
		out.Server = s.hostLatest(ServerHostID)
	}
	out.Maint = map[string]string{}
	for id, note := range s.inMaintenance() {
		if _, ok := snap.Stats[id]; ok {
			out.Maint[id] = note
		} else if _, ok := snap.Agents[id]; ok {
			out.Maint[id] = note
		}
	}
	out.Acks = map[string]Ack{}
	s.ops.mu.Lock()
	for k := range out.Active {
		if a, ok := s.ops.acks[k]; ok {
			out.Acks[k] = a
		}
	}
	s.ops.mu.Unlock()
	writeJSON(w, out)
}

func (s *Server) publicURL(r *http.Request) string {
	s.mu.RLock()
	p := strings.TrimSuffix(s.settings.PublicURL, "/")
	s.mu.RUnlock()
	if p != "" {
		return p
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	host := r.Host
	if xf := r.Header.Get("X-Forwarded-Host"); xf != "" && trustProxy {
		host = xf
	}
	return scheme + "://" + host
}

// handleLive skickar uppdateringar med Server-Sent Events.
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		httpErr(w, 500, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	sub := s.subscribe(visibleCustomers(userFrom(r)))
	defer s.unsubscribe(sub)
	_, _ = fmt.Fprint(w, "retry: 3000\n\n")
	fl.Flush()
	keep := time.NewTicker(25 * time.Second)
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-sub.ch:
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return
			}
			fl.Flush()
		case <-keep.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

func (s *Server) targetVisible(r *http.Request, id string) (*Target, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.targets[id]
	if !ok || !canSee(userFrom(r), s.customerOfTarget(t)) {
		return nil, false
	}
	cp := *t
	return &cp, true
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.targetVisible(r, id); !ok {
		httpErr(w, 404, "the target does not exist")
		return
	}
	to, _ := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	if to <= 0 {
		to = now()
	}
	if from <= 0 || from >= to {
		from = to - 3600000
	}
	s.mu.RLock()
	raw := s.settings.RawDays
	s.mu.RUnlock()
	h, err := s.db.History(id, from, to, raw)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, h)
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, ok := s.targetVisible(r, id)
	if !ok {
		httpErr(w, 404, "the target does not exist")
		return
	}
	to, _ := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	if to <= 0 {
		to = now()
	}
	if from <= 0 {
		from = to - 24*3600000
	}
	s.mu.RLock()
	raw := s.settings.RawDays
	s.mu.RUnlock()
	h, err := s.db.History(id, from, to, raw)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	name := t.Name
	if name == "" {
		name = t.Host
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="pingvakt-%s-%s.csv"`, safeFile(name), time.Now().Format("20060102-1504")))
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	lang := s.userLang(r)
	L := func(x string) string { return i18n.TL(lang, x) }
	cw := csv.NewWriter(w)
	if lang == "sv" {
		cw.Comma = ';' // svenskt Excel
	}
	num := func(v float64) string {
		f := strconv.FormatFloat(v, 'f', 3, 64)
		if lang == "sv" {
			f = strings.Replace(f, ".", ",", 1)
		}
		return f
	}
	if h.Mode == "raw" {
		_ = cw.Write([]string{L("Time"), L("Target"), L("Host"), L("Latency (ms)"), L("Lost")})
		for i, ts := range h.T {
			lat, lost := num(h.R[i]), "0"
			if h.R[i] < 0 {
				lat, lost = "", "1"
			}
			_ = cw.Write([]string{time.UnixMilli(ts).Format("2006-01-02 15:04:05.000"), name, t.Host, lat, lost})
		}
	} else {
		_ = cw.Write([]string{L("Time"), L("Target"), L("Host"), L("Avg (ms)"), "Min (ms)", "Max (ms)", L("Loss (%)"), L("Packet count")})
		f := func(p *float64) string {
			if p == nil {
				return ""
			}
			return num(*p)
		}
		for i, ts := range h.T {
			_ = cw.Write([]string{time.UnixMilli(ts).Format("2006-01-02 15:04"), name, t.Host, f(h.Avg[i]), f(h.Min[i]), f(h.Max[i]),
				num(h.Loss[i]), strconv.Itoa(h.N[i])})
		}
	}
	cw.Flush()
}

func safeFile(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`\/:*?"<>|;`, r) {
			return '_'
		}
		return r
	}, s)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	cust := r.URL.Query().Get("customer")
	if u.Role != "admin" {
		cust = u.CustomerID
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 2000 {
		limit = 300
	}
	ev, err := s.db.Events(cust, limit)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, ev)
}

// ---------- kunder och siter ----------

func (s *Server) handleSaveCustomer(w http.ResponseWriter, r *http.Request) {
	var c Customer
	if !readJSON(w, r, &c) {
		return
	}
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		httpErr(w, 400, "name missing")
		return
	}
	c.ReportTo = strings.TrimSpace(c.ReportTo)
	if c.ReportFreq != "weekly" && c.ReportFreq != "monthly" {
		c.ReportFreq = ""
	}
	if c.Lang != "" {
		c.Lang = i18n.Norm(c.Lang, "")
	}
	if id := r.PathValue("id"); id != "" {
		_, err := s.db.Exec(`UPDATE customers SET name=?, notes=?, report_to=?, report_freq=?, lang=? WHERE id=?`, c.Name, c.Notes, c.ReportTo, c.ReportFreq, c.Lang, id)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		c.ID = id
	} else {
		c.ID, c.Created = newID(), now()
		if _, err := s.db.Exec(`INSERT INTO customers(id,name,notes,created,report_to,report_freq,lang) VALUES(?,?,?,?,?,?,?)`, c.ID, c.Name, c.Notes, c.Created, c.ReportTo, c.ReportFreq, c.Lang); err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		// en första site skapas direkt
		_, _ = s.db.Exec(`INSERT INTO sites(id,customer_id,name,created) VALUES(?,?,?,?)`, newID(), c.ID, i18n.T("Head office"), now())
	}
	s.audit(r, "customer", c.ID, i18n.T("saved the customer ")+c.Name)
	s.afterChange(w, c)
}

func (s *Server) handleDeleteCustomer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ids := s.targetIDsWhere(func(t *Target, cust string) bool { return cust == id })
	for _, t := range ids {
		_ = s.db.DeleteTarget(t)
	}
	if _, err := s.db.Exec(`DELETE FROM customers WHERE id=?`, id); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	_, _ = s.db.Exec(`DELETE FROM alerts WHERE customer_id=?`, id)
	_, _ = s.db.Exec(`DELETE FROM users WHERE customer_id=? AND role='viewer'`, id)
	s.audit(r, "customer", id, i18n.T("deleted the customer ")+s.customers[id].Name)
	s.afterChange(w, map[string]bool{"ok": true})
}

func (s *Server) targetIDsWhere(f func(t *Target, cust string) bool) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for id, t := range s.targets {
		if f(t, s.customerOfTarget(t)) {
			out = append(out, id)
		}
	}
	return out
}

func (s *Server) handleSaveSite(w http.ResponseWriter, r *http.Request) {
	var x Site
	if !readJSON(w, r, &x) {
		return
	}
	x.Name = strings.TrimSpace(x.Name)
	if x.Name == "" {
		httpErr(w, 400, "name missing")
		return
	}
	if id := r.PathValue("id"); id != "" {
		if _, err := s.db.Exec(`UPDATE sites SET name=? WHERE id=?`, x.Name, id); err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		x.ID = id
	} else {
		s.mu.RLock()
		_, ok := s.customers[x.CustomerID]
		s.mu.RUnlock()
		if !ok {
			httpErr(w, 400, "unknown customer")
			return
		}
		x.ID, x.Created = newID(), now()
		if _, err := s.db.Exec(`INSERT INTO sites(id,customer_id,name,created) VALUES(?,?,?,?)`, x.ID, x.CustomerID, x.Name, x.Created); err != nil {
			httpErr(w, 500, err.Error())
			return
		}
	}
	s.audit(r, "site", x.ID, i18n.T("saved the site ")+x.Name)
	s.afterChange(w, x)
}

func (s *Server) handleDeleteSite(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.RLock()
	var tids []string
	for tid, t := range s.targets {
		if a, ok := s.agents[t.AgentID]; ok && a.SiteID == id {
			tids = append(tids, tid)
		}
	}
	s.mu.RUnlock()
	for _, t := range tids {
		_ = s.db.DeleteTarget(t)
	}
	if _, err := s.db.Exec(`DELETE FROM sites WHERE id=?`, id); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.audit(r, "site", id, i18n.T("deleted the site ")+s.sites[id].Name)
	s.afterChange(w, map[string]bool{"ok": true})
}

func (s *Server) afterChange(w http.ResponseWriter, v any) {
	if err := s.reload(); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, v)
}

// ---------- agenter ----------

type agentWithToken struct {
	Agent Agent  `json:"agent"`
	Token string `json:"token"`
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	var in struct{ SiteID, Name string }
	if !readJSON(w, r, &in) {
		return
	}
	s.mu.RLock()
	_, ok := s.sites[in.SiteID]
	s.mu.RUnlock()
	if !ok {
		httpErr(w, 400, "unknown site")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		in.Name = "Agent"
	}
	tok, h := newToken()
	a := Agent{ID: newID(), SiteID: in.SiteID, Name: in.Name, ConfigVersion: 1, Created: now()}
	if _, err := s.db.Exec(`INSERT INTO agents(id,site_id,name,token_hash,created) VALUES(?,?,?,?,?)`, a.ID, a.SiteID, a.Name, h, a.Created); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.audit(r, "agent", a.ID, i18n.T("created the agent ")+a.Name)
	s.afterChange(w, agentWithToken{a, tok})
}

func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	var in struct{ SiteID, Name string }
	if !readJSON(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	if _, err := s.db.Exec(`UPDATE agents SET name=?, site_id=COALESCE(NULLIF(?,''),site_id) WHERE id=?`, strings.TrimSpace(in.Name), in.SiteID, id); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.audit(r, "agent", id, i18n.T("changed the agent ")+in.Name)
	s.afterChange(w, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for _, t := range s.targetIDsWhere(func(t *Target, _ string) bool { return t.AgentID == id }) {
		_ = s.db.DeleteTarget(t)
	}
	if _, err := s.db.Exec(`DELETE FROM agents WHERE id=?`, id); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.audit(r, "agent", id, i18n.T("deleted the agent ")+s.agents[id].Name)
	s.afterChange(w, map[string]bool{"ok": true})
}

func (s *Server) handleNewToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tok, h := newToken()
	res, err := s.db.Exec(`UPDATE agents SET token_hash=? WHERE id=?`, h, id)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		httpErr(w, 404, "the agent does not exist")
		return
	}
	s.audit(r, "agent", id, i18n.T("created a new token"))
	s.afterChange(w, map[string]string{"token": tok})
}

// handleReport tar emot mätdata från en agent eller skrivbordsapp.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		httpErr(w, http.StatusUnauthorized, "token missing")
		return
	}
	h := hashToken(strings.TrimPrefix(auth, "Bearer "))
	s.mu.RLock()
	agentID, ok := s.tokens[h]
	s.mu.RUnlock()
	if !ok {
		httpErr(w, http.StatusUnauthorized, "unknown token")
		return
	}
	var body io.Reader = io.LimitReader(r.Body, 64<<20)
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(body)
		if err != nil {
			httpErr(w, 400, "invalid gzip")
			return
		}
		defer gz.Close()
		body = io.LimitReader(gz, 256<<20)
	}
	var rep agentproto.Report
	if err := json.NewDecoder(body).Decode(&rep); err != nil {
		httpErr(w, 400, "invalid report: "+err.Error())
		return
	}
	resp, err := s.Ingest(r.Context(), agentID, clientIP(r), rep)
	if err != nil {
		if errors.Is(err, errNotFound) {
			httpErr(w, http.StatusUnauthorized, "the agent does not exist")
			return
		}
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, resp)
}

// ---------- mål ----------

type addTargetsIn struct {
	AgentID string            `json:"agentId"`
	Hosts   string            `json:"hosts"`
	Kind    probe.Kind        `json:"kind"`
	Port    int               `json:"port"`
	Group   string            `json:"group"`
	Keyword string            `json:"keyword"`
	DNS     string            `json:"dnsServer"`
	Names   map[string]string `json:"names"` // värd -> namn (t.ex. från nätverksskanningen)
}

func (s *Server) agentEditable(id string) (*Agent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.agents[id]
	if !ok {
		return nil, errors.New("unknown agent")
	}
	if a.Kind == agentproto.KindDesktop {
		return nil, errors.New("the targets of a desktop app are changed in the app")
	}
	cp := *a
	return &cp, nil
}

func (s *Server) handleAddTargets(w http.ResponseWriter, r *http.Request) {
	var in addTargetsIn
	if !readJSON(w, r, &in) {
		return
	}
	if _, err := s.agentEditable(in.AgentID); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	s.mu.RLock()
	maxSort := 0
	for _, t := range s.targets {
		if t.Sort > maxSort {
			maxSort = t.Sort
		}
	}
	s.mu.RUnlock()
	defs := s.defaultAlertIDs(in.AgentID)
	var added []Target
	for _, tok := range strings.FieldsFunc(in.Hosts, func(r rune) bool { return strings.ContainsRune(",; \n\r\t", r) }) {
		host, kind, port := parseHost(tok, in.Kind, in.Port)
		if host == "" {
			continue
		}
		maxSort++
		t := Target{Target: monitor.Target{Host: host, Kind: kind, Port: port, Group: in.Group, AlertIDs: defs, Keyword: in.Keyword, DNSServer: in.DNS, Name: strings.TrimSpace(in.Names[host])}, AgentID: in.AgentID, Source: "server", Sort: maxSort}
		t.Normalize()
		if err := s.db.SaveTarget(t); err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		added = append(added, t)
	}
	if len(added) == 0 {
		httpErr(w, 400, "enter at least one host name or IP address")
		return
	}
	s.db.BumpConfig(in.AgentID)
	s.audit(r, "target", in.AgentID, fmt.Sprintf(i18n.T("added %d targets: %s"), len(added), strings.TrimSpace(in.Hosts)))
	s.afterChange(w, added)
}

func parseHost(tok string, kind probe.Kind, port int) (string, probe.Kind, int) {
	return monitor.ParseHostToken(tok, kind, port)
}

func (s *Server) handleUpdateTarget(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in Target
	if !readJSON(w, r, &in) {
		return
	}
	s.mu.RLock()
	old, ok := s.targets[id]
	var oldCopy Target
	if ok {
		oldCopy = *old
	}
	s.mu.RUnlock()
	if !ok {
		httpErr(w, 404, "the target does not exist")
		return
	}
	t := oldCopy
	if in.DependsOn != "" {
		s.mu.RLock()
		p, ok := s.targets[in.DependsOn]
		okCust := ok && s.customerOfTarget(p) == s.customerOfTarget(old)
		s.mu.RUnlock()
		if in.DependsOn == id || !okCust {
			httpErr(w, 400, "the target can only depend on another target of the same customer")
			return
		}
	}
	if oldCopy.Source == "agent" {
		// skrivbordsappens mål: bara larm, beroende och varningsgräns kan ändras här
		t.AlertIDs, t.WarnMs, t.DependsOn = in.AlertIDs, in.WarnMs, in.DependsOn
	} else {
		agentID := in.AgentID
		if agentID == "" {
			agentID = oldCopy.AgentID
		}
		if _, err := s.agentEditable(agentID); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		h, _, _ := parseHost(in.Host, in.Kind, in.Port)
		if h == "" {
			httpErr(w, 400, "invalid host")
			return
		}
		t.Target = in.Target
		t.Host = h
		t.AgentID = agentID
	}
	t.ID = id
	t.Normalize()
	if err := s.db.SaveTarget(t); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	if oldCopy.Host != t.Host || oldCopy.Kind != t.Kind || oldCopy.Port != t.Port || oldCopy.AgentID != t.AgentID {
		_, _ = s.db.Exec(`DELETE FROM samples WHERE target_id=?`, id)
		_, _ = s.db.Exec(`DELETE FROM agg_1m WHERE target_id=?`, id)
		s.mu.Lock()
		delete(s.trackers, id)
		s.mu.Unlock()
	}
	s.db.BumpConfig(t.AgentID)
	if oldCopy.AgentID != t.AgentID {
		s.db.BumpConfig(oldCopy.AgentID)
	}
	s.audit(r, "target", id, i18n.T("changed the target ")+t.Host)
	s.afterChange(w, t)
}

func (s *Server) handleDeleteTarget(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, ok := s.targetVisible(r, id)
	if !ok {
		httpErr(w, 404, "the target does not exist")
		return
	}
	if t.Source == "agent" {
		httpErr(w, 400, "the target belongs to a desktop app – delete it in the app")
		return
	}
	if err := s.db.DeleteTarget(id); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.db.BumpConfig(t.AgentID)
	s.audit(r, "target", id, i18n.T("deleted the target ")+t.Host)
	s.afterChange(w, map[string]bool{"ok": true})
}

func (s *Server) handleOrder(w http.ResponseWriter, r *http.Request) {
	var ids []string
	if !readJSON(w, r, &ids) {
		return
	}
	for i, id := range ids {
		_, _ = s.db.Exec(`UPDATE targets SET sort=? WHERE id=?`, i, id)
	}
	s.afterChange(w, map[string]bool{"ok": true})
}

// ---------- larmregler ----------

func (s *Server) handleSaveRule(w http.ResponseWriter, r *http.Request) {
	var in Rule
	if !readJSON(w, r, &in) {
		return
	}
	in.Normalize()
	if in.CustomerID != "" {
		s.mu.RLock()
		_, ok := s.customers[in.CustomerID]
		s.mu.RUnlock()
		if !ok {
			httpErr(w, 400, "unknown customer")
			return
		}
	}
	// systemnotiser, popup och ljud gäller webbläsaren – notis på servern är meningslöst
	in.Notify, in.Log = false, false
	if err := s.db.SaveRule(in); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.audit(r, "alert rule", in.ID, i18n.T("saved the alert rule ")+in.Name)
	s.afterChange(w, in)
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.db.Exec(`DELETE FROM alerts WHERE id=?`, id); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.audit(r, "alert rule", id, i18n.T("deleted the alert rule ")+s.rules[id].Name)
	// aktiva larm från regeln försvinner
	s.host.mu.Lock()
	for k := range s.host.alerts {
		if strings.HasPrefix(k, id+"|") {
			delete(s.host.alerts, k)
		}
	}
	s.host.mu.Unlock()
	// ta bort kopplingen från målen
	ts, _ := s.db.Targets()
	for _, t := range ts {
		keep := t.AlertIDs[:0]
		changed := false
		for _, a := range t.AlertIDs {
			if a == id {
				changed = true
				continue
			}
			keep = append(keep, a)
		}
		if changed {
			t.AlertIDs = keep
			_ = s.db.SaveTarget(t)
		}
	}
	s.afterChange(w, map[string]bool{"ok": true})
}

func (s *Server) handleTestRule(w http.ResponseWriter, r *http.Request) {
	var in Rule
	if !readJSON(w, r, &in) {
		return
	}
	in.Normalize()
	in.Notify, in.Log = false, false
	if err := s.eng.Test(in.Alert); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleAssignRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		TargetIDs []string `json:"targetIds"`
		On        bool     `json:"on"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	s.mu.RLock()
	rule, ok := s.rules[id]
	var list []Target
	for _, tid := range in.TargetIDs {
		if t, ok := s.targets[tid]; ok && (rule.CustomerID == "" || rule.CustomerID == s.customerOfTarget(t)) {
			list = append(list, *t)
		}
	}
	s.mu.RUnlock()
	if !ok {
		httpErr(w, 404, "the rule does not exist")
		return
	}
	for _, t := range list {
		var out []string
		has := false
		for _, a := range t.AlertIDs {
			if a == id {
				has = true
				if !in.On {
					continue
				}
			}
			out = append(out, a)
		}
		if in.On && !has {
			out = append(out, id)
		}
		if out == nil {
			out = []string{}
		}
		t.AlertIDs = out
		_ = s.db.SaveTarget(t)
	}
	s.afterChange(w, map[string]int{"changed": len(list)})
}

// ---------- användare ----------

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	us, err := s.db.Users()
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, us)
}

func (s *Server) handleSaveUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email, Name, Role, CustomerID, Password, Phone string
	}
	if !readJSON(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		u, err := s.createUser(in.Email, in.Name, in.Role, in.CustomerID, in.Password)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		if in.Phone != "" {
			_, _ = s.db.Exec(`UPDATE users SET phone=? WHERE id=?`, normPhone(in.Phone), u.ID)
		}
		s.audit(r, "user", u.ID, i18n.T("created the user ")+u.Email+" ("+u.Role+")")
		writeJSON(w, u)
		return
	}
	me := userFrom(r)
	if id == me.ID && in.Role != "admin" {
		httpErr(w, 400, "you cannot remove your own administrator role")
		return
	}
	role, cust := "admin", ""
	if in.Role != "admin" {
		role, cust = "viewer", in.CustomerID
		if cust == "" {
			httpErr(w, 400, "a customer user must be linked to a customer")
			return
		}
	}
	if _, err := s.db.Exec(`UPDATE users SET email=?, name=?, role=?, customer_id=? WHERE id=?`, strings.TrimSpace(in.Email), strings.TrimSpace(in.Name), role, cust, id); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	_, _ = s.db.Exec(`UPDATE users SET phone=? WHERE id=?`, normPhone(in.Phone), id)
	if in.Password != "" {
		h, err := hashPassword(in.Password)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		_, _ = s.db.Exec(`UPDATE users SET pw_hash=? WHERE id=?`, h, id)
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE user_id=?`, id)
	}
	s.audit(r, "user", id, i18n.T("changed the user ")+strings.TrimSpace(in.Email)+" ("+role+")")
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == userFrom(r).ID {
		httpErr(w, 400, "you cannot delete yourself")
		return
	}
	if _, err := s.db.Exec(`DELETE FROM users WHERE id=?`, id); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.audit(r, "user", id, i18n.T("deleted a user"))
	writeJSON(w, map[string]bool{"ok": true})
}

// ---------- inställningar ----------

type settingsOut struct {
	ServerSettings
	HasPassword bool `json:"hasPassword"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	st := s.settings
	s.mu.RUnlock()
	has := st.SMTP.Password != ""
	st.SMTP.Password = ""
	st.Notify = st.Notify.redact()
	st.Auth = st.Auth.redact()
	writeJSON(w, settingsOut{st, has})
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ServerSettings
		NewPassword bool `json:"newPassword"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	s.mu.Lock()
	if !in.NewPassword {
		in.SMTP.Password = s.settings.SMTP.Password
	}
	st := in.ServerSettings
	st.Notify = st.Notify.merge(s.settings.Notify)
	st.Auth = st.Auth.merge(s.settings.Auth)
	if st.RawDays < 1 {
		st.RawDays = 1
	}
	if st.AggDays < 7 {
		st.AggDays = 7
	}
	if st.ReportSec < 2 || st.ReportSec > 300 {
		st.ReportSec = 5
	}
	st.PublicURL = strings.TrimSuffix(strings.TrimSpace(st.PublicURL), "/")
	if st.Monitor.HistorySize < 300 {
		st.Monitor.HistorySize = 3600
	}
	st.Language = i18n.Norm(st.Language, "en")
	s.settings = st
	i18n.SetLang(st.Language)
	s.mu.Unlock()
	if err := s.db.SaveSettings(st); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.mailer.SetConfig(st.SMTP)
	_, _ = s.db.Exec(`UPDATE agents SET config_version=config_version+1`)
	s.audit(r, "settings", "", i18n.T("changed server settings"))
	s.afterChange(w, map[string]bool{"ok": true})
}

func (s *Server) handleTestEmail(w http.ResponseWriter, r *http.Request) {
	var in struct {
		To          string     `json:"to"`
		SMTP        alert.SMTP `json:"smtp"`
		NewPassword bool       `json:"newPassword"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !in.NewPassword {
		s.mu.RLock()
		in.SMTP.Password = s.settings.SMTP.Password
		s.mu.RUnlock()
	}
	to := alert.SplitAddresses(in.To)
	if len(to) == 0 {
		httpErr(w, 400, "enter a recipient")
		return
	}
	if err := alert.Send(in.SMTP, to, i18n.T("[PingVakt] Test email from the server"), i18n.T("This is a test email from the PingVakt server.\r\n")); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// userLang är användarens språk, annars serverns standardspråk.
func (s *Server) userLang(r *http.Request) string {
	if u := userFrom(r); u != nil && u.Lang != "" {
		return u.Lang
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings.Language
}
