package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"

	"pingvakt/core/alert"
	"pingvakt/core/hostinfo"
)

// ---------- data ----------

type reportTarget struct {
	Target    Target
	Site      string
	Agent     string
	N, Lost   int
	Minutes   int // minuter med data
	DownMin   int // minuter då alla paket förlorades
	Avg, Max  float64
	HasAvg    bool
	Days      []reportDay
	Available float64 // procent
}

type reportDay struct {
	T    int64
	Avg  float64
	Loss float64
	N    int
}

type reportAgent struct {
	Agent           Agent
	Site            string
	CPUAvg, CPUMax  float64
	MemAvg, DiskMax float64
	Samples         int
}

type reportData struct {
	Customer Customer
	From, To time.Time
	Company  string
	Targets  []reportTarget
	Agents   []reportAgent
	Events   []alert.Event
	Maint    []Maintenance
}

func (s *Server) buildReport(customerID string, from, to time.Time) (*reportData, error) {
	s.mu.RLock()
	c, ok := s.customers[customerID]
	if !ok {
		s.mu.RUnlock()
		return nil, errNotFound
	}
	rd := &reportData{Customer: c, From: from, To: to, Company: s.settings.ReportName}
	var ts []Target
	agentSite := map[string]string{}
	var ags []Agent
	for _, a := range s.agents {
		if s.customerOfAgent(a) == customerID {
			agentSite[a.ID] = s.sites[a.SiteID].Name
			ags = append(ags, *a)
		}
	}
	agentName := map[string]string{}
	for _, a := range ags {
		agentName[a.ID] = a.Name
	}
	for _, t := range s.targets {
		if _, ok := agentSite[t.AgentID]; ok {
			ts = append(ts, *t)
		}
	}
	s.mu.RUnlock()
	sort.Slice(ts, func(i, j int) bool {
		if agentSite[ts[i].AgentID] != agentSite[ts[j].AgentID] {
			return agentSite[ts[i].AgentID] < agentSite[ts[j].AgentID]
		}
		return ts[i].Sort < ts[j].Sort
	})
	f, t := from.UnixMilli(), to.UnixMilli()
	for _, tg := range ts {
		rt := reportTarget{Target: tg, Site: agentSite[tg.AgentID], Agent: agentName[tg.AgentID]}
		rows, err := s.db.Query(`SELECT t,n,lost,sum,max FROM agg_1m WHERE target_id=? AND t>=? AND t<? ORDER BY t`, tg.ID, f, t)
		if err != nil {
			return nil, err
		}
		var sum float64
		dayIdx := map[int64]int{}
		type dacc struct {
			n, lost int
			sum     float64
		}
		var dayAcc []dacc
		for rows.Next() {
			var ts0 int64
			var n, lost int
			var sm float64
			var mx *float64
			if rows.Scan(&ts0, &n, &lost, &sm, &mx) != nil {
				continue
			}
			rt.N += n
			rt.Lost += lost
			rt.Minutes++
			if n > 0 && lost == n {
				rt.DownMin++
			}
			sum += sm
			if mx != nil && *mx > rt.Max {
				rt.Max = *mx
			}
			lt := time.UnixMilli(ts0).Local()
			day := time.Date(lt.Year(), lt.Month(), lt.Day(), 0, 0, 0, 0, time.Local).UnixMilli()
			i, ok := dayIdx[day]
			if !ok {
				i = len(dayAcc)
				dayIdx[day] = i
				dayAcc = append(dayAcc, dacc{})
				rt.Days = append(rt.Days, reportDay{T: day})
			}
			dayAcc[i].n += n
			dayAcc[i].lost += lost
			dayAcc[i].sum += sm
		}
		rows.Close()
		for i := range rt.Days {
			d := dayAcc[i]
			rt.Days[i].N = d.n
			if d.n-d.lost > 0 {
				rt.Days[i].Avg = d.sum / float64(d.n-d.lost)
			}
			if d.n > 0 {
				rt.Days[i].Loss = float64(d.lost) * 100 / float64(d.n)
			}
		}
		if rt.N-rt.Lost > 0 {
			rt.Avg = sum / float64(rt.N-rt.Lost)
			rt.HasAvg = true
		}
		rt.Available = 100
		if rt.Minutes > 0 {
			rt.Available = float64(rt.Minutes-rt.DownMin) * 100 / float64(rt.Minutes)
		}
		rd.Targets = append(rd.Targets, rt)
	}
	sort.Slice(ags, func(i, j int) bool { return agentSite[ags[i].ID]+ags[i].Name < agentSite[ags[j].ID]+ags[j].Name })
	for _, a := range ags {
		ra := reportAgent{Agent: a, Site: agentSite[a.ID]}
		rows, err := s.db.Query(`SELECT cpu,cpu_max,mem_used,mem_total,disks FROM host_metrics WHERE agent_id=? AND t>=? AND t<?`, a.ID, f, t)
		if err != nil {
			return nil, err
		}
		var cpu, mem float64
		for rows.Next() {
			var c0, cm float64
			var mu, mt int64
			var dj string
			if rows.Scan(&c0, &cm, &mu, &mt, &dj) != nil {
				continue
			}
			ra.Samples++
			cpu += c0
			if cm > ra.CPUMax {
				ra.CPUMax = cm
			}
			if mt > 0 {
				mem += float64(mu) * 100 / float64(mt)
			}
			var ds []hostinfo.Disk
			_ = json.Unmarshal([]byte(dj), &ds)
			for _, d := range ds {
				if p := d.Pct(); p > ra.DiskMax {
					ra.DiskMax = p
				}
			}
		}
		rows.Close()
		if ra.Samples > 0 {
			ra.CPUAvg, ra.MemAvg = cpu/float64(ra.Samples), mem/float64(ra.Samples)
		}
		rd.Agents = append(rd.Agents, ra)
	}
	rows, err := s.db.Query(`SELECT data FROM alert_events WHERE customer_id=? AND t>=? AND t<? ORDER BY t DESC LIMIT 200`, customerID, f, t)
	if err == nil {
		for rows.Next() {
			var data string
			var ev alert.Event
			if rows.Scan(&data) == nil && json.Unmarshal([]byte(data), &ev) == nil {
				rd.Events = append(rd.Events, ev)
			}
		}
		rows.Close()
	}
	s.ops.mu.Lock()
	for _, m := range s.ops.maint {
		if m.CustomerID == customerID && (m.Weekly || (m.End > f && m.Start < t)) {
			rd.Maint = append(rd.Maint, m)
		}
	}
	s.ops.mu.Unlock()
	return rd, nil
}

