package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"pingvakt/core/monitor"
	"pingvakt/core/snmp"
)

// TopoNode är en enhet på nätverkskartan.
type TopoNode struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"` // snmp, integ, neighbor
	Type   string `json:"type"` // switch, ap, gateway, other
	Name   string `json:"name"`
	IP     string `json:"ip,omitempty"`
	MAC    string `json:"mac,omitempty"`
	Model  string `json:"model,omitempty"`
	Status string `json:"status"`           // up, down, unknown
	Ref    string `json:"ref,omitempty"`    // SNMP-enhetens eller integrationens ID
	Target string `json:"target,omitempty"` // pingmål med samma adress
}

// TopoLink är en förbindelse mellan två enheter.
type TopoLink struct {
	A     string `json:"a"`
	B     string `json:"b"`
	APort string `json:"aport,omitempty"`
	BPort string `json:"bport,omitempty"`
	Proto string `json:"proto"`
	Down  bool   `json:"down,omitempty"` // porten är nere
}

func normName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexByte(s, '.'); i > 0 && !isIPv4(s) {
		s = s[:i] // ta bort domänen
	}
	return s
}

func isIPv4(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if p == "" || strings.Trim(p, "0123456789") != "" {
			return false
		}
	}
	return true
}

var (
	reAP      = regexp.MustCompile(`(^|[^a-z])(ap|uap|u6|u7|mr\d+|cw\d+|iap|aruba ap|access point|wap)([^a-z]|$)`)
	reGateway = regexp.MustCompile(`(^|[^a-z])(udm|ucg|uxg|usg|gateway|router|firewall|mx\d+|fortigate|pfsense|opnsense|edgerouter|mikrotik)([^a-z]|$)`)
	reSwitch  = regexp.MustCompile(`(^|[^a-z])(switch|usw|ms\d+|catalyst|cbs\d+|sg\d+|sw|procurve|aruba \d{4})([^a-z]|$)`)
)

func guessType(text string) string {
	t := strings.ToLower(text)
	switch {
	case reGateway.MatchString(t):
		return "gateway"
	case reAP.MatchString(t):
		return "ap"
	case reSwitch.MatchString(t):
		return "switch"
	}
	return "other"
}

