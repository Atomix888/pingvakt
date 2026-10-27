package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"pingvakt/core/agentproto"
	"pingvakt/core/alert"
	"pingvakt/core/i18n"
	"pingvakt/core/monitor"
	"pingvakt/core/snmp"
)

const snmpSchema = `
CREATE TABLE IF NOT EXISTS snmp_devices (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL, data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS snmp_dev5 (
  device_id TEXT NOT NULL, t INTEGER NOT NULL, ok INTEGER NOT NULL, n INTEGER NOT NULL, rtt REAL NOT NULL, cpu REAL NOT NULL, mem REAL NOT NULL,
  PRIMARY KEY (device_id, t)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS snmp_if5 (
  device_id TEXT NOT NULL, if_index INTEGER NOT NULL, t INTEGER NOT NULL, in_bps REAL NOT NULL, out_bps REAL NOT NULL,
  in_max REAL NOT NULL, out_max REAL NOT NULL, errs REAL NOT NULL, up INTEGER NOT NULL,
  PRIMARY KEY (device_id, if_index, t)) WITHOUT ROWID;
`

// SNMPDevice är en SNMP-enhet som en agent pollar.
type SNMPDevice struct {
	snmp.Device
	AgentID string `json:"agentId"`
}

// IfRate är beräknad trafik för en port.
type IfRate struct {
	snmp.If
	InBps  float64 `json:"inBps"`
	OutBps float64 `json:"outBps"`
	ErrMin float64 `json:"errMin"` // fel per minut (in + ut)
	Util   float64 `json:"util"`   // högsta av in/ut i procent av hastigheten (-1 = okänd)
}

type ratePoint struct {
	t             int64
	in, out, errs float64
	up            bool
}

type devState struct {
	last    *snmp.Poll
	prev    *snmp.Poll
	fails   int
	rates   map[int]IfRate
	recent  map[int][]ratePoint // senaste ~3 h per port
	bucket  int64
	acc     map[int]*ifAcc
	devAcc  devAcc
	lastErr string
}

type ifAcc struct {
	n                            int
	in, out, inMax, outMax, errs float64
	up                           bool
}

type devAcc struct {
	n, ok         int
	rtt, cpu, mem float64
	cpuN, memN    int
}

type snmpState struct {
	mu   sync.Mutex
	devs map[string]*SNMPDevice
	st   map[string]*devState
}

var snmps = &snmpState{devs: map[string]*SNMPDevice{}, st: map[string]*devState{}}

func (s *Server) loadSNMP() {
	rows, err := s.db.Query(`SELECT data FROM snmp_devices`)
	if err != nil {
		return
	}
	defer rows.Close()
	snmps.mu.Lock()
	defer snmps.mu.Unlock()
	for rows.Next() {
		var data string
		var d SNMPDevice
		if rows.Scan(&data) == nil && json.Unmarshal([]byte(data), &d) == nil {
			snmps.devs[d.ID] = &d
		}
	}
}

