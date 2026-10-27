package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"pingvakt/core/alert"
	"pingvakt/core/i18n"
	"pingvakt/core/monitor"
	"pingvakt/core/offsite"
)

const opsSchema = `
CREATE TABLE IF NOT EXISTS maintenance (id TEXT PRIMARY KEY, data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS alert_acks (key TEXT PRIMARY KEY, user TEXT NOT NULL, t INTEGER NOT NULL, note TEXT NOT NULL DEFAULT '');
`

// Maintenance är ett servicefönster då larm inte skickas.
// Omfattning: hela kunden, en site, en agent eller ett mål (det mest specifika som är ifyllt).
type Maintenance struct {
	ID         string `json:"id"`
	CustomerID string `json:"customerId"`
	SiteID     string `json:"siteId,omitempty"`
	AgentID    string `json:"agentId,omitempty"`
	TargetID   string `json:"targetId,omitempty"`
	Note       string `json:"note"`
	// engångsfönster
	Start int64 `json:"start,omitempty"`
	End   int64 `json:"end,omitempty"`
	// återkommande varje vecka
	Weekly      bool   `json:"weekly"`
	Days        []int  `json:"days,omitempty"` // 0 = söndag … 6 = lördag
	StartMin    int    `json:"startMin"`       // minuter efter midnatt (lokal tid)
	DurationMin int    `json:"durationMin"`
	CreatedBy   string `json:"createdBy"`
}

// ActiveAt avgör om fönstret gäller vid tiden t.
func (m Maintenance) ActiveAt(t time.Time) bool {
	if !m.Weekly {
		ms := t.UnixMilli()
		return m.Start > 0 && ms >= m.Start && ms < m.End
	}
	if m.DurationMin <= 0 {
		return false
	}
	t = t.Local()
	// fönstret kan ha börjat igår (t.ex. 22:00 + 6 h)
	for back := 0; back <= 7; back++ {
		day := t.AddDate(0, 0, -back)
		wd := int(day.Weekday())
		match := false
		for _, d := range m.Days {
			if d == wd {
				match = true
			}
		}
		if !match {
			continue
		}
		start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.Local).Add(time.Duration(m.StartMin) * time.Minute)
		end := start.Add(time.Duration(m.DurationMin) * time.Minute)
		if !t.Before(start) && t.Before(end) {
			return true
		}
	}
	return false
}

// Expired anger om ett engångsfönster har passerat.
func (m Maintenance) Expired(t time.Time) bool {
	return !m.Weekly && m.End > 0 && t.UnixMilli() >= m.End
}

type opsState struct {
	mu         sync.Mutex
	maint      map[string]Maintenance
	acks       map[string]Ack
	suppressed map[string]bool // larm vars utlösning undertrycktes – då undertrycks även "upphört"
}

// Ack är en kvittering av ett aktivt larm.
type Ack struct {
	User string `json:"user"`
	T    int64  `json:"t"`
	Note string `json:"note"`
}

func (s *Server) loadOps() {
	s.ops = &opsState{maint: map[string]Maintenance{}, acks: map[string]Ack{}, suppressed: map[string]bool{}}
	if rows, err := s.db.Query(`SELECT id, data FROM maintenance`); err == nil {
		for rows.Next() {
			var id, data string
			var m Maintenance
			if rows.Scan(&id, &data) == nil && json.Unmarshal([]byte(data), &m) == nil {
				m.ID = id
				s.ops.maint[id] = m
			}
		}
		rows.Close()
	}
	if rows, err := s.db.Query(`SELECT key, user, t, note FROM alert_acks`); err == nil {
		for rows.Next() {
			var k string
			var a Ack
			if rows.Scan(&k, &a.User, &a.T, &a.Note) == nil {
				s.ops.acks[k] = a
			}
		}
		rows.Close()
	}
}