func (s *Server) buildTopology(customerID string) ([]TopoNode, []TopoLink) {
	nodes := []*TopoNode{}
	byKey := map[string]*TopoNode{}
	index := func(n *TopoNode) {
		for _, k := range []string{"mac:" + strings.ToLower(n.MAC), "ip:" + n.IP, "name:" + normName(n.Name)} {
			if strings.HasSuffix(k, ":") {
				continue
			}
			if _, ok := byKey[k]; !ok {
				byKey[k] = n
			}
		}
	}
	find := func(mac, ip, name string) *TopoNode {
		if mac != "" {
			if n := byKey["mac:"+strings.ToLower(mac)]; n != nil {
				return n
			}
		}
		if ip != "" {
			if n := byKey["ip:"+ip]; n != nil {
				return n
			}
		}
		if name != "" {
			if n := byKey["name:"+normName(name)]; n != nil {
				return n
			}
		}
		return nil
	}
	// SNMP-enheter hos kunden
	s.mu.RLock()
	agentCust := map[string]bool{}
	for id, a := range s.agents {
		agentCust[id] = s.customerOfAgent(a) == customerID
	}
	targetByHost := map[string]string{}
	targetUp := map[string]bool{}
	for id, t := range s.targets {
		if agentCust[t.AgentID] {
			targetByHost[strings.ToLower(t.Host)] = id
			if tr, ok := s.trackers[id]; ok {
				st := tr.Stats()
				targetUp[id] = st.Status == monitor.StatusUp || st.Status == monitor.StatusWarn
			}
		}
	}
	s.mu.RUnlock()
	type devInfo struct {
		node *TopoNode
		ifs  map[int]snmp.If
	}
	devs := map[string]*devInfo{}
	snmps.mu.Lock()
	for _, d := range snmps.devs {
		if !agentCust[d.AgentID] {
			continue
		}
		n := &TopoNode{ID: "snmp:" + d.ID, Kind: "snmp", Name: devName(d.Device), IP: d.Host, Status: "unknown", Ref: d.ID}
		di := &devInfo{node: n, ifs: map[int]snmp.If{}}
		if st := snmps.st[d.ID]; st != nil && st.last != nil {
			if st.fails == 0 && st.last.OK {
				n.Status = "up"
			} else {
				n.Status = "down"
			}
			if st.last.SysName != "" && d.Name == "" {
				n.Name = st.last.SysName
			}
			n.Model = firstLine(st.last.SysDescr)
			for _, f := range st.last.Ifs {
				di.ifs[f.Index] = f
			}
			if st.last.SysName != "" {
				byKey["name:"+normName(st.last.SysName)] = n
			}
		}
		n.Type = guessType(n.Name + " " + n.Model)
		devs[d.ID] = di
		nodes = append(nodes, n)
	}
	snmps.mu.Unlock()
	nbs := map[string][]snmp.Neighbor{}
	for id, di := range devs {
		var mac, data string
		if s.db.QueryRow(`SELECT mac,data FROM snmp_nb WHERE device_id=?`, id).Scan(&mac, &data) == nil {
			di.node.MAC = mac
			var l []snmp.Neighbor
			_ = json.Unmarshal([]byte(data), &l)
			nbs[id] = l
		}
		index(di.node)
	}
	// enheter från UniFi/Meraki/Aruba
	integs.Lock()
	for _, in := range integs.list {
		if in.CustomerID != customerID {
			continue
		}
		st := integs.st[in.ID]
		if st == nil {
			continue
		}
		for _, d := range st.devices {
			if ex := find(d.MAC, d.IP, d.Name); ex != nil {
				// samma enhet som en SNMP-enhet – komplettera i stället för att rita den två gånger
				if ex.Model == "" {
					ex.Model = d.Model
				}
				if ex.Type == "other" {
					ex.Type = d.Type
				}
				continue
			}
			st := "down"
			if d.Online {
				st = "up"
			}
			n := &TopoNode{ID: "int:" + in.ID + ":" + d.ID, Kind: "integ", Type: d.Type, Name: d.Name, IP: d.IP, MAC: strings.ToLower(d.MAC), Model: d.Model, Status: st, Ref: in.ID}
			nodes = append(nodes, n)
			index(n)
		}
	}
	integs.Unlock()
	// förbindelser från grannlistorna
	links := map[string]*TopoLink{}
	ids := make([]string, 0, len(devs))
	for id := range devs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		di := devs[id]
		for _, nb := range nbs[id] {
			other := find(nb.RemoteMAC, nb.RemoteIP, nb.RemoteName)
			if other == nil {
				name := nb.RemoteName
				if name == "" {
					name = nb.RemoteIP
				}
				if name == "" {
					name = nb.RemoteMAC
				}
				other = &TopoNode{ID: "nb:" + strings.ToLower(firstNonEmpty(nb.RemoteMAC, nb.RemoteIP, nb.RemoteName)), Kind: "neighbor", Name: name, IP: nb.RemoteIP,
					MAC: strings.ToLower(nb.RemoteMAC), Model: firstLine(nb.RemoteDesc), Status: "unknown"}
				other.Type = guessType(other.Name + " " + other.Model)
				if tid, ok := targetByHost[strings.ToLower(nb.RemoteIP)]; ok && nb.RemoteIP != "" {
					other.Target = tid
					other.Status = map[bool]string{true: "up", false: "down"}[targetUp[tid]]
				}
				nodes = append(nodes, other)
				index(other)
			}
			if other == di.node {
				continue
			}
			lport := nb.LocalPort
			f, ok := di.ifs[nb.LocalIf]
			if ok && (lport == "" || nb.Proto == "cdp") {
				lport = f.Label()
			}
			a, b, ap, bp := di.node.ID, other.ID, lport, nb.RemotePort
			if a > b {
				a, b, ap, bp = b, a, bp, ap
			}
			k := a + "|" + b
			if l := links[k]; l != nil {
				// sett från båda hållen – fyll i det som saknas
				if l.APort == "" {
					l.APort = ap
				}
				if l.BPort == "" {
					l.BPort = bp
				}
				continue
			}
			links[k] = &TopoLink{A: a, B: b, APort: ap, BPort: bp, Proto: nb.Proto, Down: ok && f.Admin == 1 && f.Oper != 1}
		}
	}
	// pingmål ger status åt grannar som saknar egen
	for _, n := range nodes {
		if n.Target == "" && n.IP != "" && n.Kind != "snmp" {
			if tid, ok := targetByHost[strings.ToLower(n.IP)]; ok {
				n.Target = tid
				if n.Kind == "neighbor" {
					n.Status = map[bool]string{true: "up", false: "down"}[targetUp[tid]]
				}
			}
		}
	}
	outN := make([]TopoNode, 0, len(nodes))
	for _, n := range nodes {
		outN = append(outN, *n)
	}
	outL := make([]TopoLink, 0, len(links))
	for _, l := range links {
		outL = append(outL, *l)
	}
	sort.Slice(outL, func(i, j int) bool { return outL[i].A+outL[i].B < outL[j].A+outL[j].B })
	return outN, outL
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

func (s *Server) handleTopology(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.RLock()
	_, ok := s.customers[id]
	s.mu.RUnlock()
	if !ok || !canSee(userFrom(r), id) {
		httpErr(w, 404, "the customer does not exist")
		return
	}
	nodes, links := s.buildTopology(id)
	writeJSON(w, map[string]any{"nodes": nodes, "links": links})
}
