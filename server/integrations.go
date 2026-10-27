package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"pingvakt/core/alert"
	"pingvakt/core/i18n"
	"pingvakt/core/monitor"
	"pingvakt/core/vendors"
)

const integrationSchema = `CREATE TABLE IF NOT EXISTS integrations (id TEXT PRIMARY KEY, customer_id TEXT NOT NULL, data TEXT NOT NULL);`

// Integration kopplar en kund till UniFi, Meraki eller Aruba Central.
type Integration struct {
	vendors.Config
	ID          string `json:"id"`
	CustomerID  string `json:"customerId"`
	Name        string `json:"name"`
	IntervalSec int    `json:"intervalSec"`
	Paused      bool   `json:"paused,omitempty"`
}

// IntegrationView är en koppling med senaste status (hemligheter maskerade).
type IntegrationView struct {
	Integration
	T       int64            `json:"t"`
	Err     string           `json:"err,omitempty"`
	Total   int              `json:"total"`
	Online  int              `json:"online"`
	Devices []vendors.Device `json:"devices,omitempty"`
}

type integState struct {
	t       int64
	err     string
	devices []vendors.Device
	down    map[string]int // enhet -> antal avläsningar i rad som offline
}

var integs = struct {
	sync.Mutex
	list    map[string]*Integration
	st      map[string]*integState
	running map[string]bool
}{list: map[string]*Integration{}, st: map[string]*integState{}, running: map[string]bool{}}

func (s *Server) loadIntegrations() {
	rows, err := s.db.Query(`SELECT data FROM integrations`)
	if err != nil {
		return
	}
	defer rows.Close()
	integs.Lock()
	defer integs.Unlock()
	for rows.Next() {
		var data string
		var in Integration
		if rows.Scan(&data) == nil && json.Unmarshal([]byte(data), &in) == nil {
			integs.list[in.ID] = &in
		}
	}
}

func (s *Server) saveIntegration(in Integration) error {
	b, _ := json.Marshal(in)
	_, err := s.db.Exec(`INSERT INTO integrations(id,customer_id,data) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET customer_id=excluded.customer_id, data=excluded.data`, in.ID, in.CustomerID, string(b))
	return err
}

// integrationLoop läser av alla kopplingar med deras intervall.
func (s *Server) integrationLoop(ctx context.Context) {
	tk := time.NewTicker(15 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
		integs.Lock()
		var due []Integration
		for id, in := range integs.list {
			st := integs.st[id]
			if in.Paused || integs.running[id] {
				continue
			}
			if st == nil || now()-st.t >= int64(in.IntervalSec)*1000 {
				integs.running[id] = true
				due = append(due, *in)
			}
		}
		integs.Unlock()
		for _, in := range due {
			go func(in Integration) {
				s.pollIntegration(ctx, in)
				integs.Lock()
				delete(integs.running, in.ID)
				integs.Unlock()
			}(in)
		}
	}
}

func (s *Server) pollIntegration(ctx context.Context, in Integration) {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	res, err := vendors.Fetch(cctx, in.Config)
	cancel()
	if res.Tokens != nil {
		// Aruba Central roterar förnyelsetoken – spara de nya direkt
		integs.Lock()
		if cur, ok := integs.list[in.ID]; ok {
			cur.AccessToken, cur.RefreshToken = res.Tokens[0], res.Tokens[1]
			cp := *cur
			integs.Unlock()
			if e := s.saveIntegration(cp); e != nil {
				log.Printf("save integration tokens: %v", e)
			}
		} else {
			integs.Unlock()
		}
	}
	integs.Lock()
	st := integs.st[in.ID]
	if st == nil {
		st = &integState{down: map[string]int{}}
		integs.st[in.ID] = st
	}
	st.t = now()
	if err != nil {
		st.err = err.Error()
	} else {
		st.err = ""
		st.devices = res.Devices
		seen := map[string]bool{}
		for _, d := range res.Devices {
			seen[d.ID] = true
			if d.Online {
				delete(st.down, d.ID)
			} else {
				st.down[d.ID]++
			}
		}
		for id := range st.down {
			if !seen[id] {
				delete(st.down, id)
			}
		}
	}
	integs.Unlock()
	s.checkIntegrationAlerts(in)
	b, _ := json.Marshal(map[string]any{"type": "integration", "id": in.ID})
	s.broadcastRaw(in.CustomerID, b)
}