// scopeOf returnerar mål-, agent-, site- och kund-ID för ett mål eller en agent.
func (s *Server) scopeOf(id string) (target, agent, site, customer string) {
	// SNMP-enheter och hastighetstester hör till agentens servicefönster
	if a, ok := strings.CutPrefix(id, "speed:"); ok {
		id = a
	} else if rest, ok := strings.CutPrefix(id, "snmp:"); ok {
		dev, _, _ := strings.Cut(rest, ":")
		snmps.mu.Lock()
		if d, ok := snmps.devs[dev]; ok {
			id = d.AgentID
		}
		snmps.mu.Unlock()
	}
	if rest, ok := strings.CutPrefix(id, "int:"); ok {
		// kopplingar till UniFi/Meraki/Aruba hör till kunden
		iid, _, _ := strings.Cut(rest, ":")
		integs.Lock()
		if in, ok := integs.list[iid]; ok {
			customer = in.CustomerID
		}
		integs.Unlock()
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if t, ok := s.targets[id]; ok {
		target, agent = id, t.AgentID
	} else if _, ok := s.agents[id]; ok {
		agent = id
	}
	if a, ok := s.agents[agent]; ok {
		site = a.SiteID
		customer = s.sites[a.SiteID].CustomerID
	}
	return
}

// maintenanceFor returnerar anteckningen för ett aktivt servicefönster som omfattar id (mål eller agent).
func (s *Server) maintenanceFor(id string, t time.Time) (string, bool) {
	target, agent, site, customer := s.scopeOf(id)
	if customer == "" {
		return "", false
	}
	s.ops.mu.Lock()
	defer s.ops.mu.Unlock()
	for _, m := range s.ops.maint {
		if m.CustomerID != customer || !m.ActiveAt(t) {
			continue
		}
		switch {
		case m.TargetID != "":
			if m.TargetID != target {
				continue
			}
		case m.AgentID != "":
			if m.AgentID != agent {
				continue
			}
		case m.SiteID != "":
			if m.SiteID != site {
				continue
			}
		}
		note := m.Note
		if note == "" {
			note = i18n.T("Maintenance window")
		}
		return note, true
	}
	return "", false
}

// inMaintenance returnerar alla mål och agenter som just nu omfattas av ett servicefönster.
func (s *Server) inMaintenance() map[string]string {
	out := map[string]string{}
	t := time.Now()
	s.mu.RLock()
	ids := make([]string, 0, len(s.targets)+len(s.agents))
	for id := range s.targets {
		ids = append(ids, id)
	}
	for id := range s.agents {
		ids = append(ids, id)
	}
	s.mu.RUnlock()
	s.ops.mu.Lock()
	any := false
	for _, m := range s.ops.maint {
		if m.ActiveAt(t) {
			any = true
			break
		}
	}
	s.ops.mu.Unlock()
	if !any {
		return out
	}
	for _, id := range ids {
		if note, ok := s.maintenanceFor(id, t); ok {
			out[id] = note
		}
	}
	return out
}

// parentDown returnerar namnet på ett överordnat mål som är nere.
func (s *Server) parentDown(targetID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]bool{targetID: true}
	t, ok := s.targets[targetID]
	for ok && t.DependsOn != "" && !seen[t.DependsOn] {
		seen[t.DependsOn] = true
		p, ok2 := s.targets[t.DependsOn]
		if !ok2 {
			break
		}
		if tr := s.trackers[p.ID]; tr != nil {
			st := tr.Stats().Status
			if st == monitor.StatusDown {
				n := p.Name
				if n == "" {
					n = p.Host
				}
				return n, true
			}
		}
		t, ok = p, true
	}
	return "", false
}

// alertFilter avgör om ett larm ska skickas (servicefönster, kvittering, beroenden).
func (s *Server) alertFilter(a alert.Alert, ev *alert.Event) bool {
	if ev.Kind == "test" {
		return true
	}
	key := a.ID + "|" + ev.TargetID
	if ev.Kind == "clear" {
		s.ops.mu.Lock()
		was := s.ops.suppressed[key]
		delete(s.ops.suppressed, key)
		_, acked := s.ops.acks[key]
		delete(s.ops.acks, key)
		s.ops.mu.Unlock()
		if acked {
			_, _ = s.db.Exec(`DELETE FROM alert_acks WHERE key=?`, key)
		}
		if was {
			ev.Note = i18n.T("The alert was not sent")
			return false
		}
		return true
	}
	suppress := func(note string) bool {
		ev.Note = note
		s.ops.mu.Lock()
		s.ops.suppressed[key] = true
		s.ops.mu.Unlock()
		return false
	}
	if note, ok := s.maintenanceFor(ev.TargetID, time.Now()); ok {
		return suppress(i18n.T("Maintenance window: ") + note)
	}
	if ev.Kind == "repeat" {
		s.ops.mu.Lock()
		ack, acked := s.ops.acks[key]
		s.ops.mu.Unlock()
		if acked {
			ev.Note = i18n.T("Acknowledged by ") + ack.User
			return false
		}
	}
	if a.Condition.ForTargets() {
		if parent, down := s.parentDown(ev.TargetID); down {
			return suppress(i18n.T("Sits behind ") + parent + i18n.T(" which is down"))
		}
	}
	return true
}

// ---------- API: servicefönster ----------