// ---------- PDF ----------

var (
	colInk    = [3]int{27, 31, 36}
	colMuted  = [3]int{95, 107, 122}
	colLine   = [3]int{223, 227, 232}
	colAccent = [3]int{15, 148, 132}
	colRed    = [3]int{217, 58, 55}
	colAmber  = [3]int{201, 138, 11}
	colGreen  = [3]int{31, 157, 85}
	colPanel  = [3]int{246, 247, 249}
)

func fmtDur(min int) string {
	if min <= 0 {
		return "0 min"
	}
	if min < 60 {
		return fmt.Sprintf("%d min", min)
	}
	h := min / 60
	if h < 48 {
		return fmt.Sprintf("%d h %d min", h, min%60)
	}
	return fmt.Sprintf("%d d %d h", h/24, h%24)
}

func fmtNum(v float64, dec int) string {
	return strings.Replace(strconv.FormatFloat(v, 'f', dec, 64), ".", ",", 1)
}

func fmtMsR(v float64) string {
	switch {
	case v < 1:
		return fmtNum(v, 2)
	case v < 10:
		return fmtNum(v, 1)
	}
	return fmtNum(math.Round(v), 0)
}

func availColor(p float64) [3]int {
	switch {
	case p < 99:
		return colRed
	case p < 99.9:
		return colAmber
	}
	return colGreen
}