func (s *Server) checkIntegrationAlerts(in Integration) {
	s.mu.RLock()
	cname := s.customers[in.CustomerID].Name
	var rules []Rule
	for _, r := range s.rules {
		if r.Condition == alert.CondVendor && (r.CustomerID == "" || r.CustomerID == in.CustomerID) {
			rules = append(rules, r)
		}
	}
	s.mu.RUnlock()
	if len(rules) == 0 {
		return
	}
	integs.Lock()
	st := integs.st[in.ID]
	var devs []vendors.Device
	down := map[string]int{}
	errText := ""
	if st != nil {
		devs = append(devs, st.devices...)
		for k, v := range st.down {
			down[k] = v
		}
		errText = st.err
	}
	integs.Unlock()
	if errText != "" {
		return // kunde inte läsa av – behåll larmens läge tills nästa lyckade avläsning
	}
	for _, r := range rules {
		need := r.Samples
		if need < 1 {
			need = 2
		}
		for _, d := range devs {
			if r.Threshold == 1 && d.Type != "ap" || r.Threshold == 2 && d.Type == "ap" {
				continue // bara åtkomstpunkter / bara övriga
			}
			tid := "int:" + in.ID + ":" + d.ID
			name := cname + " · " + in.Name + " · " + d.Name
			st := d.Status
			if st == "" {
				st = "offline"
			}
			s.setHostAlert(r, tid, name, d.IP, down[d.ID] >= need,
				fmt.Sprintf(i18n.T("%s reports %s as %s"), vendorName(in.Kind), d.Name, st), fmt.Sprintf(i18n.T("%s is online again"), d.Name))
		}
	}
}

func vendorName(k string) string {
	return map[string]string{"unifi": "UniFi", "meraki": "Meraki", "aruba": "Aruba Central"}[k]
}

// integInfo ger namn och kund för ett "int:<koppling>:<enhet>"-ID.
func (s *Server) integInfo(id string) (name, cust string, ok bool) {
	rest, found := strings.CutPrefix(id, "int:")
	if !found {
		return "", "", false
	}
	iid, dev, _ := strings.Cut(rest, ":")
	integs.Lock()
	in, ok2 := integs.list[iid]
	var dn string
	if st := integs.st[iid]; st != nil {
		for _, d := range st.devices {
			if d.ID == dev {
				dn = d.Name
			}
		}
	}
	var iname string
	if ok2 {
		cust, iname = in.CustomerID, in.Name
	}
	integs.Unlock()
	if !ok2 {
		return id, "", true
	}
	if dn == "" {
		dn = dev
	}
	s.mu.RLock()
	cname := s.customers[cust].Name
	s.mu.RUnlock()
	return cname + " · " + iname + " · " + dn, cust, true
}

// ---------- API ----------

func maskIntegration(in Integration) Integration {
	in.APIKey, in.Password, in.ClientSecret = maskIf(in.APIKey), maskIf(in.Password), maskIf(in.ClientSecret)
	in.AccessToken, in.RefreshToken = maskIf(in.AccessToken), maskIf(in.RefreshToken)
	return in
}

func (s *Server) integrationView(in Integration, withDevices bool) IntegrationView {
	v := IntegrationView{Integration: maskIntegration(in)}
	integs.Lock()
	if st := integs.st[in.ID]; st != nil {
		v.T, v.Err, v.Total = st.t, st.err, len(st.devices)
		for _, d := range st.devices {
			if d.Online {
				v.Online++
			}
		}
		if withDevices {
			v.Devices = append([]vendors.Device{}, st.devices...)
		}
	}
	integs.Unlock()
	return v
}

func (s *Server) handleIntegrations(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	cust := r.URL.Query().Get("customer")
	integs.Lock()
	var list []Integration
	for _, in := range integs.list {
		if (cust == "" || in.CustomerID == cust) && canSee(u, in.CustomerID) {
			list = append(list, *in)
		}
	}
	integs.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	out := []IntegrationView{}
	for _, in := range list {
		out = append(out, s.integrationView(in, false))
	}
	writeJSON(w, out)
}

func (s *Server) handleIntegrationGet(w http.ResponseWriter, r *http.Request) {
	integs.Lock()
	in, ok := integs.list[r.PathValue("id")]
	var cp Integration
	if ok {
		cp = *in
	}
	integs.Unlock()
	if !ok || !canSee(userFrom(r), cp.CustomerID) {
		httpErr(w, 404, "the integration does not exist")
		return
	}
	writeJSON(w, s.integrationView(cp, true))
}