func (s *Server) handleListMaintenance(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	t := time.Now()
	s.ops.mu.Lock()
	out := []map[string]any{}
	for _, m := range s.ops.maint {
		if canSee(u, m.CustomerID) {
			out = append(out, map[string]any{"window": m, "active": m.ActiveAt(t), "expired": m.Expired(t)})
		}
	}
	s.ops.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i]["window"].(Maintenance), out[j]["window"].(Maintenance)
		return a.Start < b.Start
	})
	writeJSON(w, out)
}

func (s *Server) handleSaveMaintenance(w http.ResponseWriter, r *http.Request) {
	var m Maintenance
	if !readJSON(w, r, &m) {
		return
	}
	s.mu.RLock()
	_, ok := s.customers[m.CustomerID]
	s.mu.RUnlock()
	if !ok {
		httpErr(w, 400, "unknown customer")
		return
	}
	if m.Weekly {
		if len(m.Days) == 0 || m.DurationMin <= 0 || m.StartMin < 0 || m.StartMin >= 1440 {
			httpErr(w, 400, "choose weekdays, start time and duration")
			return
		}
		if m.DurationMin > 7*1440 {
			m.DurationMin = 7 * 1440
		}
	} else if m.Start <= 0 || m.End <= m.Start {
		httpErr(w, 400, "the end time must be after the start time")
		return
	}
	if id := r.PathValue("id"); id != "" {
		m.ID = id
	} else {
		m.ID = newID()
	}
	m.Note = strings.TrimSpace(m.Note)
	m.CreatedBy = userFrom(r).Email
	b, _ := json.Marshal(m)
	if _, err := s.db.Exec(`INSERT OR REPLACE INTO maintenance(id,data) VALUES(?,?)`, m.ID, string(b)); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.ops.mu.Lock()
	s.ops.maint[m.ID] = m
	s.ops.mu.Unlock()
	s.audit(r, "maintenance window", m.ID, i18n.T("saved maintenance window ")+m.Note)
	writeJSON(w, m)
}

func (s *Server) handleDeleteMaintenance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, _ = s.db.Exec(`DELETE FROM maintenance WHERE id=?`, id)
	s.ops.mu.Lock()
	delete(s.ops.maint, id)
	s.ops.mu.Unlock()
	s.audit(r, "maintenance window", id, i18n.T("deleted maintenance window"))
	writeJSON(w, map[string]bool{"ok": true})
}

// pruneMaintenance tar bort engångsfönster som passerat för mer än 30 dagar sedan.
func (s *Server) pruneMaintenance() {
	cut := time.Now().Add(-30 * 24 * time.Hour)
	s.ops.mu.Lock()
	var del []string
	for id, m := range s.ops.maint {
		if m.Expired(cut) {
			del = append(del, id)
			delete(s.ops.maint, id)
		}
	}
	s.ops.mu.Unlock()
	for _, id := range del {
		_, _ = s.db.Exec(`DELETE FROM maintenance WHERE id=?`, id)
	}
}

// ---------- API: aktiva larm och kvittering ----------

// ActiveAlert är ett pågående larm.
type ActiveAlert struct {
	Key        string `json:"key"`
	RuleID     string `json:"ruleId"`
	RuleName   string `json:"ruleName"`
	TargetID   string `json:"targetId"`
	TargetName string `json:"targetName"`
	CustomerID string `json:"customerId"`
	Since      int64  `json:"since"`
	Ack        *Ack   `json:"ack,omitempty"`
	Note       string `json:"note,omitempty"`
	TicketURL  string `json:"ticketUrl,omitempty"`
}

func (s *Server) handleActiveAlerts(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	act := s.activeAlerts()
	maint := s.inMaintenance()
	tix := s.ticketURLs()
	out := []ActiveAlert{}
	for k, since := range act {
		rid, tid, ok := strings.Cut(k, "|")
		if !ok {
			continue
		}
		s.mu.RLock()
		rule := s.rules[rid]
		name, cust := tid, ""
		if t, ok := s.targets[tid]; ok {
			name, cust = s.displayName(t), s.customerOfTarget(t)
		} else if a, ok := s.agents[tid]; ok {
			site := s.sites[a.SiteID]
			name, cust = s.customers[site.CustomerID].Name+" · "+site.Name+" · "+a.Name, site.CustomerID
		} else if tid == ServerHostID {
			name = i18n.T("PingVakt server")
		}
		s.mu.RUnlock()
		if n, c, ok := s.extraInfo(tid); ok {
			name, cust = n, c
		}
		if !(u.Role == "admin" || (cust != "" && cust == u.CustomerID)) {
			continue
		}
		aa := ActiveAlert{Key: k, RuleID: rid, RuleName: rule.Name, TargetID: tid, TargetName: name, CustomerID: cust, Since: since}
		s.ops.mu.Lock()
		if a, ok := s.ops.acks[k]; ok {
			aa.Ack = &a
		}
		if s.ops.suppressed[k] {
			aa.Note = i18n.T("No alert was sent")
		}
		s.ops.mu.Unlock()
		if n, ok := maint[tid]; ok {
			aa.Note = i18n.T("Maintenance window: ") + n
		}
		aa.TicketURL = tix[k]
		out = append(out, aa)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since > out[j].Since })
	writeJSON(w, out)
}

