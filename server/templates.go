package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"pingvakt/core/i18n"
	"pingvakt/core/monitor"
	"pingvakt/core/probe"
)

const templateSchema = `CREATE TABLE IF NOT EXISTS templates (id TEXT PRIMARY KEY, data TEXT NOT NULL);`

// TemplateTarget är ett mål i en mall. Host och DNSServer kan innehålla
// {gateway}, {dns1} och {dns2} som ersätts med agentens värden.
type TemplateTarget struct {
	Name      string     `json:"name"`
	Kind      probe.Kind `json:"kind"`
	Host      string     `json:"host"`
	Port      int        `json:"port,omitempty"`
	Keyword   string     `json:"keyword,omitempty"`
	DNSServer string     `json:"dnsServer,omitempty"`
	TraceMin  int        `json:"traceMin,omitempty"`
}

// Template är en uppsättning mål som kan läggas till en agent i ett svep.
type Template struct {
	ID      string           `json:"id"`
	Name    string           `json:"name"`
	Note    string           `json:"note,omitempty"`
	Builtin bool             `json:"builtin,omitempty"`
	Targets []TemplateTarget `json:"targets"`
}

func builtinTemplates() []Template {
	T := i18n.T
	return []Template{
		{ID: "builtin-basic", Builtin: true, Name: T("Basic network"), Note: T("Gateway, DNS and the internet – a good start for every site"), Targets: []TemplateTarget{
			{Name: T("Gateway"), Kind: probe.KindICMP, Host: "{gateway}"},
			{Name: T("DNS server"), Kind: probe.KindICMP, Host: "{dns1}"},
			{Name: T("DNS lookup"), Kind: probe.KindDNS, Host: "www.google.com", DNSServer: "{dns1}"},
			{Name: "Cloudflare DNS", Kind: probe.KindICMP, Host: "1.1.1.1", TraceMin: 15},
			{Name: "Google DNS", Kind: probe.KindICMP, Host: "8.8.8.8"},
			{Name: T("Web"), Kind: probe.KindHTTP, Host: "https://www.google.com"},
		}},
		{ID: "builtin-m365", Builtin: true, Name: "Microsoft 365", Note: T("Sign-in, mail, Teams and SharePoint"), Targets: []TemplateTarget{
			{Name: T("Microsoft sign-in"), Kind: probe.KindHTTP, Host: "https://login.microsoftonline.com"},
			{Name: "Outlook", Kind: probe.KindHTTP, Host: "https://outlook.office365.com"},
			{Name: "Teams", Kind: probe.KindHTTP, Host: "https://teams.microsoft.com"},
			{Name: "Microsoft 365", Kind: probe.KindHTTP, Host: "https://www.office.com"},
			{Name: "Exchange Online", Kind: probe.KindTCP, Host: "outlook.office365.com", Port: 443},
			{Name: T("DNS lookup"), Kind: probe.KindDNS, Host: "outlook.office365.com", DNSServer: "{dns1}"},
		}},
		{ID: "builtin-google", Builtin: true, Name: "Google Workspace", Note: T("Sign-in, Gmail, Meet and Drive"), Targets: []TemplateTarget{
			{Name: T("Google sign-in"), Kind: probe.KindHTTP, Host: "https://accounts.google.com"},
			{Name: "Gmail", Kind: probe.KindHTTP, Host: "https://mail.google.com"},
			{Name: "Meet", Kind: probe.KindHTTP, Host: "https://meet.google.com"},
			{Name: "Drive", Kind: probe.KindHTTP, Host: "https://drive.google.com"},
		}},
		{ID: "builtin-internet", Builtin: true, Name: T("Internet quality"), Note: T("Several independent anycast addresses with path tracking"), Targets: []TemplateTarget{
			{Name: "Cloudflare", Kind: probe.KindICMP, Host: "1.1.1.1", TraceMin: 15},
			{Name: "Google", Kind: probe.KindICMP, Host: "8.8.8.8", TraceMin: 15},
			{Name: "Quad9", Kind: probe.KindICMP, Host: "9.9.9.9"},
			{Name: "OpenDNS", Kind: probe.KindICMP, Host: "208.67.222.222"},
		}},
	}
}

func (s *Server) templates() []Template {
	out := builtinTemplates()
	rows, err := s.db.Query(`SELECT data FROM templates`)
	if err != nil {
		return out
	}
	defer rows.Close()
	var own []Template
	for rows.Next() {
		var data string
		var t Template
		if rows.Scan(&data) == nil && json.Unmarshal([]byte(data), &t) == nil {
			own = append(own, t)
		}
	}
	sort.Slice(own, func(i, j int) bool { return strings.ToLower(own[i].Name) < strings.ToLower(own[j].Name) })
	return append(out, own...)
}

