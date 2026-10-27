package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/go-pdf/fpdf"
)

// Utökade delar av kundrapporten: SNMP, hastighetstester, Wi-Fi och integrationer.

type reportSNMPDev struct {
	Name, Host string
	Polls, OK  int
	CPU, Mem   float64 // medel, -1 = okänt
	CPUMax     float64
	Available  float64
}

type reportPort struct {
	Device, Port  string
	InAvg, OutAvg float64
	InMax, OutMax float64
	Speed         uint64
	UtilMax       float64 // -1 = okänd
	Errs          float64 // fel per minut i snitt
}

type reportSpeed struct {
	Agent          string
	N              int
	DownAvg, UpAvg float64
	DownMin, UpMin float64
	Latency        float64
}

type reportWiFi struct {
	Agent      string
	Minutes    int
	RSSIAvg    float64
	RSSIMin    int
	WeakPct    float64 // andel minuter under −70 dBm
	Roams, APs int
}

type reportInteg struct {
	Name, Kind    string
	Total, Online int
	Incidents     int
	Worst         []string // enheter med flest avbrott
}

type reportExtra struct {
	SNMP  []reportSNMPDev
	Ports []reportPort
	Speed []reportSpeed
	WiFi  []reportWiFi
	Integ []reportInteg
}

func (s *Server) buildReportExtra(rd *reportData, customerID string, f, t int64) {
	ex := &reportExtra{}
	agentIDs := map[string]string{}
	for _, a := range rd.Agents {
		agentIDs[a.Agent.ID] = a.Agent.Name
	}
	// SNMP
	snmps.mu.Lock()
	var devs []SNMPDevice
	for _, d := range snmps.devs {
		if _, ok := agentIDs[d.AgentID]; ok {
			devs = append(devs, *d)
		}
	}
	ifNames := map[string]map[int]snmpIfMeta{}
	for _, d := range devs {
		m := map[int]snmpIfMeta{}
		if st := snmps.st[d.ID]; st != nil && st.last != nil {
			for _, x := range st.last.Ifs {
				m[x.Index] = snmpIfMeta{label: x.Label(), speed: x.Speed}
			}
		}
		ifNames[d.ID] = m
	}
	snmps.mu.Unlock()
	s.flushSNMP(false)
	for _, d := range devs {
		// namn från senaste lagrade avläsning (minnet är tomt efter en omstart)
		if rows, err := s.db.Query(`SELECT if_index,name,speed FROM snmp_ifnames WHERE device_id=?`, d.ID); err == nil {
			for rows.Next() {
				var idx int
				var m snmpIfMeta
				var sp int64
				if rows.Scan(&idx, &m.label, &sp) == nil {
					m.speed = uint64(sp)
					if _, ok := ifNames[d.ID][idx]; !ok {
						ifNames[d.ID][idx] = m
					}
				}
			}
			rows.Close()
		}
	}
	sort.Slice(devs, func(i, j int) bool { return devName(devs[i].Device) < devName(devs[j].Device) })
	for _, d := range devs {
		rdv := reportSNMPDev{Name: devName(d.Device), Host: d.Host, CPU: -1, Mem: -1}
		var cpuS, memS float64
		var cpuN, memN int
		rows, err := s.db.Query(`SELECT ok,n,cpu,mem FROM snmp_dev5 WHERE device_id=? AND t>=? AND t<?`, d.ID, f, t)
		if err == nil {
			for rows.Next() {
				var ok, n int
				var cpu, mem float64
				if rows.Scan(&ok, &n, &cpu, &mem) != nil {
					continue
				}
				rdv.Polls += n
				rdv.OK += ok
				if cpu >= 0 {
					cpuS += cpu
					cpuN++
					if cpu > rdv.CPUMax {
						rdv.CPUMax = cpu
					}
				}
				if mem >= 0 {
					memS += mem
					memN++
				}
			}
			rows.Close()
		}
		if cpuN > 0 {
			rdv.CPU = cpuS / float64(cpuN)
		}
		if memN > 0 {
			rdv.Mem = memS / float64(memN)
		}
		rdv.Available = 100
		if rdv.Polls > 0 {
			rdv.Available = float64(rdv.OK) * 100 / float64(rdv.Polls)
		}
		ex.SNMP = append(ex.SNMP, rdv)
		rows, err = s.db.Query(`SELECT if_index, avg(in_bps), avg(out_bps), max(in_max), max(out_max), avg(errs) FROM snmp_if5 WHERE device_id=? AND t>=? AND t<? GROUP BY if_index`, d.ID, f, t)
		if err == nil {
			for rows.Next() {
				var idx int
				var p reportPort
				if rows.Scan(&idx, &p.InAvg, &p.OutAvg, &p.InMax, &p.OutMax, &p.Errs) != nil {
					continue
				}
				meta := ifNames[d.ID][idx]
				p.Device, p.Port, p.Speed, p.UtilMax = rdv.Name, meta.label, meta.speed, -1
				if p.Port == "" {
					p.Port = fmt.Sprintf("#%d", idx)
				}
				if p.Speed > 0 {
					p.UtilMax = maxf(p.InMax, p.OutMax) * 100 / float64(p.Speed)
				}
				if p.InMax+p.OutMax > 0 || p.Errs > 0 {
					ex.Ports = append(ex.Ports, p)
				}
			}
			rows.Close()
		}
	}
	sort.Slice(ex.Ports, func(i, j int) bool {
		return maxf(ex.Ports[i].InMax, ex.Ports[i].OutMax) > maxf(ex.Ports[j].InMax, ex.Ports[j].OutMax)
	})
	if len(ex.Ports) > 15 {
		ex.Ports = ex.Ports[:15]
	}
	// hastighetstester och Wi-Fi per agent
	for _, a := range rd.Agents {
		var sp reportSpeed
		sp.Agent = a.Agent.Name
		_ = s.db.QueryRow(`SELECT count(*), coalesce(avg(down),0), coalesce(avg(up),0), coalesce(min(down),0), coalesce(min(up),0), coalesce(avg(latency),0)
			FROM speedtests WHERE agent_id=? AND t>=? AND t<? AND err=''`, a.Agent.ID, f, t).Scan(&sp.N, &sp.DownAvg, &sp.UpAvg, &sp.DownMin, &sp.UpMin, &sp.Latency)
		if sp.N > 0 {
			ex.Speed = append(ex.Speed, sp)
		}
		var w reportWiFi
		w.Agent = a.Agent.Name
		var weak int
		_ = s.db.QueryRow(`SELECT count(*), coalesce(avg(rssi),0), coalesce(min(rssi_min),0), coalesce(sum(CASE WHEN rssi < -70 THEN 1 ELSE 0 END),0), count(DISTINCT bssid)
			FROM wifi_metrics WHERE agent_id=? AND t>=? AND t<?`, a.Agent.ID, f, t).Scan(&w.Minutes, &w.RSSIAvg, &w.RSSIMin, &weak, &w.APs)
		if w.Minutes == 0 {
			continue
		}
		w.WeakPct = float64(weak) * 100 / float64(w.Minutes)
		_ = s.db.QueryRow(`SELECT count(*) FROM wifi_roam WHERE agent_id=? AND t>=? AND t<?`, a.Agent.ID, f, t).Scan(&w.Roams)
		ex.WiFi = append(ex.WiFi, w)
	}
	// integrationer: nuvarande status och avbrott under perioden (ur larmhändelserna)
	integs.Lock()
	var ins []Integration
	for _, in := range integs.list {
		if in.CustomerID == customerID {
			ins = append(ins, *in)
		}
	}
	cur := map[string][2]int{}
	for _, in := range ins {
		if st := integs.st[in.ID]; st != nil {
			on := 0
			for _, d := range st.devices {
				if d.Online {
					on++
				}
			}
			cur[in.ID] = [2]int{len(st.devices), on}
		}
	}
	integs.Unlock()
	sort.Slice(ins, func(i, j int) bool { return ins[i].Name < ins[j].Name })
	for _, in := range ins {
		ri := reportInteg{Name: in.Name, Kind: vendorName(in.Kind), Total: cur[in.ID][0], Online: cur[in.ID][1]}
		count := map[string]int{}
		for _, e := range rd.Events {
			if e.Kind == "fire" && strings.HasPrefix(e.TargetID, "int:"+in.ID+":") {
				ri.Incidents++
				n := e.TargetName
				if i := strings.LastIndex(n, " · "); i >= 0 {
					n = n[i+3:]
				}
				count[n]++
			}
		}
		type kv struct {
			k string
			v int
		}
		var l []kv
		for k, v := range count {
			l = append(l, kv{k, v})
		}
		sort.Slice(l, func(i, j int) bool { return l[i].v > l[j].v })
		for i, x := range l {
			if i >= 3 {
				break
			}
			ri.Worst = append(ri.Worst, fmt.Sprintf("%s (%d)", x.k, x.v))
		}
		ex.Integ = append(ex.Integ, ri)
	}
	rd.Extra = ex
}

