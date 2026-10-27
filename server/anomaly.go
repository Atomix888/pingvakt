package main

import (
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"

	"pingvakt/core/alert"
	"pingvakt/core/i18n"
)

// Dynamiska tröskelvärden: PingVakt lär sig vad som är normal svarstid per mål och
// timme på dygnet (de senaste två veckorna) och larmar när svarstiden avviker kraftigt.

type hourStat struct {
	Mean float64 `json:"mean"`
	Std  float64 `json:"std"`
	Days int     `json:"days"`
}

type baseline struct {
	at    int64
	hours [24]hourStat
}

var baselines = struct {
	sync.Mutex
	m       map[string]*baseline
	lastRun int64
}{m: map[string]*baseline{}}

const baselineDays = 14
const minBaselineDays = 5

// computeBaseline räknar medel och spridning av timmedelvärdena per timme på dygnet.
func (s *Server) computeBaseline(targetID string) *baseline {
	to := now() - now()%3600000
	from := to - baselineDays*86400000
	rows, err := s.db.Query(`SELECT t/3600000, sum(sum), sum(n-lost) FROM agg_1m WHERE target_id=? AND t>=? AND t<? GROUP BY t/3600000`, targetID, from, to)
	b := &baseline{at: now()}
	if err != nil {
		return b
	}
	defer rows.Close()
	var vals [24][]float64
	for rows.Next() {
		var h int64
		var sum float64
		var got int64
		if rows.Scan(&h, &sum, &got) != nil || got < 30 { // minst en halvtimmes mätningar
			continue
		}
		hr := time.UnixMilli(h * 3600000).Hour()
		vals[hr] = append(vals[hr], sum/float64(got))
	}
	for i, v := range vals {
		if len(v) == 0 {
			continue
		}
		var m float64
		for _, x := range v {
			m += x
		}
		m /= float64(len(v))
		var sd float64
		for _, x := range v {
			sd += (x - m) * (x - m)
		}
		if len(v) > 1 {
			sd = math.Sqrt(sd / float64(len(v)-1))
		}
		b.hours[i] = hourStat{Mean: round1(m), Std: round1(sd), Days: len(v)}
	}
	return b
}

func (s *Server) baselineFor(targetID string) *baseline {
	baselines.Lock()
	b := baselines.m[targetID]
	baselines.Unlock()
	if b != nil && now()-b.at < 3600000 {
		return b
	}
	b = s.computeBaseline(targetID)
	baselines.Lock()
	baselines.m[targetID] = b
	baselines.Unlock()
	return b
}

// anomalyLimit ger gränsen för en timme: medel + max(k·spridning, minsta ökning, 25 % av medel).
func anomalyLimit(h hourStat, k, minDelta float64) float64 {
	d := math.Max(k*h.Std, minDelta)
	return h.Mean + math.Max(d, h.Mean*0.25)
}

// checkAnomalies körs en gång i minuten.
func (s *Server) checkAnomalies() {
	t := now()
	baselines.Lock()
	if t-baselines.lastRun < 55000 {
		baselines.Unlock()
		return
	}
	baselines.lastRun = t
	baselines.Unlock()
	s.mu.RLock()
	var rules []Rule
	for _, r := range s.rules {
		if r.Condition == alert.CondAnomaly {
			rules = append(rules, r)
		}
	}
	type tg struct {
		id, cust, name, host string
		paused               bool
	}
	var targets []tg
	if len(rules) > 0 {
		for id, x := range s.targets {
			targets = append(targets, tg{id, s.customerOfTarget(x), s.displayName(x), x.Host, x.Paused})
		}
	}
	s.mu.RUnlock()
	if len(rules) == 0 {
		return
	}
	hr := time.Now().Hour()
	for _, r := range rules {
		win := int64(r.Samples) * 60000
		for _, x := range targets {
			if r.CustomerID != "" && r.CustomerID != x.cust {
				continue
			}
			if x.paused {
				s.setHostAlert(r, x.id, x.name, x.host, false, "", i18n.T("The latency is normal again"))
				continue
			}
			b := s.baselineFor(x.id)
			h := b.hours[hr]
			if h.Days < minBaselineDays {
				continue // lär sig fortfarande
			}
			var sum float64
			var got, mins int64
			_ = s.db.QueryRow(`SELECT coalesce(sum(sum),0), coalesce(sum(n-lost),0), count(*) FROM agg_1m WHERE target_id=? AND t>=?`, x.id, t-win).Scan(&sum, &got, &mins)
			if got == 0 || mins < int64(r.Samples)*7/10 {
				continue // ingen data (nere-larmet tar hand om det)
			}
			cur := sum / float64(got)
			limit := anomalyLimit(h, r.Threshold, float64(r.Count))
			met := cur > limit
			s.setHostAlert(r, x.id, x.name, x.host, met,
				fmt.Sprintf(i18n.T("Latency %.1f ms over the last %d min – normally %.1f ms (±%.1f) at this hour"), cur, r.Samples, h.Mean, h.Std),
				fmt.Sprintf(i18n.T("The latency is normal again (%.1f ms)"), cur))
		}
	}
}

// handleBaseline ger målets normalvärden per timme.
func (s *Server) handleBaseline(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.RLock()
	t, ok := s.targets[id]
	vis := ok && canSee(userFrom(r), s.customerOfTarget(t))
	s.mu.RUnlock()
	if !vis {
		httpErr(w, 404, "the target does not exist")
		return
	}
	b := s.baselineFor(id)
	writeJSON(w, map[string]any{"hours": b.hours, "now": time.Now().Hour(), "minDays": minBaselineDays})
}