func (s *Server) handleAck(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key  string `json:"key"`
		Note string `json:"note"`
		Undo bool   `json:"undo"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if _, ok := s.activeAlerts()[in.Key]; !ok && !in.Undo {
		httpErr(w, 404, "the alert is no longer active")
		return
	}
	u := userFrom(r)
	if in.Undo {
		_, _ = s.db.Exec(`DELETE FROM alert_acks WHERE key=?`, in.Key)
		s.ops.mu.Lock()
		delete(s.ops.acks, in.Key)
		s.ops.mu.Unlock()
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	a := Ack{User: firstNonEmpty(u.Name, u.Email), T: now(), Note: strings.TrimSpace(in.Note)}
	_, _ = s.db.Exec(`INSERT OR REPLACE INTO alert_acks(key,user,t,note) VALUES(?,?,?,?)`, in.Key, a.User, a.T, a.Note)
	s.ops.mu.Lock()
	s.ops.acks[in.Key] = a
	s.ops.mu.Unlock()
	s.audit(r, "alert", in.Key, i18n.T("acknowledged alert ")+a.Note)
	b, _ := json.Marshal(map[string]any{"type": "ack", "key": in.Key, "ack": a})
	s.broadcastRaw("", b)
	writeJSON(w, a)
}

func firstNonEmpty(v ...string) string {
	for _, x := range v {
		if x != "" {
			return x
		}
	}
	return ""
}

// ---------- säkerhetskopior ----------

func (s *Server) backupDir() string { return filepath.Join(s.dataDir, "backups") }

// backupTo skriver en konsistent kopia av databasen till path.
func (s *Server) backupTo(path string) error {
	_ = os.Remove(path)
	_, err := s.db.Exec(`VACUUM INTO ?`, path)
	return err
}

// nightlyBackup körs varje natt kl 03:15 lokal tid och sparar BackupKeep kopior.
func (s *Server) nightlyBackupLoop(stop <-chan struct{}) {
	for {
		n := time.Now()
		next := time.Date(n.Year(), n.Month(), n.Day(), 3, 15, 0, 0, time.Local)
		if !next.After(n) {
			next = next.AddDate(0, 0, 1)
		}
		select {
		case <-stop:
			return
		case <-time.After(time.Until(next)):
		}
		if err := s.runBackup(); err != nil {
			log.Printf("backup: %v", err)
		}
	}
}

func (s *Server) runBackup() error {
	s.mu.RLock()
	keep := s.settings.BackupKeep
	off := s.settings.Offsite
	s.mu.RUnlock()
	if keep <= 0 {
		if !off.Enabled() {
			return nil
		}
		// bara utanför servern: gör en tillfällig kopia
		p := filepath.Join(s.dataDir, "pingvakt-"+time.Now().Format("20060102-1504")+".db")
		if err := s.backupTo(p); err != nil {
			return err
		}
		defer os.Remove(p)
		return s.uploadOffsite(off, p)
	}
	dir := s.backupDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	p := filepath.Join(dir, "pingvakt-"+time.Now().Format("20060102-1504")+".db")
	if err := s.backupTo(p); err != nil {
		return err
	}
	log.Printf("backup saved: %s", p)
	files, _ := filepath.Glob(filepath.Join(dir, "pingvakt-*.db"))
	sort.Strings(files)
	for len(files) > keep {
		_ = os.Remove(files[0])
		files = files[1:]
	}
	if off.Enabled() {
		return s.uploadOffsite(off, p)
	}
	return nil
}

// OffsiteStatus är resultatet av den senaste uppladdningen.
type OffsiteStatus struct {
	T    int64  `json:"t"`
	File string `json:"file,omitempty"`
	Err  string `json:"err,omitempty"`
}

func (s *Server) uploadOffsite(c offsite.Config, file string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	err := offsite.UploadAndPrune(ctx, c, file)
	st := OffsiteStatus{T: now(), File: filepath.Base(file)}
	if err != nil {
		st.Err = err.Error()
		log.Printf("off-site backup: %v", err)
	} else {
		log.Printf("off-site backup uploaded: %s", st.File)
	}
	b, _ := json.Marshal(st)
	_, _ = s.db.Exec(`INSERT INTO settings(key,value) VALUES('offsite_status',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, string(b))
	return err
}

