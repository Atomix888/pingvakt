package main

import (
	"fmt"

	"pingvakt/core/alert"
	"pingvakt/core/i18n"
)

// checkCerts larmar när ett HTTPS-måls certifikat snart går ut.
// Aktiva larm lagras i host.alerts så att de syns, kan kvitteras och rensas som övriga.
func (s *Server) checkCerts() {
	type item struct {
		id, name, host, cust string
		days                 int
	}
	s.mu.RLock()
	var rules []Rule
	for _, r := range s.rules {
		if r.Condition == alert.CondCert {
			rules = append(rules, r)
		}
	}
	var items []item
	if len(rules) > 0 {
		for id, t := range s.targets {
			tr := s.trackers[id]
			if tr == nil || t.Paused {
				continue
			}
			st := tr.Stats()
			if st.CertDays < 0 && st.HTTPStatus == 0 {
				continue
			}
			items = append(items, item{id, s.displayName(t), t.Host, s.customerOfTarget(t), st.CertDays})
		}
	}
	s.mu.RUnlock()
	t := now()
	for _, r := range rules {
		for _, it := range items {
			key := r.ID + "|" + it.id
			if r.CustomerID != "" && r.CustomerID != it.cust {
				continue
			}
			met := it.days >= 0 && float64(it.days) < r.Threshold
			s.host.mu.Lock()
			_, active := s.host.alerts[key]
			kind, detail := "", ""
			switch {
			case met && !active:
				s.host.alerts[key] = t
				kind = "fire"
				detail = fmt.Sprintf(i18n.T("The certificate expires in %d days"), it.days)
				if it.days < 0 {
					detail = i18n.T("The certificate has expired")
				}
			case !met && active && it.days >= 0:
				delete(s.host.alerts, key)
				if r.OnClear {
					kind, detail = "clear", fmt.Sprintf(i18n.T("The certificate has been renewed (valid %d more days)"), it.days)
				}
			}
			s.host.mu.Unlock()
			if kind == "" {
				continue
			}
			s.eng.Fire(r.Alert, alert.Event{Kind: kind, AlertID: r.ID, AlertName: r.Name, TargetID: it.id, TargetName: it.name,
				Host: it.host, Time: t, Detail: detail, Popup: r.Popup, Sound: r.Sound && kind == "fire", SoundName: r.SoundName})
		}
	}
}