func renderReportPDF(rd *reportData) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	tr := pdf.UnicodeTranslatorFromDescriptor("")
	pdf.SetMargins(16, 16, 16)
	pdf.SetAutoPageBreak(true, 18)
	pdf.SetTitle(tr("Driftrapport "+rd.Customer.Name), false)
	pdf.SetCreator("PingVakt", false)
	period := rd.From.Format("2006-01-02") + " – " + rd.To.Add(-time.Second).Format("2006-01-02")
	pdf.SetFooterFunc(func() {
		pdf.SetY(-12)
		pdf.SetFont("Helvetica", "", 8)
		pdf.SetTextColor(colMuted[0], colMuted[1], colMuted[2])
		pdf.CellFormat(0, 5, tr(rd.Customer.Name+" · "+period), "", 0, "L", false, 0, "")
		pdf.CellFormat(0, 5, tr(fmt.Sprintf("Sida %d av {nb}", pdf.PageNo())), "", 0, "R", false, 0, "")
	})
	pdf.AliasNbPages("{nb}")
	pdf.AddPage()
	setC := func(c [3]int) { pdf.SetTextColor(c[0], c[1], c[2]) }
	fill := func(c [3]int) { pdf.SetFillColor(c[0], c[1], c[2]) }
	draw := func(c [3]int) { pdf.SetDrawColor(c[0], c[1], c[2]) }
	pageW, _ := pdf.GetPageSize()
	cw := pageW - 32

	// rubrik
	fill(colAccent)
	pdf.Rect(16, 16, 3, 22, "F")
	pdf.SetXY(23, 16)
	pdf.SetFont("Helvetica", "B", 20)
	setC(colInk)
	pdf.CellFormat(0, 9, tr("Driftrapport"), "", 2, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 12)
	pdf.CellFormat(0, 7, tr(rd.Customer.Name), "", 2, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 9)
	setC(colMuted)
	sub := "Period " + period + " · skapad " + time.Now().Format("2006-01-02 15:04")
	if rd.Company != "" {
		sub = rd.Company + " · " + sub
	}
	pdf.CellFormat(0, 6, tr(sub), "", 2, "L", false, 0, "")
	pdf.Ln(6)

	// nyckeltal
	var mins, down, n, lost int
	for _, t := range rd.Targets {
		mins += t.Minutes
		down += t.DownMin
		n += t.N
		lost += t.Lost
	}
	avail := 100.0
	if mins > 0 {
		avail = float64(mins-down) * 100 / float64(mins)
	}
	fired := 0
	for _, e := range rd.Events {
		if e.Kind == "fire" {
			fired++
		}
	}
	lossPct := 0.0
	if n > 0 {
		lossPct = float64(lost) * 100 / float64(n)
	}
	kpis := []struct {
		label, value string
		col          [3]int
	}{
		{"Tillgänglighet", fmtNum(avail, 3) + " %", availColor(avail)},
		{"Total nertid", fmtDur(down), colInk},
		{"Paketförlust", fmtNum(lossPct, 2) + " %", colInk},
		{"Larm", strconv.Itoa(fired), colInk},
	}
	bw := (cw - 3*4) / 4
	y := pdf.GetY()
	for i, k := range kpis {
		x := 16 + float64(i)*(bw+4)
		fill(colPanel)
		draw(colLine)
		pdf.RoundedRect(x, y, bw, 20, 2, "1234", "FD")
		pdf.SetXY(x+4, y+3)
		pdf.SetFont("Helvetica", "", 8)
		setC(colMuted)
		pdf.CellFormat(bw-8, 4, tr(strings.ToUpper(k.label)), "", 2, "L", false, 0, "")
		pdf.SetFont("Helvetica", "B", 15)
		setC(k.col)
		pdf.CellFormat(bw-8, 9, tr(k.value), "", 0, "L", false, 0, "")
	}
	pdf.SetY(y + 26)
	pdf.SetFont("Helvetica", "", 9)
	setC(colMuted)
	pdf.MultiCell(0, 4.5, tr(fmt.Sprintf("%d mål övervakas av %d agenter. Tillgänglighet räknas per minut: en minut räknas som nertid när inga paket kom fram.", len(rd.Targets), len(rd.Agents))), "", "L", false)
	pdf.Ln(4)

	section := func(title string) {
		if pdf.GetY() > 250 {
			pdf.AddPage()
		}
		pdf.SetFont("Helvetica", "B", 12)
		setC(colInk)
		pdf.CellFormat(0, 8, tr(title), "", 1, "L", false, 0, "")
		draw(colLine)
		pdf.Line(16, pdf.GetY(), 16+cw, pdf.GetY())
		pdf.Ln(2)
	}
	tableHead := func(cols []string, widths []float64, aligns []string) {
		pdf.SetFont("Helvetica", "B", 8)
		setC(colMuted)
		fill(colPanel)
		for i, c := range cols {
			pdf.CellFormat(widths[i], 6, tr(c), "", 0, aligns[i], true, 0, "")
		}
		pdf.Ln(-1)
	}

	// mål
	section("Mål")
	cols := []string{"Mål", "Site", "Tillgänglighet", "Nertid", "Förlust", "Snitt ms", "Max ms"}
	widths := []float64{50, 30, 24, 22, 18, 17, 17}
	aligns := []string{"L", "L", "R", "R", "R", "R", "R"}
	tableHead(cols, widths, aligns)
	for i, t := range rd.Targets {
		if pdf.GetY() > 270 {
			pdf.AddPage()
			tableHead(cols, widths, aligns)
		}
		if i%2 == 1 {
			fill([3]int{251, 251, 252})
		} else {
			fill([3]int{255, 255, 255})
		}
		name := t.Target.Name
		if name == "" {
			name = t.Target.Host
		}
		pdf.SetFont("Helvetica", "", 8.5)
		setC(colInk)
		label := name
		if t.Target.Name != "" && t.Target.Name != t.Target.Host {
			label = name + "  (" + t.Target.Host + ")"
		}
		pdf.CellFormat(widths[0], 6, tr(clip(label, 42)), "", 0, "L", true, 0, "")
		setC(colMuted)
		pdf.CellFormat(widths[1], 6, tr(clip(t.Site, 22)), "", 0, "L", true, 0, "")
		if t.Minutes == 0 {
			pdf.CellFormat(widths[2]+widths[3]+widths[4]+widths[5]+widths[6], 6, tr("ingen data under perioden"), "", 1, "R", true, 0, "")
			continue
		}
		setC(availColor(t.Available))
		pdf.SetFont("Helvetica", "B", 8.5)
		pdf.CellFormat(widths[2], 6, fmtNum(t.Available, 3)+" %", "", 0, "R", true, 0, "")
		pdf.SetFont("Helvetica", "", 8.5)
		setC(colInk)
		pdf.CellFormat(widths[3], 6, tr(fmtDur(t.DownMin)), "", 0, "R", true, 0, "")
		lp := 0.0
		if t.N > 0 {
			lp = float64(t.Lost) * 100 / float64(t.N)
		}
		pdf.CellFormat(widths[4], 6, fmtNum(lp, 2)+" %", "", 0, "R", true, 0, "")
		avg := "–"
		if t.HasAvg {
			avg = fmtMsR(t.Avg)
		}
		pdf.CellFormat(widths[5], 6, tr(avg), "", 0, "R", true, 0, "")
		pdf.CellFormat(widths[6], 6, fmtMsR(t.Max), "", 1, "R", true, 0, "")
	}
	pdf.Ln(4)

	// grafer per mål
	var withData []reportTarget
	for _, t := range rd.Targets {
		if len(t.Days) > 0 {
			withData = append(withData, t)
		}
	}
	if len(withData) > 0 {
		pdf.AddPage()
		section("Latens och förlust per dygn")
		gw, gh := (cw-6)/2, 38.0
		rowY := pdf.GetY()
		for i, t := range withData {
			if i >= 24 {
				break
			}
			col := i % 2
			if col == 0 {
				if pdf.GetY()+gh+10 > 280 {
					pdf.AddPage()
				}
				rowY = pdf.GetY()
			}
			x := 16 + float64(col)*(gw+6)
			y := rowY
			name := t.Target.Name
			if name == "" {
				name = t.Target.Host
			}
			pdf.SetXY(x, y)
			pdf.SetFont("Helvetica", "B", 8.5)
			setC(colInk)
			pdf.CellFormat(gw, 5, tr(clip(name, 48)), "", 0, "L", false, 0, "")
			drawDayChart(pdf, t.Days, x, y+6, gw, gh-8, rd.From, rd.To)
			if col == 1 || i == len(withData)-1 {
				pdf.SetY(y + gh + 4)
			}
		}
		pdf.SetFont("Helvetica", "", 7.5)
		setC(colMuted)
		pdf.CellFormat(0, 4, tr("Linje: snittlatens per dygn (ms). Röda staplar: paketförlust per dygn (skala till höger, %)."), "", 1, "L", false, 0, "")
		pdf.Ln(3)
	}

	// agenter
	if len(rd.Agents) > 0 {
		section("Agenter – CPU, minne och disk")
		cols := []string{"Agent", "Site", "Version", "CPU snitt", "CPU max", "Minne snitt", "Disk max"}
		widths := []float64{44, 34, 20, 20, 20, 20, 20}
		aligns := []string{"L", "L", "L", "R", "R", "R", "R"}
		tableHead(cols, widths, aligns)
		for _, a := range rd.Agents {
			pdf.SetFont("Helvetica", "", 8.5)
			setC(colInk)
			fill([3]int{255, 255, 255})
			pdf.CellFormat(widths[0], 6, tr(clip(a.Agent.Name, 30)), "", 0, "L", false, 0, "")
			setC(colMuted)
			pdf.CellFormat(widths[1], 6, tr(clip(a.Site, 24)), "", 0, "L", false, 0, "")
			pdf.CellFormat(widths[2], 6, tr(a.Agent.Version), "", 0, "L", false, 0, "")
			setC(colInk)
			if a.Samples == 0 {
				pdf.CellFormat(widths[3]+widths[4]+widths[5]+widths[6], 6, tr("inga värden"), "", 1, "R", false, 0, "")
				continue
			}
			pctCell := func(w, v float64, last bool) {
				c := colInk
				if v >= 90 {
					c = colRed
				} else if v >= 75 {
					c = colAmber
				}
				setC(c)
				ln := 0
				if last {
					ln = 1
				}
				pdf.CellFormat(w, 6, fmtNum(v, 0)+" %", "", ln, "R", false, 0, "")
			}
			pctCell(widths[3], a.CPUAvg, false)
			pctCell(widths[4], a.CPUMax, false)
			pctCell(widths[5], a.MemAvg, false)
			pctCell(widths[6], a.DiskMax, true)
		}
		pdf.Ln(4)
	}

	// servicefönster
	if len(rd.Maint) > 0 {
		section("Servicefönster")
		pdf.SetFont("Helvetica", "", 8.5)
		for _, m := range rd.Maint {
			when := ""
			if m.Weekly {
				days := []string{"sön", "mån", "tis", "ons", "tor", "fre", "lör"}
				var ds []string
				for _, d := range m.Days {
					if d >= 0 && d < 7 {
						ds = append(ds, days[d])
					}
				}
				when = fmt.Sprintf("Varje vecka %s %02d:%02d, %s", strings.Join(ds, ", "), m.StartMin/60, m.StartMin%60, fmtDur(m.DurationMin))
			} else {
				when = time.UnixMilli(m.Start).Format("2006-01-02 15:04") + " – " + time.UnixMilli(m.End).Format("2006-01-02 15:04")
			}
			note := m.Note
			if note == "" {
				note = "Servicefönster"
			}
			setC(colInk)
			pdf.CellFormat(70, 5.5, tr(clip(note, 48)), "", 0, "L", false, 0, "")
			setC(colMuted)
			pdf.CellFormat(0, 5.5, tr(when), "", 1, "L", false, 0, "")
		}
		pdf.Ln(4)
	}

	// larmhändelser
	section("Larmhändelser")
	if len(rd.Events) == 0 {
		pdf.SetFont("Helvetica", "", 9)
		setC(colMuted)
		pdf.CellFormat(0, 6, tr("Inga larm under perioden."), "", 1, "L", false, 0, "")
	} else {
		cols := []string{"Tid", "Händelse", "Mål", "Larm"}
		widths := []float64{30, 20, 82, 46}
		aligns := []string{"L", "L", "L", "L"}
		tableHead(cols, widths, aligns)
		kinds := map[string]string{"fire": "Utlöst", "repeat": "Pågår", "clear": "Upphört"}
		for i, e := range rd.Events {
			if i >= 80 {
				pdf.SetFont("Helvetica", "", 8)
				setC(colMuted)
				pdf.CellFormat(0, 5, tr(fmt.Sprintf("… och %d händelser till.", len(rd.Events)-80)), "", 1, "L", false, 0, "")
				break
			}
			if pdf.GetY() > 272 {
				pdf.AddPage()
				tableHead(cols, widths, aligns)
			}
			pdf.SetFont("Helvetica", "", 8)
			setC(colMuted)
			pdf.CellFormat(widths[0], 5.5, time.UnixMilli(e.Time).Format("2006-01-02 15:04"), "", 0, "L", false, 0, "")
			c := colInk
			switch e.Kind {
			case "fire", "repeat":
				c = colRed
			case "clear":
				c = colGreen
			}
			setC(c)
			k := kinds[e.Kind]
			if e.Suppressed {
				k += "*"
			}
			pdf.CellFormat(widths[1], 5.5, tr(k), "", 0, "L", false, 0, "")
			setC(colInk)
			tn := e.TargetName
			if i := strings.LastIndex(tn, " · "); i >= 0 && strings.HasPrefix(tn, rd.Customer.Name) {
				tn = strings.TrimPrefix(tn, rd.Customer.Name+" · ")
			}
			pdf.CellFormat(widths[2], 5.5, tr(clip(tn, 58)), "", 0, "L", false, 0, "")
			pdf.CellFormat(widths[3], 5.5, tr(clip(e.AlertName, 30)), "", 1, "L", false, 0, "")
		}
		pdf.SetFont("Helvetica", "", 7.5)
		setC(colMuted)
		pdf.CellFormat(0, 5, tr("* Larmet skickades inte (servicefönster eller beroende)."), "", 1, "L", false, 0, "")
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// drawDayChart ritar snittlatens (linje) och förlust (staplar) per dygn.
func drawDayChart(pdf *fpdf.Fpdf, days []reportDay, x, y, w, h float64, from, to time.Time) {
	pdf.SetDrawColor(colLine[0], colLine[1], colLine[2])
	pdf.SetFillColor(252, 252, 253)
	pdf.Rect(x, y, w, h, "FD")
	maxV := 0.0
	for _, d := range days {
		if d.Avg > maxV {
			maxV = d.Avg
		}
	}
	maxV = niceMax(maxV)
	span := float64(to.Sub(from).Milliseconds())
	if span <= 0 {
		return
	}
	xOf := func(t int64) float64 { return x + (float64(t-from.UnixMilli())+43200000)/span*w }
	// stödlinjer
	pdf.SetFont("Helvetica", "", 6)
	pdf.SetTextColor(colMuted[0], colMuted[1], colMuted[2])
	for i := 0; i <= 2; i++ {
		v := maxV * float64(i) / 2
		yy := y + h - h*float64(i)/2
		pdf.SetDrawColor(236, 238, 241)
		if i > 0 && i < 2 {
			pdf.Line(x, yy, x+w, yy)
		}
		pdf.SetXY(x-9, yy-1.5)
		pdf.CellFormat(8, 3, fmtMsR(v), "", 0, "R", false, 0, "")
	}
	// förlust
	bw := w / (span / 86400000) * 0.6
	pdf.SetFillColor(colRed[0], colRed[1], colRed[2])
	for _, d := range days {
		if d.Loss <= 0 {
			continue
		}
		bh := math.Max(0.6, h*math.Min(d.Loss, 100)/100)
		pdf.Rect(xOf(d.T)-bw/2, y+h-bh, bw, bh, "F")
	}
	// latens
	pdf.SetDrawColor(colAccent[0], colAccent[1], colAccent[2])
	pdf.SetLineWidth(0.5)
	var px, py float64
	first := true
	for _, d := range days {
		if d.N == 0 || d.Avg <= 0 && d.Loss >= 100 {
			first = true
			continue
		}
		xx := xOf(d.T)
		yy := y + h - h*d.Avg/maxV
		if !first {
			pdf.Line(px, py, xx, yy)
		}
		pdf.SetFillColor(colAccent[0], colAccent[1], colAccent[2])
		pdf.Circle(xx, yy, 0.7, "F")
		px, py, first = xx, yy, false
	}
	pdf.SetLineWidth(0.2)
	// datum
	pdf.SetFont("Helvetica", "", 6)
	pdf.SetXY(x, y+h+0.5)
	pdf.CellFormat(w/2, 3, from.Format("2/1"), "", 0, "L", false, 0, "")
	pdf.CellFormat(w/2, 3, to.Add(-time.Second).Format("2/1"), "", 0, "R", false, 0, "")
}

func niceMax(v float64) float64 {
	if v <= 0 {
		return 1
	}
	e := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if v <= m*e {
			return m * e
		}
	}
	return 10 * e
}