func (s *Server) template(id string) (Template, bool) {
	for _, t := range s.templates() {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}

// agentNetInfo ger agentens gateway och DNS-servrar (från de nät den rapporterat).
func (s *Server) agentNetInfo(agentID string) (gw string, dns []string) {
	scans.mu.Lock()
	nets := scans.nets[agentID]
	scans.mu.Unlock()
	for _, n := range nets {
		if gw == "" && n.Gateway != "" {
			gw = n.Gateway
		}
		if len(dns) == 0 && len(n.DNS) > 0 {
			dns = n.DNS
		}
	}
	return
}

// ResolvedTarget är ett mål i en mall med platshållarna ersatta.
type ResolvedTarget struct {
	TemplateTarget
	Missing string `json:"missing,omitempty"` // platshållare som agenten saknar värde för
	Exists  bool   `json:"exists,omitempty"`  // agenten har redan ett sådant mål
}

func resolveTemplate(t Template, gw string, dns []string, existing map[string]bool) []ResolvedTarget {
	vals := map[string]string{"{gateway}": gw}
	for i := 0; i < 2; i++ {
		v := ""
		if i < len(dns) {
			v = dns[i]
		}
		vals[fmt.Sprintf("{dns%d}", i+1)] = v
	}
	var out []ResolvedTarget
	for _, tt := range t.Targets {
		r := ResolvedTarget{TemplateTarget: tt}
		for k, v := range vals {
			for _, p := range []*string{&r.Host, &r.DNSServer} {
				if strings.Contains(*p, k) {
					if v == "" {
						r.Missing = k
					}
					*p = strings.ReplaceAll(*p, k, v)
				}
			}
		}
		r.Exists = existing[targetKey(r.Kind, r.Host, r.Port)]
		out = append(out, r)
	}
	return out
}

func targetKey(k probe.Kind, host string, port int) string {
	return fmt.Sprintf("%s|%s|%d", k, strings.ToLower(strings.TrimSpace(host)), port)
}

func (s *Server) existingKeys(agentID string) map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := map[string]bool{}
	for _, t := range s.targets {
		if t.AgentID == agentID {
			m[targetKey(t.Kind, t.Host, t.Port)] = true
		}
	}
	return m
}

// ---------- API ----------

// handleTemplates listar mallarna; med ?agent= fylls agentens värden i.
func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent")
	type view struct {
		Template
		Resolved []ResolvedTarget `json:"resolved,omitempty"`
	}
	out := []view{}
	var gw string
	var dns []string
	var existing map[string]bool
	if agentID != "" {
		if !s.agentVisible(r, agentID) {
			httpErr(w, 404, "the agent does not exist")
			return
		}
		gw, dns = s.agentNetInfo(agentID)
		existing = s.existingKeys(agentID)
	}
	for _, t := range s.templates() {
		v := view{Template: t}
		if agentID != "" {
			v.Resolved = resolveTemplate(t, gw, dns, existing)
		}
		out = append(out, v)
	}
	writeJSON(w, map[string]any{"templates": out, "gateway": gw, "dns": dns})
}

func normTemplate(t *Template) error {
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" {
		return fmt.Errorf("%s", i18n.T("enter a name"))
	}
	var keep []TemplateTarget
	for _, tt := range t.Targets {
		tt.Host = strings.TrimSpace(tt.Host)
		if tt.Host == "" {
			continue
		}
		mt := monitor.Target{Host: tt.Host, Kind: tt.Kind, Port: tt.Port}
		if !strings.Contains(tt.Host, "{") {
			h, k, p := monitor.ParseHostToken(tt.Host, tt.Kind, tt.Port)
			if h == "" {
				return fmt.Errorf(i18n.T("invalid host: %s"), tt.Host)
			}
			mt.Host, mt.Kind, mt.Port = h, k, p
		}
		mt.Normalize()
		tt.Kind, tt.Port = mt.Kind, mt.Port
		if !strings.Contains(tt.Host, "{") {
			tt.Host = mt.Host
		}
		tt.Name = strings.TrimSpace(tt.Name)
		keep = append(keep, tt)
	}
	if len(keep) == 0 {
		return fmt.Errorf("%s", i18n.T("add at least one target"))
	}
	if len(keep) > 200 {
		keep = keep[:200]
	}
	t.Targets = keep
	t.Builtin = false
	return nil
}