// mergeSecrets behåller sparade hemligheter när gränssnittet skickar tillbaka masken.
func mergeSecrets(in *Integration, old *Integration) {
	keep := func(v *string, o string) {
		if *v == secretMask {
			*v = o
		}
	}
	if old == nil {
		old = &Integration{}
	}
	keep(&in.APIKey, old.APIKey)
	keep(&in.Password, old.Password)
	keep(&in.ClientSecret, old.ClientSecret)
	keep(&in.AccessToken, old.AccessToken)
	keep(&in.RefreshToken, old.RefreshToken)
}

func normIntegration(in *Integration) error {
	in.Name = strings.TrimSpace(in.Name)
	in.URL = strings.TrimSpace(in.URL)
	in.Site = strings.TrimSpace(in.Site)
	in.APIKey = strings.TrimSpace(in.APIKey)
	switch in.Kind {
	case "unifi", "meraki", "aruba":
	default:
		return fmt.Errorf("%s", i18n.T("choose UniFi, Meraki or Aruba Central"))
	}
	if in.Name == "" {
		in.Name = vendorName(in.Kind)
	}
	if in.IntervalSec < 60 {
		in.IntervalSec = 120
	}
	if in.IntervalSec > 3600 {
		in.IntervalSec = 3600
	}
	return nil
}

func (s *Server) handleIntegrationTest(w http.ResponseWriter, r *http.Request) {
	var in Integration
	if !readJSON(w, r, &in) {
		return
	}
	integs.Lock()
	old := integs.list[in.ID]
	var oc *Integration
	if old != nil {
		c := *old
		oc = &c
	}
	integs.Unlock()
	mergeSecrets(&in, oc)
	if err := normIntegration(&in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	res, err := vendors.Fetch(ctx, in.Config)
	if res.Tokens != nil && oc != nil {
		in.AccessToken, in.RefreshToken = res.Tokens[0], res.Tokens[1]
		integs.Lock()
		if cur := integs.list[in.ID]; cur != nil {
			cur.AccessToken, cur.RefreshToken = in.AccessToken, in.RefreshToken
			_ = s.saveIntegration(*cur)
		}
		integs.Unlock()
	}
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	resp := map[string]any{"devices": res.Devices}
	if res.Tokens != nil {
		resp["tokens"] = true // gränssnittet måste spara direkt så att de nya token inte går förlorade
		resp["accessToken"], resp["refreshToken"] = res.Tokens[0], res.Tokens[1]
	}
	writeJSON(w, resp)
}

func (s *Server) handleIntegrationSave(w http.ResponseWriter, r *http.Request) {
	var in Integration
	if !readJSON(w, r, &in) {
		return
	}
	if id := r.PathValue("id"); id != "" {
		in.ID = id
	}
	integs.Lock()
	var oc *Integration
	if old := integs.list[in.ID]; old != nil {
		c := *old
		oc = &c
	}
	integs.Unlock()
	if in.ID != "" && oc == nil {
		httpErr(w, 404, "the integration does not exist")
		return
	}
	if in.ID == "" {
		in.ID = monitor.NewID()
	}
	mergeSecrets(&in, oc)
	if err := normIntegration(&in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	s.mu.RLock()
	_, custOK := s.customers[in.CustomerID]
	s.mu.RUnlock()
	if !custOK {
		httpErr(w, 400, "unknown customer")
		return
	}
	if err := s.saveIntegration(in); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	integs.Lock()
	cp := in
	integs.list[in.ID] = &cp
	delete(integs.st, in.ID) // läs av direkt med de nya inställningarna
	integs.Unlock()
	s.audit(r, "customer", in.CustomerID, i18n.T("saved the integration ")+in.Name)
	writeJSON(w, s.integrationView(in, false))
}

func (s *Server) handleIntegrationDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	integs.Lock()
	in, ok := integs.list[id]
	delete(integs.list, id)
	delete(integs.st, id)
	integs.Unlock()
	if !ok {
		httpErr(w, 404, "the integration does not exist")
		return
	}
	_, _ = s.db.Exec(`DELETE FROM integrations WHERE id=?`, id)
	s.host.mu.Lock()
	for k := range s.host.alerts {
		if strings.Contains(k, "|int:"+id+":") {
			delete(s.host.alerts, k)
		}
	}
	s.host.mu.Unlock()
	s.audit(r, "customer", in.CustomerID, i18n.T("deleted the integration ")+in.Name)
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) pruneIntegrations() {
	_, _ = s.db.Exec(`DELETE FROM integrations WHERE customer_id NOT IN (SELECT id FROM customers)`)
}