// ---------- perioder och API ----------

// reportPeriod tolkar ?period= (last-month, this-month, last-week, last-30) eller from/to.
func reportPeriod(r *http.Request) (time.Time, time.Time) {
	nowT := time.Now()
	today := time.Date(nowT.Year(), nowT.Month(), nowT.Day(), 0, 0, 0, 0, time.Local)
	monthStart := time.Date(nowT.Year(), nowT.Month(), 1, 0, 0, 0, 0, time.Local)
	weekStart := today.AddDate(0, 0, -((int(today.Weekday()) + 6) % 7))
	switch r.URL.Query().Get("period") {
	case "this-month":
		return monthStart, nowT
	case "last-week":
		return weekStart.AddDate(0, 0, -7), weekStart
	case "this-week":
		return weekStart, nowT
	case "last-30":
		return today.AddDate(0, 0, -30), nowT
	case "custom":
		f, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
		t, _ := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
		if f > 0 && t > f {
			return time.UnixMilli(f), time.UnixMilli(t)
		}
	}
	return monthStart.AddDate(0, -1, 0), monthStart
}

func (s *Server) handleCustomerReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !canSee(userFrom(r), id) {
		httpErr(w, 404, "kunden finns inte")
		return
	}
	from, to := reportPeriod(r)
	rd, err := s.buildReport(id, from, to)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	b, err := renderReportPDF(rd)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	disp := "inline"
	if r.URL.Query().Get("download") == "1" {
		disp = "attachment"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename="%s"`, disp, reportFileName(rd)))
	_, _ = w.Write(b)
}

func reportFileName(rd *reportData) string {
	return "driftrapport-" + safeFile(strings.ReplaceAll(rd.Customer.Name, " ", "-")) + "-" + rd.From.Format("2006-01-02") + ".pdf"
}

// sendReport skapar och mejlar rapporten för en period.
func (s *Server) sendReport(c Customer, from, to time.Time, toAddr string) error {
	rcpt := alert.SplitAddresses(toAddr)
	if len(rcpt) == 0 {
		return fmt.Errorf("ange minst en mottagare")
	}
	rd, err := s.buildReport(c.ID, from, to)
	if err != nil {
		return err
	}
	b, err := renderReportPDF(rd)
	if err != nil {
		return err
	}
	s.mu.RLock()
	cfg := s.settings.SMTP
	company := s.settings.ReportName
	s.mu.RUnlock()
	period := from.Format("2006-01-02") + " – " + to.Add(-time.Second).Format("2006-01-02")
	body := fmt.Sprintf("Hej!\r\n\r\nHär kommer driftrapporten för %s, perioden %s.\r\n\r\n", c.Name, period)
	var mins, down int
	for _, t := range rd.Targets {
		mins += t.Minutes
		down += t.DownMin
	}
	if mins > 0 {
		body += fmt.Sprintf("Tillgänglighet: %s %%\r\nTotal nertid: %s\r\n\r\n", fmtNum(float64(mins-down)*100/float64(mins), 3), fmtDur(down))
	}
	if company != "" {
		body += "Med vänliga hälsningar\r\n" + company + "\r\n"
	}
	return alert.SendWithAttachments(cfg, rcpt, "Driftrapport "+c.Name+" "+period, body,
		alert.Attachment{Name: reportFileName(rd), ContentType: "application/pdf", Data: b})
}

func (s *Server) handleSendReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		To     string `json:"to"`
		Period string `json:"period"`
		From   int64  `json:"from"`
		ToT    int64  `json:"toT"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	s.mu.RLock()
	c, ok := s.customers[id]
	s.mu.RUnlock()
	if !ok {
		httpErr(w, 404, "kunden finns inte")
		return
	}
	q := r.URL.Query()
	q.Set("period", in.Period)
	q.Set("from", strconv.FormatInt(in.From, 10))
	q.Set("to", strconv.FormatInt(in.ToT, 10))
	r.URL.RawQuery = q.Encode()
	from, to := reportPeriod(r)
	if err := s.sendReport(c, from, to, in.To); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	s.audit(r, "rapport", id, "skickade rapport till "+in.To)
	writeJSON(w, map[string]bool{"ok": true})
}