func (s *Server) handleSaveTemplate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Template
		FromAgent string `json:"fromAgent"` // skapa från en agents mål
	}
	if !readJSON(w, r, &in) {
		return
	}
	t := in.Template
	if id := r.PathValue("id"); id != "" {
		t.ID = id
	}
	if strings.HasPrefix(t.ID, "builtin-") {
		httpErr(w, 400, "built-in templates cannot be changed")
		return
	}
	if t.ID == "" {
		t.ID = monitor.NewID()
	}
	if in.FromAgent != "" {
		gw, dns := s.agentNetInfo(in.FromAgent)
		repl := map[string]string{}
		if gw != "" {
			repl[gw] = "{gateway}"
		}
		for i, d := range dns {
			if i < 2 {
				repl[d] = fmt.Sprintf("{dns%d}", i+1)
			}
		}
		s.mu.RLock()
		var ts []*Target
		for _, x := range s.targets {
			if x.AgentID == in.FromAgent {
				ts = append(ts, x)
			}
		}
		s.mu.RUnlock()
		sort.Slice(ts, func(i, j int) bool { return ts[i].Sort < ts[j].Sort })
		t.Targets = nil
		for _, x := range ts {
			tt := TemplateTarget{Name: x.Name, Kind: x.Kind, Host: x.Host, Port: x.Port, Keyword: x.Keyword, DNSServer: x.DNSServer, TraceMin: x.TraceMin}
			if v, ok := repl[tt.Host]; ok {
				tt.Host = v
			}
			if v, ok := repl[tt.DNSServer]; ok {
				tt.DNSServer = v
			}
			t.Targets = append(t.Targets, tt)
		}
	}
	if err := normTemplate(&t); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	b, _ := json.Marshal(t)
	if _, err := s.db.Exec(`INSERT INTO templates(id,data) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data`, t.ID, string(b)); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.audit(r, "settings", t.ID, i18n.T("saved the template ")+t.Name)
	writeJSON(w, t)
}

func (s *Server) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, ok := s.template(id)
	if !ok || t.Builtin {
		httpErr(w, 404, "the template does not exist")
		return
	}
	_, _ = s.db.Exec(`DELETE FROM templates WHERE id=?`, id)
	s.audit(r, "settings", id, i18n.T("deleted the template ")+t.Name)
	writeJSON(w, map[string]bool{"ok": true})
}

// handleApplyTemplate lägger till mallens mål på en agent.
func (s *Server) handleApplyTemplate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AgentID string `json:"agentId"`
		Group   string `json:"group"`
		Skip    []int  `json:"skip"` // index i mallen som inte ska läggas till
		Gateway string `json:"gateway"`
		DNS     string `json:"dns"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	t, ok := s.template(r.PathValue("id"))
	if !ok {
		httpErr(w, 404, "the template does not exist")
		return
	}
	if _, err := s.agentEditable(in.AgentID); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	gw, dns := s.agentNetInfo(in.AgentID)
	// värden som angetts för hand går före agentens
	if ip := strings.TrimSpace(in.Gateway); ip != "" {
		gw = ip
	}
	if v := strings.TrimSpace(in.DNS); v != "" {
		dns = netscanSplit(v)
	}
	skip := map[int]bool{}
	for _, i := range in.Skip {
		skip[i] = true
	}
	res := resolveTemplate(t, gw, dns, s.existingKeys(in.AgentID))
	s.mu.RLock()
	maxSort := 0
	for _, x := range s.targets {
		if x.Sort > maxSort {
			maxSort = x.Sort
		}
	}
	s.mu.RUnlock()
	defs := s.defaultAlertIDs(in.AgentID)
	group := strings.TrimSpace(in.Group)
	var added []Target
	var gwID string
	seen := map[string]bool{}
	for i, rt := range res {
		k := targetKey(rt.Kind, rt.Host, rt.Port)
		if skip[i] || rt.Missing != "" || rt.Exists || seen[k] {
			continue
		}
		seen[k] = true
		maxSort++
		mt := monitor.Target{Host: rt.Host, Kind: rt.Kind, Port: rt.Port, Name: rt.Name, Keyword: rt.Keyword, DNSServer: rt.DNSServer,
			TraceMin: rt.TraceMin, Group: group, AlertIDs: defs}
		mt.Normalize()
		tg := Target{Target: mt, AgentID: in.AgentID, Source: "server", Sort: maxSort}
		if strings.Contains(t.Targets[i].Host, "{gateway}") && rt.Kind == probe.KindICMP {
			gwID = tg.ID
		}
		added = append(added, tg)
	}
	if len(added) == 0 {
		httpErr(w, 400, "nothing to add – the agent already has these targets")
		return
	}
	for i := range added {
		// allt annat ligger bakom gatewayen, så ett gatewayavbrott ger bara ett larm
		if gwID != "" && added[i].ID != gwID {
			added[i].DependsOn = gwID
		}
		if err := s.db.SaveTarget(added[i]); err != nil {
			httpErr(w, 500, err.Error())
			return
		}
	}
	s.db.BumpConfig(in.AgentID)
	s.audit(r, "target", in.AgentID, fmt.Sprintf(i18n.T("added %d targets from the template %s"), len(added), t.Name))
	s.afterChange(w, added)
}

func netscanSplit(v string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		out = append(out, f)
	}
	return out
}
