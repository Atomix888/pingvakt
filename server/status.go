package main

import (
	"net/http"
	"sort"
	"sync"
	"time"

	"pingvakt/core/i18n"
	"pingvakt/core/monitor"
)

// Publik statussida per kund: en hemlig länk som visar status och tillgänglighet
// utan inloggning. IP-adresser och detaljer visas inte.

type publicTarget struct {
	Name     string  `json:"name"`
	Status   string  `json:"status"`
	Uptime24 float64 `json:"uptime24"`
	Uptime30 float64 `json:"uptime30"`
	Since    int64   `json:"since"`
}

type publicSite struct {
	Name    string         `json:"name"`
	Targets []publicTarget `json:"targets"`
}

type publicStatus struct {
	Customer  string       `json:"customer"`
	Company   string       `json:"company"`
	Generated int64        `json:"generated"`
	Overall   string       `json:"overall"`
	Sites     []publicSite `json:"sites"`
	Maint     []string     `json:"maint"`
}

var uptimeCache = struct {
	sync.Mutex
	m map[string]uptimeEntry
}{m: map[string]uptimeEntry{}}

type uptimeEntry struct {
	at       time.Time
	u24, u30 float64
}

// uptime returnerar tillgänglighet i procent (minuter utan total förlust) för 24 h och 30 d.
func (s *Server) uptime(id string) (float64, float64) {
	uptimeCache.Lock()
	e, ok := uptimeCache.m[id]
	uptimeCache.Unlock()
	if ok && time.Since(e.at) < time.Minute {
		return e.u24, e.u30
	}
	calc := func(from int64) float64 {
		var mins, down int
		_ = s.db.QueryRow(`SELECT count(*), coalesce(sum(CASE WHEN lost=n AND n>0 THEN 1 ELSE 0 END),0) FROM agg_1m WHERE target_id=? AND t>=?`, id, from).Scan(&mins, &down)
		if mins == 0 {
			return -1
		}
		return float64(mins-down) * 100 / float64(mins)
	}
	e = uptimeEntry{at: time.Now(), u24: calc(now() - 86400000), u30: calc(now() - 30*86400000)}
	uptimeCache.Lock()
	uptimeCache.m[id] = e
	uptimeCache.Unlock()
	return e.u24, e.u30
}

func (s *Server) handlePublicStatus(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	s.mu.RLock()
	var cust Customer
	found := false
	for _, c := range s.customers {
		if c.StatusKey != "" && c.StatusKey == key {
			cust, found = c, true
		}
	}
	company := s.settings.ReportName
	s.mu.RUnlock()
	if !found || len(key) < 16 {
		httpErr(w, 404, "the status page does not exist")
		return
	}
	out := s.buildStatus(cust, company)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, out)
}

// buildStatus sammanställer kundens status (används av statussidan och kundportalen).
func (s *Server) buildStatus(cust Customer, company string) publicStatus {
	snap := s.snapshot(map[string]bool{cust.ID: true})
	maint := s.inMaintenance()
	out := publicStatus{Customer: cust.Name, Company: company, Generated: now(), Overall: "up", Sites: []publicSite{}, Maint: []string{}}
	s.mu.RLock()
	siteIdx := map[string]int{}
	type item struct {
		site string
		t    Target
	}
	var items []item
	for _, t := range s.targets {
		a, ok := s.agents[t.AgentID]
		if !ok || s.sites[a.SiteID].CustomerID != cust.ID {
			continue
		}
		items = append(items, item{s.sites[a.SiteID].Name, *t})
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].site != items[j].site {
			return items[i].site < items[j].site
		}
		return items[i].t.Sort < items[j].t.Sort
	})
	rank := map[string]int{"down": 3, "offline": 2, "warn": 1}
	worst := 0
	seenMaint := map[string]bool{}
	for _, it := range items {
		i, ok := siteIdx[it.site]
		if !ok {
			i = len(out.Sites)
			siteIdx[it.site] = i
			out.Sites = append(out.Sites, publicSite{Name: it.site, Targets: []publicTarget{}})
		}
		st := snap.Stats[it.t.ID]
		status := st.Status
		if note, ok := maint[it.t.ID]; ok {
			status = "maint"
			if !seenMaint[note] {
				seenMaint[note] = true
				out.Maint = append(out.Maint, note)
			}
		} else if it.t.Paused {
			status = monitor.StatusPaused
		}
		if r := rank[status]; r > worst {
			worst = r
		}
		name := it.t.Name
		if name == "" {
			name = it.t.Host
		}
		u24, u30 := s.uptime(it.t.ID)
		out.Sites[i].Targets = append(out.Sites[i].Targets, publicTarget{Name: name, Status: status, Uptime24: u24, Uptime30: u30, Since: st.Since})
	}
	out.Overall = map[int]string{0: "up", 1: "warn", 2: "offline", 3: "down"}[worst]
	return out
}

func (s *Server) handleStatusKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Enable bool `json:"enable"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	key := ""
	if in.Enable {
		key, _ = newToken()
		key = key[4:] // utan prefixet "pva_"
	}
	if _, err := s.db.Exec(`UPDATE customers SET status_key=? WHERE id=?`, key, id); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	if in.Enable {
		s.audit(r, "customer", id, i18n.T("created a new status page link"))
	} else {
		s.audit(r, "customer", id, i18n.T("turned off the status page"))
	}
	s.afterChange(w, map[string]string{"key": key})
}

// serveStatusPage serverar den publika statussidan (status.html).
func (s *Server) serveStatusPage(web http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/status.html"
		w.Header().Set("X-Robots-Tag", "noindex")
		web.ServeHTTP(w, r2)
	}
}