// reportLoop skickar schemalagda rapporter: veckovis måndagar och månadsvis den 1:a, från kl 07.
func (s *Server) reportLoop(stop <-chan struct{}) {
	tk := time.NewTicker(10 * time.Minute)
	defer tk.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
		}
		nowT := time.Now()
		if nowT.Hour() < 7 {
			continue
		}
		today := time.Date(nowT.Year(), nowT.Month(), nowT.Day(), 0, 0, 0, 0, time.Local)
		s.mu.RLock()
		var list []Customer
		for _, c := range s.customers {
			if c.ReportFreq != "" && c.ReportTo != "" {
				list = append(list, c)
			}
		}
		s.mu.RUnlock()
		for _, c := range list {
			var from, to time.Time
			switch c.ReportFreq {
			case "weekly":
				to = today.AddDate(0, 0, -((int(today.Weekday()) + 6) % 7))
				from = to.AddDate(0, 0, -7)
			case "monthly":
				to = time.Date(nowT.Year(), nowT.Month(), 1, 0, 0, 0, 0, time.Local)
				from = to.AddDate(0, -1, 0)
			default:
				continue
			}
			if c.ReportLast >= to.UnixMilli() {
				continue
			}
			if err := s.sendReport(c, from, to, c.ReportTo); err != nil {
				log.Printf("rapport %s: %v", c.Name, err)
				continue
			}
			_, _ = s.db.Exec(`UPDATE customers SET report_last=? WHERE id=?`, to.UnixMilli(), c.ID)
			s.mu.Lock()
			if cc, ok := s.customers[c.ID]; ok {
				cc.ReportLast = to.UnixMilli()
				s.customers[c.ID] = cc
			}
			s.mu.Unlock()
			log.Printf("rapport skickad: %s (%s – %s)", c.Name, from.Format("2006-01-02"), to.Format("2006-01-02"))
		}
	}
}
