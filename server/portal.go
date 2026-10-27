package main

import (
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"pingvakt/core/alert"
)

// Kundportalen: det kundens användare ser när de loggar in – status, pågående störningar,
// planerade servicefönster, händelser och rapporter, och om de vill få e-post vid störningar.

type portalIncident struct {
	Alert  string `json:"alert"`
	Target string `json:"target"`
	Since  int64  `json:"since"`
	Ack    bool   `json:"ack,omitempty"`
}

type portalMaint struct {
	Note   string `json:"note"`
	Start  int64  `json:"start,omitempty"`
	End    int64  `json:"end,omitempty"`
	Weekly bool   `json:"weekly,omitempty"`
	Days   []int  `json:"days,omitempty"`
	From   int    `json:"startMin,omitempty"`
	Dur    int    `json:"durationMin,omitempty"`
	Active bool   `json:"active,omitempty"`
	Scope  string `json:"scope,omitempty"`
}

func (s *Server) handlePortal(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	cid := r.URL.Query().Get("customer")
	if u.Role != "admin" {
		cid = u.CustomerID
	}
	s.mu.RLock()
	c, ok := s.customers[cid]
	company := s.settings.ReportName
	s.mu.RUnlock()
	if !ok {
		httpErr(w, 404, "the customer does not exist")
		return
	}
	st := s.buildStatus(c, company)
	// pågående störningar
	act := s.activeAlerts()
	maint := s.inMaintenance()
	s.ops.mu.Lock()
	acks := map[string]bool{}
	for k := range s.ops.acks {
		acks[k] = true
	}
	s.ops.mu.Unlock()
	inc := []portalIncident{}
	for k, since := range act {
		rid, tid, _ := strings.Cut(k, "|")
		s.mu.RLock()
		rule := s.rules[rid]
		name, cust := "", ""
		if t, ok := s.targets[tid]; ok {
			name, cust = s.displayName(t), s.customerOfTarget(t)
		} else if a, ok := s.agents[tid]; ok {
			name, cust = a.Name, s.customerOfAgent(a)
		}
		s.mu.RUnlock()
		if cust == "" {
			if n, c2, ok := s.extraInfo(tid); ok {
				name, cust = n, c2
			}
		}
		name = strings.TrimPrefix(name, c.Name+" · ") // kundens namn behövs inte i kundens egen portal
		if cust != cid {
			continue
		}
		if _, m := maint[tid]; m {
			continue
		}
		inc = append(inc, portalIncident{Alert: rule.Name, Target: name, Since: since, Ack: acks[k]})
	}
	sort.Slice(inc, func(i, j int) bool { return inc[i].Since > inc[j].Since })
	// servicefönster som pågår eller kommer
	t := time.Now()
	mw := []portalMaint{}
	s.ops.mu.Lock()
	for _, m := range s.ops.maint {
		if m.CustomerID != cid || m.Expired(t) {
			continue
		}
		pm := portalMaint{Note: m.Note, Start: m.Start, End: m.End, Weekly: m.Weekly, Days: m.Days, From: m.StartMin, Dur: m.DurationMin, Active: m.ActiveAt(t)}
		s.mu.RLock()
		switch {
		case m.TargetID != "":
			if x, ok := s.targets[m.TargetID]; ok {
				pm.Scope = s.displayName(x)
			}
		case m.AgentID != "":
			if a, ok := s.agents[m.AgentID]; ok {
				pm.Scope = s.sites[a.SiteID].Name
			}
		case m.SiteID != "":
			pm.Scope = s.sites[m.SiteID].Name
		}
		s.mu.RUnlock()
		mw = append(mw, pm)
	}
	s.ops.mu.Unlock()
	sort.Slice(mw, func(i, j int) bool { return mw[i].Start < mw[j].Start })
	evs, _ := s.db.Events(cid, 30)
	var sub int
	_ = s.db.QueryRow(`SELECT subscribe FROM users WHERE id=?`, u.ID).Scan(&sub)
	writeJSON(w, map[string]any{"customerId": cid, "status": st, "incidents": inc, "maintenance": mw, "events": evs, "subscribed": sub == 1})
}

func (s *Server) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		On bool `json:"on"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	u := userFrom(r)
	_, _ = s.db.Exec(`UPDATE users SET subscribe=? WHERE id=?`, b2i(in.On), u.ID)
	writeJSON(w, map[string]bool{"ok": true})
}

// notifySubscribers skickar e-post till användare som vill veta när något går ner eller kommer tillbaka.
func (s *Server) notifySubscribers(a alert.Alert, ev alert.Event) {
	if ev.Kind != "fire" && ev.Kind != "clear" {
		return
	}
	cust := s.customerOfEvent(ev)
	rows, err := s.db.Query(`SELECT email, role, customer_id FROM users WHERE subscribe=1 AND email<>''`)
	if err != nil {
		return
	}
	var to []string
	for rows.Next() {
		var email, role, c string
		if rows.Scan(&email, &role, &c) == nil && (role == "admin" || (cust != "" && c == cust)) {
			to = append(to, email)
		}
	}
	rows.Close()
	for _, addr := range to {
		if err := s.mailer.SendNow([]string{addr}, []alert.Event{ev}); err != nil {
			log.Printf("subscriber email %s: %v", addr, err)
		}
	}
}
