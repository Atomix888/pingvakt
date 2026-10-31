package main

import (
	"fmt"
	"log"
	"sync"

	"pingvakt/core/agentproto"
	"pingvakt/core/alert"
	"pingvakt/core/i18n"
)

// Värden från Android-appen: batteri, mobilnät och nätverksstatus. Sparas som minutvärden.

const phoneSchema = `
CREATE TABLE IF NOT EXISTS phone_metrics (
  agent_id TEXT NOT NULL, t INTEGER NOT NULL, bat INTEGER NOT NULL, chg INTEGER NOT NULL, temp REAL NOT NULL,
  rsrp INTEGER NOT NULL, sinr INTEGER NOT NULL, tech TEXT NOT NULL,
  PRIMARY KEY (agent_id, t)) WITHOUT ROWID;
`

var phones = struct {
	sync.Mutex
	latest  map[string]*agentproto.Phone
	at      map[string]int64
	saved   map[string]int64 // senaste sparade minut
	slowRms map[string]Roam  // senaste långsamma roaming per agent
}{latest: map[string]*agentproto.Phone{}, at: map[string]int64{}, saved: map[string]int64{}, slowRms: map[string]Roam{}}

func (s *Server) ingestPhone(agentID string, p agentproto.Phone) {
	t := now()
	minute := t - t%60000
	phones.Lock()
	cp := p
	phones.latest[agentID], phones.at[agentID] = &cp, t
	save := phones.saved[agentID] != minute
	phones.saved[agentID] = minute
	phones.Unlock()
	if save {
		var rsrp, sinr int
		tech := ""
		if p.Cell != nil {
			rsrp, sinr, tech = p.Cell.RSRP, p.Cell.SINR, p.Cell.Tech
		}
		if _, err := s.db.Exec(`INSERT OR REPLACE INTO phone_metrics(agent_id,t,bat,chg,temp,rsrp,sinr,tech) VALUES(?,?,?,?,?,?,?,?)`,
			agentID, minute, p.Battery, b2i(p.Charging), round1(p.Temp), rsrp, sinr, tech); err != nil {
			log.Printf("save phone: %v", err)
		}
	}
	s.checkBattery(agentID, p)
}

// phoneLatest ger telefonens senaste värden om de är färska.
func (s *Server) phoneLatest(agentID string) *agentproto.Phone {
	phones.Lock()
	defer phones.Unlock()
	if p := phones.latest[agentID]; p != nil && now()-phones.at[agentID] < 5*60000 {
		cp := *p
		return &cp
	}
	return nil
}

// PhoneHistory är minutvärden för grafer.
type PhoneHistory struct {
	T    []int64   `json:"t"`
	Bat  []int     `json:"bat"`
	Chg  []bool    `json:"chg"`
	Temp []float64 `json:"temp"`
	RSRP []*int    `json:"rsrp"`
	SINR []*int    `json:"sinr"`
}

func (s *Server) phoneHistory(agentID string, from, to int64) *PhoneHistory {
	rows, err := s.db.Query(`SELECT t,bat,chg,temp,rsrp,sinr FROM phone_metrics WHERE agent_id=? AND t>? AND t<=? ORDER BY t`, agentID, from, to)
	if err != nil {
		return nil
	}
	defer rows.Close()
	h := &PhoneHistory{T: []int64{}, Bat: []int{}, Chg: []bool{}, Temp: []float64{}, RSRP: []*int{}, SINR: []*int{}}
	// långa perioder: högst ~1500 punkter
	step := (to - from) / 1500
	var last int64
	for rows.Next() {
		var t int64
		var bat, chg, rsrp, sinr int
		var temp float64
		if rows.Scan(&t, &bat, &chg, &temp, &rsrp, &sinr) != nil || (step > 60000 && t-last < step) {
			continue
		}
		last = t
		h.T = append(h.T, t)
		h.Bat = append(h.Bat, bat)
		h.Chg = append(h.Chg, chg == 1)
		h.Temp = append(h.Temp, temp)
		if rsrp != 0 {
			r, si := rsrp, sinr
			h.RSRP, h.SINR = append(h.RSRP, &r), append(h.SINR, &si)
		} else {
			h.RSRP, h.SINR = append(h.RSRP, nil), append(h.SINR, nil)
		}
	}
	if len(h.T) == 0 {
		return nil
	}
	return h
}

func (s *Server) prunePhones(aggDays int) {
	_, _ = s.db.Exec(`DELETE FROM phone_metrics WHERE t < ?`, now()-int64(aggDays)*86400000)
}

// checkBattery larmar när telefonens batteri är lågt och den inte laddar.
func (s *Server) checkBattery(agentID string, p agentproto.Phone) {
	name, host, rules := s.agentAlertInfo(agentID, alert.CondBattery)
	for _, r := range rules {
		met := p.Battery >= 0 && float64(p.Battery) < r.Threshold && !p.Charging
		s.setHostAlert(r, agentID, name, host, met,
			fmt.Sprintf(i18n.T("Battery at %d %% and not charging"), p.Battery),
			fmt.Sprintf(i18n.T("Battery at %d %%%s"), p.Battery, map[bool]string{true: i18n.T(", charging"), false: ""}[p.Charging]))
	}
}

// noteRoamGap kommer ihåg en roaming med mätt avbrott och kontrollerar larmet för långsam roaming.
func (s *Server) noteRoamGap(agentID string, r Roam) {
	phones.Lock()
	phones.slowRms[agentID] = r
	phones.Unlock()
	s.checkRoamGap(agentID)
}

// checkRoamGap: larmet är aktivt så länge den senaste roamingen var långsam och skedde inom regelns minuter.
func (s *Server) checkRoamGap(agentID string) {
	name, host, rules := s.agentAlertInfo(agentID, alert.CondRoamGap)
	if len(rules) == 0 {
		return
	}
	phones.Lock()
	r, ok := phones.slowRms[agentID]
	phones.Unlock()
	if !ok {
		return
	}
	for _, rule := range rules {
		met := float64(r.GapMs) > rule.Threshold && now()-r.T < int64(rule.Samples)*60000
		s.setHostAlert(rule, agentID, name, host, met,
			fmt.Sprintf(i18n.T("Roaming to %s took %d ms (limit %g ms)"), r.To, r.GapMs, rule.Threshold),
			i18n.T("Roaming is fast again"))
	}
}