func (s *Server) offsiteStatus() *OffsiteStatus {
	var raw string
	if s.db.QueryRow(`SELECT value FROM settings WHERE key='offsite_status'`).Scan(&raw) != nil {
		return nil
	}
	var st OffsiteStatus
	if json.Unmarshal([]byte(raw), &st) != nil {
		return nil
	}
	return &st
}

func redactOffsite(c offsite.Config) offsite.Config {
	c.SecretKey, c.Password = maskIf(c.SecretKey), maskIf(c.Password)
	return c
}

func mergeOffsite(c, old offsite.Config) offsite.Config {
	if c.SecretKey == secretMask {
		c.SecretKey = old.SecretKey
	}
	if c.Password == secretMask {
		c.Password = old.Password
	}
	c.Endpoint, c.Bucket, c.URL = strings.TrimSpace(c.Endpoint), strings.TrimSpace(c.Bucket), strings.TrimSpace(c.URL)
	c.AccessKey, c.Region = strings.TrimSpace(c.AccessKey), strings.TrimSpace(c.Region)
	if c.Keep < 0 {
		c.Keep = 0
	}
	return c
}

func (s *Server) handleTestOffsite(w http.ResponseWriter, r *http.Request) {
	var in offsite.Config
	if !readJSON(w, r, &in) {
		return
	}
	s.mu.RLock()
	in = mergeOffsite(in, s.settings.Offsite)
	s.mu.RUnlock()
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := offsite.Test(ctx, in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// handleOffsiteNow gör en säkerhetskopia och laddar upp den direkt.
func (s *Server) handleOffsiteNow(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	off := s.settings.Offsite
	s.mu.RUnlock()
	if !off.Enabled() {
		httpErr(w, 400, "off-site backup is not configured")
		return
	}
	p := filepath.Join(s.dataDir, "pingvakt-"+time.Now().Format("20060102-1504")+".db")
	if err := s.backupTo(p); err != nil {
		httpErr(w, 500, "backup: "+err.Error())
		return
	}
	defer os.Remove(p)
	if err := s.uploadOffsite(off, p); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	s.audit(r, "settings", "", i18n.T("uploaded a backup off-site"))
	writeJSON(w, s.offsiteStatus())
}

// BackupFile beskriver en sparad säkerhetskopia.
type BackupFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	T    int64  `json:"t"`
}

func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	files, _ := filepath.Glob(filepath.Join(s.backupDir(), "pingvakt-*.db"))
	out := []BackupFile{}
	for _, f := range files {
		if st, err := os.Stat(f); err == nil {
			out = append(out, BackupFile{Name: filepath.Base(f), Size: st.Size(), T: st.ModTime().UnixMilli()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T > out[j].T })
	writeJSON(w, map[string]any{"dir": s.backupDir(), "files": out, "offsite": s.offsiteStatus()})
}

// handleDownloadBackup skapar en färsk kopia (eller hämtar en sparad) och skickar den.
func (s *Server) handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("file")
	var path string
	cleanup := false
	if name != "" {
		if strings.ContainsAny(name, `/\`) || !strings.HasPrefix(name, "pingvakt-") || !strings.HasSuffix(name, ".db") {
			httpErr(w, 400, "invalid file name")
			return
		}
		path = filepath.Join(s.backupDir(), name)
	} else {
		f, err := os.CreateTemp(s.dataDir, "export-*.db")
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		path = f.Name()
		f.Close()
		cleanup = true
		if err := s.backupTo(path); err != nil {
			os.Remove(path)
			httpErr(w, 500, "backup: "+err.Error())
			return
		}
		name = "pingvakt-" + time.Now().Format("20060102-1504") + ".db"
	}
	if cleanup {
		defer os.Remove(path)
	}
	f, err := os.Open(path)
	if err != nil {
		httpErr(w, 404, "the file does not exist")
		return
	}
	defer f.Close()
	s.audit(r, "backup", name, i18n.T("downloaded a backup"))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	_, _ = io.Copy(w, f)
}

// audit skriver en rad i ändringsloggen för den inloggade användaren.
func (s *Server) audit(r *http.Request, kind, id, text string) {
	user := i18n.T("unknown|user")
	if u := userFrom(r); u != nil {
		user = u.Email
	}
	s.auditAs(user, clientIP(r), kind, id, text)
}

var _ = sql.ErrNoRows