type snmpIfMeta struct {
	label string
	speed uint64
}

func (rd *reportData) fmtBps(v float64) string {
	switch {
	case v >= 1e9:
		return rd.fmtNum(v/1e9, 2) + " Gbit/s"
	case v >= 1e6:
		return rd.fmtNum(v/1e6, 1) + " Mbit/s"
	case v >= 1e3:
		return rd.fmtNum(v/1e3, 0) + " kbit/s"
	}
	return rd.fmtNum(v, 0) + " bit/s"
}

// renderExtra ritar de utökade avsnitten.
func (rd *reportData) renderExtra(pdf *fpdf.Fpdf, tr func(string) string, section func(string), tableHead func([]string, []float64, []string), setC func([3]int)) {
	ex := rd.Extra
	if ex == nil {
		return
	}
	row := func(widths []float64, aligns []string, cells []string, colors [][3]int) {
		if pdf.GetY() > 272 {
			pdf.AddPage()
		}
		pdf.SetFont("Helvetica", "", 8.5)
		for i, c := range cells {
			col := colInk
			if colors != nil && colors[i] != ([3]int{}) {
				col = colors[i]
			}
			setC(col)
			ln := 0
			if i == len(cells)-1 {
				ln = 1
			}
			pdf.CellFormat(widths[i], 6, tr(c), "", ln, aligns[i], false, 0, "")
		}
	}
	pctCol := func(v float64) [3]int {
		switch {
		case v >= 90:
			return colRed
		case v >= 75:
			return colAmber
		}
		return colInk
	}
	pct := func(v float64) string {
		if v < 0 {
			return "–"
		}
		return rd.fmtNum(v, 0) + " %"
	}
	if len(ex.SNMP) > 0 {
		section(rd.L("Network devices (SNMP)"))
		cols := []string{rd.L("Device"), rd.L("Address"), rd.L("Reachable"), rd.L("CPU avg"), "CPU max", rd.L("Memory avg")}
		w := []float64{52, 36, 24, 22, 22, 22}
		al := []string{"L", "L", "R", "R", "R", "R"}
		tableHead(cols, w, al)
		for _, d := range ex.SNMP {
			av := "–"
			if d.Polls > 0 {
				av = rd.fmtNum(d.Available, 2) + " %"
			}
			cpuMax := "–"
			if d.CPU >= 0 {
				cpuMax = pct(d.CPUMax)
			}
			row(w, al, []string{clip(d.Name, 34), clip(d.Host, 24), av, pct(d.CPU), cpuMax, pct(d.Mem)},
				[][3]int{{}, colMuted, availColor(d.Available), pctCol(d.CPU), pctCol(d.CPUMax), pctCol(d.Mem)})
		}
		pdf.Ln(3)
		if len(ex.Ports) > 0 {
			pdf.SetFont("Helvetica", "B", 9)
			setC(colInk)
			pdf.CellFormat(0, 6, tr(rd.L("Busiest ports")), "", 1, "L", false, 0, "")
			cols := []string{rd.L("Device"), rd.L("Port"), rd.L("In avg"), rd.L("Out avg"), rd.L("Peak"), rd.L("Max load"), rd.L("Errors/min")}
			w := []float64{38, 34, 22, 22, 22, 20, 20}
			al := []string{"L", "L", "R", "R", "R", "R", "R"}
			tableHead(cols, w, al)
			for _, p := range ex.Ports {
				errCol := colInk
				if p.Errs > 1 {
					errCol = colAmber
				}
				row(w, al, []string{clip(p.Device, 26), clip(p.Port, 24), rd.fmtBps(p.InAvg), rd.fmtBps(p.OutAvg), rd.fmtBps(maxf(p.InMax, p.OutMax)), pct(p.UtilMax), rd.fmtNum(p.Errs, 1)},
					[][3]int{{}, colMuted, {}, {}, {}, pctCol(p.UtilMax), errCol})
			}
			pdf.Ln(4)
		}
	}
	if len(ex.Speed) > 0 {
		section(rd.L("Speed tests"))
		cols := []string{"Agent", rd.L("Tests"), rd.L("Download avg"), rd.L("Lowest"), rd.L("Upload avg"), rd.L("Lowest"), rd.L("Latency")}
		w := []float64{44, 16, 26, 22, 26, 22, 22}
		al := []string{"L", "R", "R", "R", "R", "R", "R"}
		tableHead(cols, w, al)
		mb := func(v float64) string { return rd.fmtNum(v, 1) + " Mbit/s" }
		for _, s := range ex.Speed {
			row(w, al, []string{clip(s.Agent, 30), fmt.Sprint(s.N), mb(s.DownAvg), mb(s.DownMin), mb(s.UpAvg), mb(s.UpMin), rd.fmtMsR(s.Latency) + " ms"}, nil)
		}
		pdf.Ln(4)
	}
	if len(ex.WiFi) > 0 {
		section(rd.L("Wi-Fi quality"))
		cols := []string{"Agent", rd.L("Avg signal"), rd.L("Weakest"), rd.L("Below -70 dBm"), rd.L("Roams"), rd.L("Access points")}
		w := []float64{50, 26, 24, 30, 20, 28}
		al := []string{"L", "R", "R", "R", "R", "R"}
		tableHead(cols, w, al)
		rssiCol := func(v float64) [3]int {
			switch {
			case v < -75:
				return colRed
			case v < -67:
				return colAmber
			}
			return colGreen
		}
		for _, x := range ex.WiFi {
			weakCol := colInk
			if x.WeakPct > 10 {
				weakCol = colAmber
			}
			row(w, al, []string{clip(x.Agent, 34), rd.fmtNum(x.RSSIAvg, 0) + " dBm", fmt.Sprintf("%d dBm", x.RSSIMin), rd.fmtNum(x.WeakPct, 1) + " %", fmt.Sprint(x.Roams), fmt.Sprint(x.APs)},
				[][3]int{{}, rssiCol(x.RSSIAvg), rssiCol(float64(x.RSSIMin)), weakCol, {}, {}})
		}
		pdf.SetFont("Helvetica", "", 7.5)
		setC(colMuted)
		pdf.CellFormat(0, 5, tr(rd.L("-67 dBm or better is enough for calls and video. Below -75 dBm problems are common.")), "", 1, "L", false, 0, "")
		pdf.Ln(3)
	}
	if len(ex.Integ) > 0 {
		section(rd.L("Network equipment (UniFi, Meraki, Aruba)"))
		cols := []string{rd.L("Integration"), rd.L("System"), rd.L("Devices"), rd.L("Online now"), rd.L("Outages"), rd.L("Most outages")}
		w := []float64{36, 26, 18, 22, 18, 58}
		al := []string{"L", "L", "R", "R", "R", "L"}
		tableHead(cols, w, al)
		for _, x := range ex.Integ {
			onCol := colGreen
			if x.Online < x.Total {
				onCol = colRed
			}
			tot, on := fmt.Sprint(x.Total), fmt.Sprint(x.Online)
			if x.Total == 0 {
				tot, on, onCol = "–", "–", colMuted
			}
			row(w, al, []string{clip(x.Name, 24), x.Kind, tot, on, fmt.Sprint(x.Incidents), clip(strings.Join(x.Worst, ", "), 44)},
				[][3]int{{}, colMuted, {}, onCol, {}, colMuted})
		}
		pdf.Ln(4)
	}
}