// snmpForAgent returnerar agentens enheter (skickas i konfigurationen).
func (s *Server) snmpForAgent(agentID string) []snmp.Device {
	snmps.mu.Lock()
	defer snmps.mu.Unlock()
	var out []snmp.Device
	for _, d := range snmps.devs {
		if d.AgentID == agentID {
			out = append(out, d.Device)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// snmpInfo ger namn och kund för ett "snmp:<enhet>[:<port>]"-ID (larm).
func (s *Server) snmpInfo(id string) (name, cust string, ok bool) {
	if !strings.HasPrefix(id, "snmp:") {
		return "", "", false
	}
	rest := strings.TrimPrefix(id, "snmp:")
	devID, port, _ := strings.Cut(rest, ":")
	snmps.mu.Lock()
	d, found := snmps.devs[devID]
	var label string
	if found && port != "" {
		if st := snmps.st[devID]; st != nil && st.last != nil {
			idx, _ := strconv.Atoi(port)
			for _, f := range st.last.Ifs {
				if f.Index == idx {
					label = f.Label()
				}
			}
		}
		if label == "" {
			label = "#" + port
		}
	}
	snmps.mu.Unlock()
	if !found {
		return id, "", true
	}
	s.mu.RLock()
	if a, ok := s.agents[d.AgentID]; ok {
		cust = s.customerOfAgent(a)
		site := s.sites[a.SiteID]
		name = s.customers[site.CustomerID].Name + " · " + site.Name + " · "
	}
	s.mu.RUnlock()
	name += devName(d.Device)
	if label != "" {
		name += " · " + label
	}
	return name, cust, true
}

func devName(d snmp.Device) string {
	if d.Name != "" {
		return d.Name
	}
	return d.Host
}

// counterDelta räknar skillnaden mellan två räknarvärden. Minskar räknaren har
// enheten startats om (eller 32-bitarsräknaren slagit runt) – då hoppas punkten över.
func counterDelta(cur, prev uint64) (uint64, bool) {
	if cur < prev {
		if prev < 1<<32 && cur < 1<<32 && prev-cur > 1<<31 {
			return cur + (1 << 32) - prev, true // 32-bitars omslag
		}
		return 0, false
	}
	return cur - prev, true
}

// ingestSNMP tar emot avläsningar från en agent.
func (s *Server) ingestSNMP(agentID string, polls []snmp.Poll) {
	sort.Slice(polls, func(i, j int) bool { return polls[i].T < polls[j].T })
	for _, p := range polls {
		snmps.mu.Lock()
		d, ok := snmps.devs[p.DeviceID]
		if !ok || d.AgentID != agentID {
			snmps.mu.Unlock()
			continue
		}
		st := snmps.st[p.DeviceID]
		if st == nil {
			st = &devState{rates: map[int]IfRate{}, recent: map[int][]ratePoint{}, acc: map[int]*ifAcc{}}
			snmps.st[p.DeviceID] = st
		}
		pc := p
		var flush []func()
		bucket := p.T - p.T%300000
		if st.bucket != 0 && bucket != st.bucket {
			flush = append(flush, s.snmpFlushFn(p.DeviceID, st))
		}
		st.bucket = bucket
		st.devAcc.n++
		if p.OK {
			st.fails = 0
			st.devAcc.ok++
			st.devAcc.rtt += p.RTT
			if p.CPU >= 0 {
				st.devAcc.cpu += p.CPU
				st.devAcc.cpuN++
			}
			if p.Mem >= 0 {
				st.devAcc.mem += p.Mem
				st.devAcc.memN++
			}
			if st.last != nil && st.last.OK && p.T > st.last.T {
				dt := float64(p.T-st.last.T) / 1000
				prev := map[int]snmp.If{}
				for _, f := range st.last.Ifs {
					prev[f.Index] = f
				}
				rates := map[int]IfRate{}
				for _, f := range p.Ifs {
					r := IfRate{If: f, Util: -1}
					if o, ok := prev[f.Index]; ok && dt > 0 {
						if di, ok1 := counterDelta(f.InOct, o.InOct); ok1 {
							r.InBps = float64(di) * 8 / dt
						}
						if do, ok1 := counterDelta(f.OutOct, o.OutOct); ok1 {
							r.OutBps = float64(do) * 8 / dt
						}
						var errs uint64
						for _, pair := range [][2]uint64{{f.InErr, o.InErr}, {f.OutErr, o.OutErr}} {
							if de, ok1 := counterDelta(pair[0], pair[1]); ok1 {
								errs += de
							}
						}
						r.ErrMin = float64(errs) / dt * 60
					}
					if f.Speed > 0 {
						r.Util = maxf(r.InBps, r.OutBps) * 100 / float64(f.Speed)
					}
					rates[f.Index] = r
					rp := ratePoint{t: p.T, in: r.InBps, out: r.OutBps, errs: r.ErrMin, up: f.Up()}
					st.recent[f.Index] = trimPoints(append(st.recent[f.Index], rp), p.T-3*3600*1000)
					a := st.acc[f.Index]
					if a == nil {
						a = &ifAcc{}
						st.acc[f.Index] = a
					}
					a.n++
					a.in += r.InBps
					a.out += r.OutBps
					a.inMax = maxf(a.inMax, r.InBps)
					a.outMax = maxf(a.outMax, r.OutBps)
					a.errs += r.ErrMin
					a.up = a.up || f.Up()
				}
				st.rates = rates
			} else {
				st.rates = map[int]IfRate{}
				for _, f := range p.Ifs {
					sp := -1.0
					st.rates[f.Index] = IfRate{If: f, Util: sp}
				}
			}
			st.prev, st.last = st.last, &pc
			st.lastErr = ""
		} else {
			st.fails++
			st.lastErr = p.Err
			if st.last == nil {
				st.last = &pc
			} else {
				cp := *st.last
				cp.OK, cp.Err, cp.T = false, p.Err, p.T
				st.last = &cp
			}
		}
		dev := *d
		snmps.mu.Unlock()
		for _, f := range flush {
			f()
		}
		s.checkSNMPAlerts(dev)
	}
	b, _ := json.Marshal(map[string]any{"type": "snmp", "agentId": agentID})
	s.mu.RLock()
	cust := ""
	if a, ok := s.agents[agentID]; ok {
		cust = s.customerOfAgent(a)
	}
	s.mu.RUnlock()
	s.broadcastRaw(cust, b)
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func trimPoints(p []ratePoint, cut int64) []ratePoint {
	k := 0
	for k < len(p) && p[k].t < cut {
		k++
	}
	if k > 0 {
		return append(p[:0:0], p[k:]...)
	}
	return p
}

// snmpFlushFn sparar avslutade 5-minutersvärden (anropas utan lås).
func (s *Server) snmpFlushFn(devID string, st *devState) func() {
	t := st.bucket
	da := st.devAcc
	accs := st.acc
	st.devAcc, st.acc = devAcc{}, map[int]*ifAcc{}
	return func() {
		if da.n > 0 {
			rtt, cpu, mem := -1.0, -1.0, -1.0
			if da.ok > 0 {
				rtt = da.rtt / float64(da.ok)
			}
			if da.cpuN > 0 {
				cpu = da.cpu / float64(da.cpuN)
			}
			if da.memN > 0 {
				mem = da.mem / float64(da.memN)
			}
			if _, err := s.db.Exec(`INSERT OR REPLACE INTO snmp_dev5(device_id,t,ok,n,rtt,cpu,mem) VALUES(?,?,?,?,?,?,?)`, devID, t, da.ok, da.n, round1(rtt), round1(cpu), round1(mem)); err != nil {
				log.Printf("save SNMP: %v", err)
			}
		}
		for idx, a := range accs {
			if a.n == 0 {
				continue
			}
			_, _ = s.db.Exec(`INSERT OR REPLACE INTO snmp_if5(device_id,if_index,t,in_bps,out_bps,in_max,out_max,errs,up) VALUES(?,?,?,?,?,?,?,?,?)`,
				devID, idx, t, a.in/float64(a.n), a.out/float64(a.n), a.inMax, a.outMax, a.errs/float64(a.n), b2i(a.up))
		}
	}
}

// flushSNMP sparar 5-minutersvärden som blivit gamla (anropas regelbundet).
func (s *Server) flushSNMP(force bool) {
	cur := now() - now()%300000
	var fns []func()
	snmps.mu.Lock()
	for id, st := range snmps.st {
		if st.bucket != 0 && (force || st.bucket < cur) && st.devAcc.n > 0 {
			fns = append(fns, s.snmpFlushFn(id, st))
		}
	}
	snmps.mu.Unlock()
	for _, f := range fns {
		f()
	}
}

func (s *Server) pruneSNMP(aggDays int) {
	cut := now() - int64(aggDays)*86400000
	_, _ = s.db.Exec(`DELETE FROM snmp_dev5 WHERE t < ?`, cut)
	_, _ = s.db.Exec(`DELETE FROM snmp_if5 WHERE t < ?`, cut)
	_, _ = s.db.Exec(`DELETE FROM snmp_devices WHERE agent_id NOT IN (SELECT id FROM agents)`)
}

// ---------- larm ----------

func (s *Server) checkSNMPAlerts(d SNMPDevice) {
	s.mu.RLock()
	a, ok := s.agents[d.AgentID]
	if !ok {
		s.mu.RUnlock()
		return
	}
	cust := s.customerOfAgent(a)
	var rules []Rule
	for _, r := range s.rules {
		switch r.Condition {
		case alert.CondSNMP, alert.CondIfDown, alert.CondIfUtil, alert.CondIfErrors:
			if r.CustomerID == "" || r.CustomerID == cust {
				rules = append(rules, r)
			}
		}
	}
	s.mu.RUnlock()
	if len(rules) == 0 {
		return
	}
	snmps.mu.Lock()
	st := snmps.st[d.ID]
	if st == nil {
		snmps.mu.Unlock()
		return
	}
	fails := st.fails
	lastErr := st.lastErr
	var ifs []snmp.If
	if st.last != nil {
		ifs = append(ifs, st.last.Ifs...)
	}
	recent := map[int][]ratePoint{}
	for k, v := range st.recent {
		recent[k] = append([]ratePoint(nil), v...)
	}
	reachable := fails == 0 && st.last != nil && st.last.OK
	snmps.mu.Unlock()
	watched := map[int]bool{}
	for _, w := range d.Watch {
		watched[w] = true
	}
	t := now()
	type check struct {
		key, tid, detail, clearDetail string
		met                           bool
	}
	for _, r := range rules {
		var checks []check
		devTID := "snmp:" + d.ID
		switch r.Condition {
		case alert.CondSNMP:
			need := r.Samples
			if need < 1 {
				need = 3
			}
			checks = append(checks, check{tid: devTID, met: fails >= need,
				detail: fmt.Sprintf(i18n.T("No SNMP response from %s (%d attempts): %s"), d.Host, fails, lastErr), clearDetail: i18n.T("SNMP responds again")})
		case alert.CondIfDown:
			if !reachable {
				continue
			}
			for _, f := range ifs {
				if watched[f.Index] {
					checks = append(checks, check{tid: fmt.Sprintf("%s:%d", devTID, f.Index), met: !f.Up() && f.Admin == 1,
						detail: fmt.Sprintf(i18n.T("Port %s is down"), f.Label()), clearDetail: fmt.Sprintf(i18n.T("Port %s is up again"), f.Label())})
				}
			}
		case alert.CondIfUtil:
			if !reachable {
				continue
			}
			window := int64(r.Samples) * 60000
			for _, f := range ifs {
				if f.Speed == 0 || (len(watched) > 0 && !watched[f.Index]) || !f.Up() {
					continue
				}
				pts := recent[f.Index]
				var sumIn, sumOut float64
				n := 0
				var first int64
				for _, p := range pts {
					if p.t >= t-window {
						if first == 0 {
							first = p.t
						}
						sumIn += p.in
						sumOut += p.out
						n++
					}
				}
				if n == 0 {
					continue
				}
				util := maxf(sumIn, sumOut) / float64(n) * 100 / float64(f.Speed)
				covered := t-first >= window*7/10 || r.Samples <= 1
				checks = append(checks, check{tid: fmt.Sprintf("%s:%d", devTID, f.Index), met: covered && util > r.Threshold,
					detail: fmt.Sprintf(i18n.T("Port %s at %.0f %% of %s for %d min"), f.Label(), util, speedText(f.Speed), r.Samples), clearDetail: fmt.Sprintf(i18n.T("Port %s traffic is normal again"), f.Label())})
			}
		case alert.CondIfErrors:
			if !reachable {
				continue
			}
			for _, f := range ifs {
				pts := recent[f.Index]
				if len(pts) == 0 || !f.Up() {
					continue
				}
				e := pts[len(pts)-1].errs
				checks = append(checks, check{tid: fmt.Sprintf("%s:%d", devTID, f.Index), met: e > r.Threshold,
					detail: fmt.Sprintf(i18n.T("Port %s has %.0f errors per minute"), f.Label(), e), clearDetail: fmt.Sprintf(i18n.T("Port %s has no more errors"), f.Label())})
			}
		}
		name, _, _ := s.snmpInfo(devTID)
		for _, c := range checks {
			key := r.ID + "|" + c.tid
			s.host.mu.Lock()
			since, active := s.host.alerts[key]
			kind, detail := "", c.detail
			switch {
			case c.met && !active:
				s.host.alerts[key] = t
				kind = "fire"
			case !c.met && active:
				delete(s.host.alerts, key)
				if r.OnClear {
					kind = "clear"
					detail = fmt.Sprintf(i18n.T("%s (the alert lasted %s)"), c.clearDetail, time.Duration(t-since)*time.Millisecond/time.Second*time.Second)
				}
			}
			s.host.mu.Unlock()
			if kind == "" {
				continue
			}
			tn := name
			if c.tid != devTID {
				tn, _, _ = s.snmpInfo(c.tid)
			}
			s.eng.Fire(r.Alert, alert.Event{Kind: kind, AlertID: r.ID, AlertName: r.Name, TargetID: c.tid, TargetName: tn, Host: d.Host,
				Time: t, Detail: detail, Popup: r.Popup, Sound: r.Sound && kind == "fire", SoundName: r.SoundName})
		}
	}
}

func speedText(bps uint64) string {
	switch {
	case bps >= 1_000_000_000:
		return fmt.Sprintf("%g Gbit/s", float64(bps)/1e9)
	case bps >= 1_000_000:
		return fmt.Sprintf("%g Mbit/s", float64(bps)/1e6)
	}
	return fmt.Sprintf("%g kbit/s", float64(bps)/1e3)
}

// ---------- API ----------

// SNMPView är en enhet med senaste status.
type SNMPView struct {
	SNMPDevice
	OK       bool     `json:"ok"`
	Err      string   `json:"err,omitempty"`
	T        int64    `json:"t"`
	SysName  string   `json:"sysName,omitempty"`
	SysDescr string   `json:"sysDescr,omitempty"`
	Uptime   int64    `json:"uptime,omitempty"`
	CPU      float64  `json:"cpu"`
	Mem      float64  `json:"mem"`
	RTT      float64  `json:"rtt"`
	Ports    int      `json:"ports"`
	PortsUp  int      `json:"portsUp"`
	Ifs      []IfRate `json:"ifs,omitempty"`
	Customer string   `json:"customerId"`
}

func (s *Server) snmpView(d SNMPDevice, withIfs bool) SNMPView {
	v := SNMPView{SNMPDevice: d, CPU: -1, Mem: -1}
	v.AuthPass, v.PrivPass, v.Community = maskIf(v.AuthPass), maskIf(v.PrivPass), maskIf(v.Community)
	snmps.mu.Lock()
	if st := snmps.st[d.ID]; st != nil && st.last != nil {
		l := st.last
		v.OK, v.Err, v.T, v.SysName, v.SysDescr, v.Uptime, v.CPU, v.Mem, v.RTT = l.OK && st.fails == 0, st.lastErr, l.T, l.SysName, l.SysDescr, l.Uptime, l.CPU, l.Mem, l.RTT
		for _, f := range l.Ifs {
			if f.Admin == 1 {
				v.Ports++
				if f.Up() {
					v.PortsUp++
				}
			}
		}
		if withIfs {
			for _, f := range l.Ifs {
				r, ok := st.rates[f.Index]
				if !ok {
					r = IfRate{If: f, Util: -1}
				}
				r.If = f
				v.Ifs = append(v.Ifs, r)
			}
		}
	}
	snmps.mu.Unlock()
	s.mu.RLock()
	if a, ok := s.agents[d.AgentID]; ok {
		v.Customer = s.customerOfAgent(a)
	}
	s.mu.RUnlock()
	return v
}

func maskIf(s string) string {
	if s != "" {
		return secretMask
	}
	return ""
}

func (s *Server) handleSNMPList(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	snmps.mu.Lock()
	var list []SNMPDevice
	for _, d := range snmps.devs {
		list = append(list, *d)
	}
	snmps.mu.Unlock()
	out := []SNMPView{}
	for _, d := range list {
		v := s.snmpView(d, false)
		if canSee(u, v.Customer) {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return devName(out[i].Device) < devName(out[j].Device) })
	writeJSON(w, out)
}

func (s *Server) snmpVisible(r *http.Request, id string) (SNMPDevice, bool) {
	snmps.mu.Lock()
	d, ok := snmps.devs[id]
	var cp SNMPDevice
	if ok {
		cp = *d
	}
	snmps.mu.Unlock()
	if !ok {
		return cp, false
	}
	s.mu.RLock()
	a, ok := s.agents[cp.AgentID]
	vis := ok && canSee(userFrom(r), s.customerOfAgent(a))
	s.mu.RUnlock()
	return cp, vis
}

func (s *Server) handleSNMPGet(w http.ResponseWriter, r *http.Request) {
	d, ok := s.snmpVisible(r, r.PathValue("id"))
	if !ok {
		httpErr(w, 404, "the device does not exist")
		return
	}
	writeJSON(w, s.snmpView(d, true))
}

func (s *Server) handleSNMPSave(w http.ResponseWriter, r *http.Request) {
	var in SNMPDevice
	if !readJSON(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	if id != "" {
		in.ID = id
		snmps.mu.Lock()
		old, ok := snmps.devs[id]
		if ok {
			if in.AuthPass == secretMask {
				in.AuthPass = old.AuthPass
			}
			if in.PrivPass == secretMask {
				in.PrivPass = old.PrivPass
			}
			if in.Community == secretMask {
				in.Community = old.Community
			}
		}
		snmps.mu.Unlock()
		if !ok {
			httpErr(w, 404, "the device does not exist")
			return
		}
	} else {
		in.ID = monitor.NewID()
	}
	if err := s.agentManaged(in.AgentID); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	in.Normalize()
	if in.Host == "" {
		httpErr(w, 400, "enter a host name or IP address")
		return
	}
	b, _ := json.Marshal(in)
	if _, err := s.db.Exec(`INSERT INTO snmp_devices(id,agent_id,data) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET agent_id=excluded.agent_id, data=excluded.data`, in.ID, in.AgentID, string(b)); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	snmps.mu.Lock()
	var oldAgent string
	if o, ok := snmps.devs[in.ID]; ok {
		oldAgent = o.AgentID
	}
	cp := in
	snmps.devs[in.ID] = &cp
	snmps.mu.Unlock()
	s.db.BumpConfig(in.AgentID)
	if oldAgent != "" && oldAgent != in.AgentID {
		s.db.BumpConfig(oldAgent)
	}
	s.audit(r, "agent", in.AgentID, i18n.T("saved SNMP device ")+devName(in.Device))
	writeJSON(w, s.snmpView(in, false))
}

func (s *Server) handleSNMPDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	snmps.mu.Lock()
	d, ok := snmps.devs[id]
	delete(snmps.devs, id)
	delete(snmps.st, id)
	snmps.mu.Unlock()
	if !ok {
		httpErr(w, 404, "the device does not exist")
		return
	}
	s.host.mu.Lock()
	for k := range s.host.alerts {
		if strings.Contains(k, "|snmp:"+id) {
			delete(s.host.alerts, k)
		}
	}
	s.host.mu.Unlock()
	_, _ = s.db.Exec(`DELETE FROM snmp_devices WHERE id=?`, id)
	_, _ = s.db.Exec(`DELETE FROM snmp_dev5 WHERE device_id=?`, id)
	_, _ = s.db.Exec(`DELETE FROM snmp_if5 WHERE device_id=?`, id)
	s.db.BumpConfig(d.AgentID)
	s.audit(r, "agent", d.AgentID, i18n.T("deleted SNMP device ")+devName(d.Device))
	writeJSON(w, map[string]bool{"ok": true})
}

// handleSNMPTest ber agenten läsa av enheten en gång och väntar på svaret.
func (s *Server) handleSNMPTest(w http.ResponseWriter, r *http.Request) {
	var in SNMPDevice
	if !readJSON(w, r, &in) {
		return
	}
	if in.ID != "" {
		snmps.mu.Lock()
		if old, ok := snmps.devs[in.ID]; ok {
			if in.AuthPass == secretMask {
				in.AuthPass = old.AuthPass
			}
			if in.PrivPass == secretMask {
				in.PrivPass = old.PrivPass
			}
			if in.Community == secretMask {
				in.Community = old.Community
			}
		}
		snmps.mu.Unlock()
	}
	if err := s.agentManaged(in.AgentID); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	in.Normalize()
	dev := in.Device
	res, err := s.runAgentCommand(in.AgentID, agentproto.Command{Kind: agentproto.CmdSNMP, Host: in.Host, SNMP: &dev}, 70*time.Second)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if res.SNMP == nil {
		httpErr(w, 400, res.Err)
		return
	}
	writeJSON(w, res.SNMP)
}

// handleSNMPHistory ger trafik för en port (in/ut bit/s) eller enhetens CPU/minne.
func (s *Server) handleSNMPHistory(w http.ResponseWriter, r *http.Request) {
	d, ok := s.snmpVisible(r, r.PathValue("id"))
	if !ok {
		httpErr(w, 404, "the device does not exist")
		return
	}
	to, _ := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	if to <= 0 {
		to = now()
	}
	if from <= 0 || from >= to {
		from = to - 86400000
	}
	idx, err := strconv.Atoi(r.URL.Query().Get("if"))
	type series struct {
		T      []int64   `json:"t"`
		In     []float64 `json:"in"`
		Out    []float64 `json:"out"`
		InMax  []float64 `json:"inMax"`
		OutMax []float64 `json:"outMax"`
		Errs   []float64 `json:"errs"`
	}
	out := series{T: []int64{}, In: []float64{}, Out: []float64{}, InMax: []float64{}, OutMax: []float64{}, Errs: []float64{}}
	if err == nil {
		if to-from <= 3*3600*1000 {
			// fin upplösning från minnet
			snmps.mu.Lock()
			if st := snmps.st[d.ID]; st != nil {
				for _, p := range st.recent[idx] {
					if p.t >= from && p.t <= to {
						out.T = append(out.T, p.t)
						out.In = append(out.In, p.in)
						out.Out = append(out.Out, p.out)
						out.InMax = append(out.InMax, p.in)
						out.OutMax = append(out.OutMax, p.out)
						out.Errs = append(out.Errs, p.errs)
					}
				}
			}
			snmps.mu.Unlock()
		} else {
			s.flushSNMP(false)
			bucket := int64(300000)
			if to-from > 7*86400000 {
				bucket = 3600000
			}
			rows, err := s.db.Query(`SELECT (t/?)*? AS b, avg(in_bps), avg(out_bps), max(in_max), max(out_max), avg(errs) FROM snmp_if5
				WHERE device_id=? AND if_index=? AND t>=? AND t<=? GROUP BY b ORDER BY b`, bucket, bucket, d.ID, idx, from, to)
			if err == nil {
				for rows.Next() {
					var t int64
					var a, b, c, e, f float64
					if rows.Scan(&t, &a, &b, &c, &e, &f) == nil {
						out.T = append(out.T, t)
						out.In = append(out.In, a)
						out.Out = append(out.Out, b)
						out.InMax = append(out.InMax, c)
						out.OutMax = append(out.OutMax, e)
						out.Errs = append(out.Errs, f)
					}
				}
				rows.Close()
			}
		}
	}
	writeJSON(w, out)
}

// ---------- kommandon som väntar på svar ----------

var cmdWaiters = struct {
	sync.Mutex
	m map[string]chan agentproto.CommandResult
}{m: map[string]chan agentproto.CommandResult{}}

// runAgentCommand köar ett kommando till agenten och väntar på svaret.
func (s *Server) runAgentCommand(agentID string, c agentproto.Command, timeout time.Duration) (agentproto.CommandResult, error) {
	s.mu.RLock()
	a, ok := s.agents[agentID]
	online := ok && !s.offline[agentID]
	s.mu.RUnlock()
	if !ok {
		return agentproto.CommandResult{}, fmt.Errorf("%s", i18n.T("the agent does not exist"))
	}
	if !online || now()-a.LastSeen > 5*60000 {
		return agentproto.CommandResult{}, fmt.Errorf("%s", i18n.T("the agent is offline"))
	}
	c.ID = monitor.NewID()
	ch := make(chan agentproto.CommandResult, 1)
	cmdWaiters.Lock()
	cmdWaiters.m[c.ID] = ch
	cmdWaiters.Unlock()
	defer func() {
		cmdWaiters.Lock()
		delete(cmdWaiters.m, c.ID)
		cmdWaiters.Unlock()
	}()
	traces.mu.Lock()
	traces.queue[agentID] = append(traces.queue[agentID], c)
	traces.mu.Unlock()
	select {
	case res := <-ch:
		return res, nil
	case <-time.After(timeout):
		return agentproto.CommandResult{}, fmt.Errorf("%s", i18n.T("the agent did not answer in time"))
	}
}

// deliverWaiter lämnar ett svar till en väntande begäran. Returnerar false om ingen väntar.
func deliverWaiter(res agentproto.CommandResult) bool {
	cmdWaiters.Lock()
	ch, ok := cmdWaiters.m[res.ID]
	cmdWaiters.Unlock()
	if ok {
		select {
		case ch <- res:
		default:
		}
	}
	return ok
}
