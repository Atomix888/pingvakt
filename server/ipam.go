package main

// IPAM: subnät per kund med alla IP-adresser. Adresserna fylls i automatiskt från agenternas
// nätverksskanningar, routrarnas ARP-tabeller (SNMP) och agenternas egna adresser, och kan
// kompletteras för hand (reservationer, beskrivning, ansvarig).

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"pingvakt/core/alert"
	"pingvakt/core/i18n"
	"pingvakt/core/monitor"
	"pingvakt/core/netscan"
	"pingvakt/core/snmp"
)

const ipamSchema = `
CREATE TABLE IF NOT EXISTS ipam_subnets (id TEXT PRIMARY KEY, customer_id TEXT NOT NULL, data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS ipam_addrs (subnet_id TEXT NOT NULL, ip TEXT NOT NULL, data TEXT NOT NULL, PRIMARY KEY (subnet_id, ip)) WITHOUT ROWID;
`

const (
	ipamActive   = 24 * time.Hour      // "online" i vyn
	ipamUsed     = 30 * 24 * time.Hour // räknas som använd
	ipamDHCPUsed = 7 * 24 * time.Hour  // räknas som utdelad i DHCP-poolen
	ipamConflict = 10 * time.Minute    // två MAC på samma IP inom detta = konflikt
	ipamMaxSize  = 65536               // största subnät (/16)
)

// Subnet är ett IPv4-nät hos en kund.
type Subnet struct {
	ID         string `json:"id"`
	CustomerID string `json:"customerId"`
	SiteID     string `json:"siteId"`
	CIDR       string `json:"cidr"`
	Name       string `json:"name"`
	VLAN       int    `json:"vlan"`
	Gateway    string `json:"gateway"`
	DHCPFrom   string `json:"dhcpFrom"`
	DHCPTo     string `json:"dhcpTo"`
	Notes      string `json:"notes"`
	ScanAgent  string `json:"scanAgent"` // agent som skannar nätet automatiskt
	ScanHours  int    `json:"scanHours"` // 0 = av
	LastScan   int64  `json:"lastScan"`
	Created    int64  `json:"created"`
}

// IPAddr är en adress som setts eller lagts in för hand.
type IPAddr struct {
	IP       string `json:"ip"`
	Host     string `json:"host,omitempty"`
	MAC      string `json:"mac,omitempty"`
	Vendor   string `json:"vendor,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Desc     string `json:"desc,omitempty"`
	Owner    string `json:"owner,omitempty"`
	Reserved bool   `json:"reserved,omitempty"`
	ResMAC   string `json:"resMac,omitempty"`   // reserverad för en viss MAC
	HostSet  bool   `json:"hostSet,omitempty"`  // namnet är satt för hand och skrivs inte över
	First    int64  `json:"first,omitempty"`    // först sedd
	Last     int64  `json:"last,omitempty"`     // senast sedd
	Src      string `json:"src,omitempty"`      // scan, arp, agent
	PrevMAC  string `json:"prevMac,omitempty"`  // föregående MAC om den bytts
	MACAt    int64  `json:"macAt,omitempty"`    // när MAC byttes
	Conflict int64  `json:"conflict,omitempty"` // tidpunkt för misstänkt IP-konflikt (0 = ingen)
	ConfMAC  string `json:"confMac,omitempty"`  // den andra MAC-adressen i konflikten
	Ports    []int  `json:"ports,omitempty"`    // öppna portar vid senaste skanningen
	User     string `json:"user,omitempty"`     // användarnamn för webb/SSH
	Secret   string `json:"secret,omitempty"`   // lösenordet, krypterat (skickas aldrig till webbläsaren)
	HasPass  bool   `json:"hasPass,omitempty"`  // bara i API-svar
	HostKey  string `json:"hostKey,omitempty"`  // SSH-värdnyckelns fingeravtryck
}

// public är adressen som den visas i webbläsaren (utan det krypterade lösenordet).
func (a IPAddr) public() IPAddr {
	a.HasPass = a.Secret != ""
	a.Secret = ""
	return a
}

type ipamState struct {
	mu      sync.Mutex
	subnets map[string]*Subnet
	nets    map[string]*net.IPNet
	addrs   map[string]map[string]*IPAddr // subnät -> ip -> adress
	dirty   map[string]map[string]bool    // osparade ändringar
}

var ipam = &ipamState{subnets: map[string]*Subnet{}, nets: map[string]*net.IPNet{}, addrs: map[string]map[string]*IPAddr{}, dirty: map[string]map[string]bool{}}

func ip4u(ip net.IP) uint32 { return binary.BigEndian.Uint32(ip.To4()) }
func u2ip4(u uint32) net.IP {
	b := make(net.IP, 4)
	binary.BigEndian.PutUint32(b, u)
	return b
}

// span ger första och sista användbara adress samt antalet.
func span(n *net.IPNet) (first, last uint32, size int) {
	ones, bits := n.Mask.Size()
	base := ip4u(n.IP)
	total := uint32(1) << uint(bits-ones)
	if ones >= 31 {
		return base, base + total - 1, int(total)
	}
	return base + 1, base + total - 2, int(total) - 2
}

func parseSubnet(cidr string) (*net.IPNet, error) {
	_, n, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil || n.IP.To4() == nil {
		return nil, errors.New(i18n.T("enter an IPv4 network, e.g. 192.168.1.0/24"))
	}
	n.IP = n.IP.To4()
	ones, _ := n.Mask.Size()
	if ones < 16 {
		return nil, errors.New(i18n.T("the network is too large (at most /16)"))
	}
	return n, nil
}

func (s *Server) loadIPAM() {
	rows, err := s.db.Query(`SELECT data FROM ipam_subnets`)
	if err != nil {
		return
	}
	ipam.mu.Lock()
	defer ipam.mu.Unlock()
	for rows.Next() {
		var d string
		var sn Subnet
		if rows.Scan(&d) == nil && json.Unmarshal([]byte(d), &sn) == nil {
			if n, err := parseSubnet(sn.CIDR); err == nil {
				ipam.subnets[sn.ID], ipam.nets[sn.ID], ipam.addrs[sn.ID] = &sn, n, map[string]*IPAddr{}
			}
		}
	}
	rows.Close()
	rows, err = s.db.Query(`SELECT subnet_id, data FROM ipam_addrs`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, d string
		var a IPAddr
		if rows.Scan(&id, &d) == nil && json.Unmarshal([]byte(d), &a) == nil && ipam.addrs[id] != nil {
			ipam.addrs[id][a.IP] = &a
		}
	}
}

func (s *Server) saveSubnet(sn *Subnet) {
	b, _ := json.Marshal(sn)
	_, _ = s.db.Exec(`INSERT OR REPLACE INTO ipam_subnets(id,customer_id,data) VALUES(?,?,?)`, sn.ID, sn.CustomerID, string(b))
}

// flushIPAM sparar ändrade adresser (observationer kommer ofta och sparas samlat).
func (s *Server) flushIPAM() {
	ipam.mu.Lock()
	type row struct{ sub, ip, data string }
	var save []row
	var del [][2]string
	for sub, ips := range ipam.dirty {
		for ip := range ips {
			if a, ok := ipam.addrs[sub][ip]; ok {
				b, _ := json.Marshal(a)
				save = append(save, row{sub, ip, string(b)})
			} else {
				del = append(del, [2]string{sub, ip})
			}
		}
	}
	ipam.dirty = map[string]map[string]bool{}
	ipam.mu.Unlock()
	if len(save) == 0 && len(del) == 0 {
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	for _, r := range save {
		_, _ = tx.Exec(`INSERT OR REPLACE INTO ipam_addrs(subnet_id,ip,data) VALUES(?,?,?)`, r.sub, r.ip, r.data)
	}
	for _, d := range del {
		_, _ = tx.Exec(`DELETE FROM ipam_addrs WHERE subnet_id=? AND ip=?`, d[0], d[1])
	}
	_ = tx.Commit()
}

func markDirty(sub, ip string) {
	if ipam.dirty[sub] == nil {
		ipam.dirty[sub] = map[string]bool{}
	}
	ipam.dirty[sub][ip] = true
}

// ---------- observationer ----------

type ipObs struct {
	IP, MAC, Host, Vendor, Kind, Src string
	Ports                            []int
}

// ipamObserve för in adresser som setts hos en kund i de subnät de hör till.
func (s *Server) ipamObserve(cust string, obs []ipObs) {
	if cust == "" || len(obs) == 0 {
		return
	}
	t := now()
	ipam.mu.Lock()
	defer ipam.mu.Unlock()
	for id, sn := range ipam.subnets {
		if sn.CustomerID != cust {
			continue
		}
		n := ipam.nets[id]
		for _, o := range obs {
			ip := net.ParseIP(o.IP).To4()
			if ip == nil || !n.Contains(ip) {
				continue
			}
			key := ip.String()
			a := ipam.addrs[id][key]
			if a == nil {
				a = &IPAddr{IP: key, First: t}
				ipam.addrs[id][key] = a
			}
			mac := strings.ToLower(o.MAC)
			if mac != "" {
				switch {
				case a.MAC == "":
					a.MAC = mac
				case a.MAC != mac:
					// en annan MAC kort efter den förra = två enheter på samma adress
					if t-a.Last < ipamConflict.Milliseconds() {
						a.Conflict, a.ConfMAC = t, a.MAC
					}
					a.PrevMAC, a.MACAt, a.MAC = a.MAC, t, mac
					a.Vendor = ""
				}
				if a.Reserved && a.ResMAC != "" && !strings.EqualFold(a.ResMAC, mac) {
					a.Conflict, a.ConfMAC = t, mac
				}
				if v := netscan.Vendor(mac); v != "" {
					a.Vendor = v
				}
			}
			if o.Vendor != "" {
				a.Vendor = o.Vendor
			}
			if o.Host != "" && !a.HostSet {
				a.Host = o.Host
			}
			if o.Kind != "" {
				a.Kind = o.Kind
			}
			if o.Src == "scan" {
				a.Ports = o.Ports
			}
			// skanning väger tyngst som källa; ARP och agentens egen adress fyller på
			if a.Src == "" || o.Src == "scan" {
				a.Src = o.Src
			}
			// skriv inte till databasen för ofta för samma adress
			if t-a.Last > 5*60*1000 || a.First == t {
				markDirty(id, key)
			}
			a.Last = t
		}
	}
}

func (s *Server) ipamFromScan(agentID string, devs []netscan.Device) {
	s.mu.RLock()
	a, ok := s.agents[agentID]
	cust := ""
	if ok {
		cust = s.customerOfAgent(a)
	}
	s.mu.RUnlock()
	obs := make([]ipObs, 0, len(devs))
	for _, d := range devs {
		obs = append(obs, ipObs{IP: d.IP, MAC: d.MAC, Host: d.Name, Vendor: d.Vendor, Kind: d.Kind, Src: "scan", Ports: d.Ports})
	}
	s.ipamObserve(cust, obs)
	// markera när nät som skannats av den här agenten senast skannades
	ipam.mu.Lock()
	for _, sn := range ipam.subnets {
		if sn.ScanAgent == agentID && sn.CustomerID == cust {
			sn.LastScan = now()
		}
	}
	ipam.mu.Unlock()
}

func (s *Server) ipamFromArp(deviceID string, arp []snmp.ArpEntry) {
	snmps.mu.Lock()
	d, ok := snmps.devs[deviceID]
	agentID := ""
	if ok {
		agentID = d.AgentID
	}
	snmps.mu.Unlock()
	s.mu.RLock()
	cust := ""
	if a, ok := s.agents[agentID]; ok {
		cust = s.customerOfAgent(a)
	}
	s.mu.RUnlock()
	obs := make([]ipObs, 0, len(arp))
	for _, e := range arp {
		obs = append(obs, ipObs{IP: e.IP, MAC: e.MAC, Src: "arp"})
	}
	s.ipamObserve(cust, obs)
}

func (s *Server) ipamFromAgent(agentID string, nets []netscan.Network) {
	s.mu.RLock()
	a, ok := s.agents[agentID]
	cust, name := "", ""
	if ok {
		cust, name = s.customerOfAgent(a), a.Name
	}
	s.mu.RUnlock()
	var obs []ipObs
	for _, n := range nets {
		if n.Addr != "" {
			obs = append(obs, ipObs{IP: n.Addr, Host: name, Kind: "agent", Src: "agent"})
		}
	}
	s.ipamObserve(cust, obs)
}

// ---------- statistik och larm ----------

// SubnetStats är beläggningen i ett subnät.
type SubnetStats struct {
	Size      int     `json:"size"`     // användbara adresser
	Used      int     `json:"used"`     // sedda senaste 30 dagarna eller reserverade
	Active    int     `json:"active"`   // sedda senaste dygnet
	Reserved  int     `json:"reserved"` // reserverade
	Util      float64 `json:"util"`     // procent
	DHCPSize  int     `json:"dhcpSize"`
	DHCPUsed  int     `json:"dhcpUsed"`
	DHCPUtil  float64 `json:"dhcpUtil"`
	Conflicts int     `json:"conflicts"`
	Free      int     `json:"free"`
}

func dhcpRange(sn *Subnet) (from, to uint32, ok bool) {
	a, b := net.ParseIP(sn.DHCPFrom).To4(), net.ParseIP(sn.DHCPTo).To4()
	if a == nil || b == nil {
		return 0, 0, false
	}
	from, to = ip4u(a), ip4u(b)
	if to < from {
		from, to = to, from
	}
	return from, to, true
}

// activeConflict: konflikter glöms efter ett dygn utan att de upprepas.
func activeConflict(a *IPAddr, t int64) bool {
	return a.Conflict > 0 && t-a.Conflict < 24*3600*1000
}

func stats(sn *Subnet, n *net.IPNet, addrs map[string]*IPAddr) SubnetStats {
	t := now()
	first, last, size := span(n)
	st := SubnetStats{Size: size}
	df, dt, hasDHCP := dhcpRange(sn)
	if hasDHCP {
		if df < first {
			df = first
		}
		if dt > last {
			dt = last
		}
		if dt >= df {
			st.DHCPSize = int(dt - df + 1)
		}
	}
	gw := net.ParseIP(sn.Gateway).To4()
	gwCounted := false
	for _, a := range addrs {
		ip := net.ParseIP(a.IP).To4()
		if ip == nil {
			continue
		}
		u := ip4u(ip)
		if u < first || u > last {
			continue
		}
		used := a.Reserved || (a.Last > 0 && t-a.Last < ipamUsed.Milliseconds())
		if used {
			st.Used++
			if gw != nil && ip.Equal(gw) {
				gwCounted = true
			}
		}
		if a.Reserved {
			st.Reserved++
		}
		if a.Last > 0 && t-a.Last < ipamActive.Milliseconds() {
			st.Active++
		}
		if activeConflict(a, t) {
			st.Conflicts++
		}
		if st.DHCPSize > 0 && u >= df && u <= dt && a.Last > 0 && t-a.Last < ipamDHCPUsed.Milliseconds() {
			st.DHCPUsed++
		}
	}
	if gw != nil && n.Contains(gw) && !gwCounted {
		st.Used++
	}
	st.Free = st.Size - st.Used
	if st.Free < 0 {
		st.Free = 0
	}
	if st.Size > 0 {
		st.Util = float64(st.Used) * 100 / float64(st.Size)
	}
	if st.DHCPSize > 0 {
		st.DHCPUtil = float64(st.DHCPUsed) * 100 / float64(st.DHCPSize)
	}
	return st
}

func (s *Server) ipamInfo(id string) (name, cust string, ok bool) {
	sid, found := strings.CutPrefix(id, "ipam:")
	if !found {
		return "", "", false
	}
	ipam.mu.Lock()
	sn, ok2 := ipam.subnets[sid]
	var cp Subnet
	if ok2 {
		cp = *sn
	}
	ipam.mu.Unlock()
	if !ok2 {
		return id, "", true
	}
	s.mu.RLock()
	cname := ""
	if c, ok := s.customers[cp.CustomerID]; ok {
		cname = c.Name
	}
	s.mu.RUnlock()
	label := cp.CIDR
	if cp.Name != "" {
		label = cp.Name + " (" + cp.CIDR + ")"
	}
	return cname + " · IPAM · " + label, cp.CustomerID, true
}

// ipamTick kör schemalagda skanningar och kontrollerar larmen (varje minut).
func (s *Server) ipamTick() {
	t := now()
	type job struct{ agent, cidr string }
	var jobs []job
	type chk struct {
		sn Subnet
		st SubnetStats
	}
	var checks []chk
	ipam.mu.Lock()
	for id, sn := range ipam.subnets {
		if sn.ScanAgent != "" && sn.ScanHours > 0 && t-sn.LastScan > int64(sn.ScanHours)*3600*1000 {
			if _, _, size := span(ipam.nets[id]); size <= netscan.MaxHosts {
				jobs = append(jobs, job{sn.ScanAgent, sn.CIDR})
				sn.LastScan = t // försök inte igen varje minut om agenten är offline
			}
		}
		checks = append(checks, chk{*sn, stats(sn, ipam.nets[id], ipam.addrs[id])})
	}
	ipam.mu.Unlock()
	for _, j := range jobs {
		_, _ = s.queueScan(j.agent, j.cidr, "IPAM")
	}
	s.mu.RLock()
	var rules []Rule
	for _, r := range s.rules {
		if r.Condition == alert.CondIPAM {
			rules = append(rules, r)
		}
	}
	s.mu.RUnlock()
	for _, c := range checks {
		name, _, _ := s.ipamInfo("ipam:" + c.sn.ID)
		for _, r := range rules {
			if r.CustomerID != "" && r.CustomerID != c.sn.CustomerID {
				continue
			}
			var why []string
			if c.st.Util >= r.Threshold {
				why = append(why, fmt.Sprintf(i18n.T("the subnet is %.0f %% used (%d of %d)"), c.st.Util, c.st.Used, c.st.Size))
			}
			if c.st.DHCPSize > 0 && c.st.DHCPUtil >= r.Threshold {
				why = append(why, fmt.Sprintf(i18n.T("the DHCP pool is %.0f %% used (%d of %d)"), c.st.DHCPUtil, c.st.DHCPUsed, c.st.DHCPSize))
			}
			if c.st.Conflicts > 0 {
				why = append(why, fmt.Sprintf(i18n.T("%d possible IP conflicts"), c.st.Conflicts))
			}
			s.setHostAlert(r, "ipam:"+c.sn.ID, name, c.sn.CIDR, len(why) > 0, strings.Join(why, ", "), i18n.T("The subnet is OK again"))
		}
	}
}

func (s *Server) ipamLoop(ctx context.Context) {
	tk := time.NewTicker(time.Minute)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			s.flushIPAM()
			return
		case <-tk.C:
			s.flushIPAM()
			s.ipamTick()
			pruneRemotes()
		}
	}
}

// ---------- API ----------

func (s *Server) subnetVisible(r *http.Request, id string) (*Subnet, bool) {
	ipam.mu.Lock()
	sn, ok := ipam.subnets[id]
	var cp Subnet
	if ok {
		cp = *sn
	}
	ipam.mu.Unlock()
	if !ok || !canSee(userFrom(r), cp.CustomerID) {
		return nil, false
	}
	return &cp, true
}

type subnetSummary struct {
	Subnet
	Stats     SubnetStats `json:"stats"`
	SiteName  string      `json:"siteName,omitempty"`
	AgentName string      `json:"agentName,omitempty"`
}

type subnetSuggestion struct {
	CIDR      string `json:"cidr"`
	Gateway   string `json:"gateway,omitempty"`
	AgentID   string `json:"agentId"`
	AgentName string `json:"agentName"`
	SiteID    string `json:"siteId"`
}

// handleIPAMList ger subnäten (för en kund eller alla synliga) och förslag på nät att lägga till.
func (s *Server) handleIPAMList(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	cust := r.URL.Query().Get("customer")
	if cust != "" && !canSee(u, cust) {
		httpErr(w, 404, "the customer does not exist")
		return
	}
	out := []subnetSummary{}
	covered := map[string][]*net.IPNet{}
	ipam.mu.Lock()
	for id, sn := range ipam.subnets {
		covered[sn.CustomerID] = append(covered[sn.CustomerID], ipam.nets[id])
		if (cust != "" && sn.CustomerID != cust) || !canSee(u, sn.CustomerID) {
			continue
		}
		out = append(out, subnetSummary{Subnet: *sn, Stats: stats(sn, ipam.nets[id], ipam.addrs[id])})
	}
	ipam.mu.Unlock()
	s.mu.RLock()
	for i := range out {
		if st, ok := s.sites[out[i].SiteID]; ok {
			out[i].SiteName = st.Name
		}
		if a, ok := s.agents[out[i].ScanAgent]; ok {
			out[i].AgentName = a.Name
		}
	}
	var agents []*Agent
	for _, a := range s.agents {
		if cust != "" && s.customerOfAgent(a) == cust {
			cp := *a
			agents = append(agents, &cp)
		}
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].CustomerID != out[j].CustomerID {
			return out[i].CustomerID < out[j].CustomerID
		}
		a, b := net.ParseIP(strings.Split(out[i].CIDR, "/")[0]).To4(), net.ParseIP(strings.Split(out[j].CIDR, "/")[0]).To4()
		if a == nil || b == nil {
			return out[i].CIDR < out[j].CIDR
		}
		return ip4u(a) < ip4u(b)
	})
	// förslag: nät som kundens agenter sitter på men som inte finns i IPAM
	sugg := []subnetSuggestion{}
	seen := map[string]bool{}
	scans.mu.Lock()
	for _, a := range agents {
		for _, n := range scans.nets[a.ID] {
			pn, err := parseSubnet(n.CIDR)
			if err != nil || seen[pn.String()] {
				continue
			}
			dup := false
			for _, c := range covered[cust] {
				if c.Contains(pn.IP) || pn.Contains(c.IP) {
					dup = true
				}
			}
			if dup {
				continue
			}
			seen[pn.String()] = true
			sugg = append(sugg, subnetSuggestion{CIDR: pn.String(), Gateway: n.Gateway, AgentID: a.ID, AgentName: a.Name, SiteID: a.SiteID})
		}
	}
	scans.mu.Unlock()
	writeJSON(w, map[string]any{"subnets": out, "suggestions": sugg})
}

// validateSubnet kontrollerar och normaliserar ett subnät.
func (s *Server) validateSubnet(sn *Subnet) error {
	n, err := parseSubnet(sn.CIDR)
	if err != nil {
		return err
	}
	sn.CIDR = n.String()
	sn.Name = strings.TrimSpace(sn.Name)
	s.mu.RLock()
	_, custOK := s.customers[sn.CustomerID]
	st, siteOK := s.sites[sn.SiteID]
	if sn.SiteID != "" && (!siteOK || st.CustomerID != sn.CustomerID) {
		sn.SiteID = ""
	}
	if a, ok := s.agents[sn.ScanAgent]; sn.ScanAgent != "" && (!ok || s.customerOfAgent(a) != sn.CustomerID) {
		sn.ScanAgent = ""
	}
	s.mu.RUnlock()
	if !custOK {
		return errors.New(i18n.T("the customer does not exist"))
	}
	for _, f := range []*string{&sn.Gateway, &sn.DHCPFrom, &sn.DHCPTo} {
		*f = strings.TrimSpace(*f)
		if *f == "" {
			continue
		}
		ip := net.ParseIP(*f).To4()
		if ip == nil || !n.Contains(ip) {
			return fmt.Errorf(i18n.T("%s is not an address in %s"), *f, sn.CIDR)
		}
		*f = ip.String()
	}
	if (sn.DHCPFrom == "") != (sn.DHCPTo == "") {
		return errors.New(i18n.T("enter both the first and the last address of the DHCP pool"))
	}
	if sn.VLAN < 0 || sn.VLAN > 4094 {
		return errors.New(i18n.T("VLAN must be 1–4094"))
	}
	if sn.ScanHours < 0 {
		sn.ScanHours = 0
	}
	if _, _, size := span(n); sn.ScanHours > 0 && size > netscan.MaxHosts {
		return fmt.Errorf(i18n.T("automatic scanning works for networks up to %d addresses"), netscan.MaxHosts)
	}
	ipam.mu.Lock()
	defer ipam.mu.Unlock()
	for id, o := range ipam.subnets {
		if id == sn.ID || o.CustomerID != sn.CustomerID {
			continue
		}
		on := ipam.nets[id]
		if on.Contains(n.IP) || n.Contains(on.IP) {
			return fmt.Errorf(i18n.T("overlaps with %s"), o.CIDR)
		}
	}
	return nil
}

func (s *Server) handleSubnetCreate(w http.ResponseWriter, r *http.Request) {
	var sn Subnet
	if !readJSON(w, r, &sn) {
		return
	}
	sn.ID, sn.Created = monitor.NewID(), now()
	if err := s.validateSubnet(&sn); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	n, _ := parseSubnet(sn.CIDR)
	ipam.mu.Lock()
	ipam.subnets[sn.ID], ipam.nets[sn.ID], ipam.addrs[sn.ID] = &sn, n, map[string]*IPAddr{}
	ipam.mu.Unlock()
	s.saveSubnet(&sn)
	s.audit(r, "ipam", sn.ID, i18n.T("new subnet ")+sn.CIDR)
	// kör en första skanning direkt om en agent är vald
	if sn.ScanAgent != "" && n != nil {
		if _, _, size := span(n); size <= netscan.MaxHosts {
			if _, err := s.queueScan(sn.ScanAgent, sn.CIDR, userFrom(r).Email); err == nil {
				ipam.mu.Lock()
				ipam.subnets[sn.ID].LastScan = now()
				ipam.mu.Unlock()
			}
		}
	}
	writeJSON(w, sn)
}

func (s *Server) handleSubnetSave(w http.ResponseWriter, r *http.Request) {
	old, ok := s.subnetVisible(r, r.PathValue("id"))
	if !ok {
		httpErr(w, 404, "the subnet does not exist")
		return
	}
	var sn Subnet
	if !readJSON(w, r, &sn) {
		return
	}
	sn.ID, sn.CustomerID, sn.Created, sn.LastScan = old.ID, old.CustomerID, old.Created, old.LastScan
	if err := s.validateSubnet(&sn); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	n, _ := parseSubnet(sn.CIDR)
	ipam.mu.Lock()
	ipam.subnets[sn.ID], ipam.nets[sn.ID] = &sn, n
	// adresser som hamnat utanför ett ändrat nät tas bort
	for ip := range ipam.addrs[sn.ID] {
		if !n.Contains(net.ParseIP(ip)) {
			delete(ipam.addrs[sn.ID], ip)
			markDirty(sn.ID, ip)
		}
	}
	ipam.mu.Unlock()
	s.saveSubnet(&sn)
	s.flushIPAM()
	s.audit(r, "ipam", sn.ID, i18n.T("edited subnet ")+sn.CIDR)
	writeJSON(w, sn)
}

func (s *Server) handleSubnetDelete(w http.ResponseWriter, r *http.Request) {
	sn, ok := s.subnetVisible(r, r.PathValue("id"))
	if !ok {
		httpErr(w, 404, "the subnet does not exist")
		return
	}
	ipam.mu.Lock()
	delete(ipam.subnets, sn.ID)
	delete(ipam.nets, sn.ID)
	delete(ipam.addrs, sn.ID)
	delete(ipam.dirty, sn.ID)
	ipam.mu.Unlock()
	_, _ = s.db.Exec(`DELETE FROM ipam_subnets WHERE id=?`, sn.ID)
	_, _ = s.db.Exec(`DELETE FROM ipam_addrs WHERE subnet_id=?`, sn.ID)
	s.audit(r, "ipam", sn.ID, i18n.T("deleted subnet ")+sn.CIDR)
	writeJSON(w, map[string]bool{"ok": true})
}

// ipRef är något i PingVakt som använder en adress (mål, agent, SNMP-enhet).
type ipRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ipRefs samlar vilka mål, agenter och SNMP-enheter hos kunden som har en viss adress.
func (s *Server) ipRefs(cust string) map[string][]ipRef {
	out := map[string][]ipRef{}
	add := func(host string, ref ipRef) {
		if ip := net.ParseIP(strings.TrimSpace(host)).To4(); ip != nil {
			out[ip.String()] = append(out[ip.String()], ref)
		}
	}
	s.mu.RLock()
	agentIDs := map[string]bool{}
	for _, a := range s.agents {
		if s.customerOfAgent(a) == cust {
			agentIDs[a.ID] = true
		}
	}
	for _, t := range s.targets {
		if agentIDs[t.AgentID] {
			add(t.Host, ipRef{"target", t.ID, s.displayName(t)})
		}
	}
	names := map[string]string{}
	for id := range agentIDs {
		names[id] = s.agents[id].Name
	}
	s.mu.RUnlock()
	scans.mu.Lock()
	for id := range agentIDs {
		for _, n := range scans.nets[id] {
			add(n.Addr, ipRef{"agent", id, names[id]})
		}
	}
	scans.mu.Unlock()
	snmps.mu.Lock()
	for _, d := range snmps.devs {
		if agentIDs[d.AgentID] {
			add(d.Host, ipRef{"snmp", d.ID, d.Name})
		}
	}
	snmps.mu.Unlock()
	return out
}

func (s *Server) handleSubnetGet(w http.ResponseWriter, r *http.Request) {
	sn, ok := s.subnetVisible(r, r.PathValue("id"))
	if !ok {
		httpErr(w, 404, "the subnet does not exist")
		return
	}
	ipam.mu.Lock()
	st := stats(ipam.subnets[sn.ID], ipam.nets[sn.ID], ipam.addrs[sn.ID])
	list := make([]IPAddr, 0, len(ipam.addrs[sn.ID]))
	for _, a := range ipam.addrs[sn.ID] {
		list = append(list, a.public())
	}
	ipam.mu.Unlock()
	sort.Slice(list, func(i, j int) bool {
		return ip4u(net.ParseIP(list[i].IP)) < ip4u(net.ParseIP(list[j].IP))
	})
	s.mu.RLock()
	agentName, siteName := "", ""
	if a, ok := s.agents[sn.ScanAgent]; ok {
		agentName = a.Name
	}
	if site, ok := s.sites[sn.SiteID]; ok {
		siteName = site.Name
	}
	s.mu.RUnlock()
	running := false
	scans.mu.Lock()
	for _, run := range scans.runs[sn.ScanAgent] {
		if run.Pending && run.CIDR == sn.CIDR {
			running = true
		}
	}
	scans.mu.Unlock()
	writeJSON(w, map[string]any{"subnet": subnetSummary{Subnet: *sn, Stats: st, SiteName: siteName, AgentName: agentName},
		"addrs": list, "refs": s.ipRefs(sn.CustomerID), "scanning": running, "now": now()})
}

// handleAddrSave sparar det man fyllt i för hand om en adress.
func (s *Server) handleAddrSave(w http.ResponseWriter, r *http.Request) {
	sn, ok := s.subnetVisible(r, r.PathValue("id"))
	if !ok {
		httpErr(w, 404, "the subnet does not exist")
		return
	}
	var in struct {
		Host     string `json:"host"`
		Desc     string `json:"desc"`
		Owner    string `json:"owner"`
		Reserved bool   `json:"reserved"`
		ResMAC   string `json:"resMac"`
		Resolve  bool   `json:"resolve"` // markera konflikten som åtgärdad
		User     string `json:"user"`
		Password string `json:"password"`      // nytt lösenord (tomt = oförändrat)
		ClearPw  bool   `json:"clearPassword"` // ta bort sparat lösenord
	}
	if !readJSON(w, r, &in) {
		return
	}
	ip := net.ParseIP(r.PathValue("ip")).To4()
	ipam.mu.Lock()
	n := ipam.nets[sn.ID]
	ipam.mu.Unlock()
	if ip == nil || !n.Contains(ip) {
		httpErr(w, 400, fmt.Sprintf(i18n.T("%s is not an address in %s"), r.PathValue("ip"), sn.CIDR))
		return
	}
	in.ResMAC = strings.ToLower(strings.TrimSpace(in.ResMAC))
	if in.ResMAC != "" {
		hw, err := net.ParseMAC(in.ResMAC)
		if err != nil {
			httpErr(w, 400, "invalid MAC address")
			return
		}
		in.ResMAC = hw.String()
	}
	key := ip.String()
	var enc string
	if in.Password != "" {
		var err error
		if enc, err = s.seal(in.Password); err != nil {
			httpErr(w, 500, err.Error())
			return
		}
	}
	ipam.mu.Lock()
	a := ipam.addrs[sn.ID][key]
	if a == nil {
		a = &IPAddr{IP: key}
		ipam.addrs[sn.ID][key] = a
	}
	a.User = strings.TrimSpace(in.User)
	switch {
	case enc != "":
		a.Secret = enc
	case in.ClearPw:
		a.Secret = ""
	}
	a.Host, a.HostSet = strings.TrimSpace(in.Host), strings.TrimSpace(in.Host) != ""
	a.Desc, a.Owner = strings.TrimSpace(in.Desc), strings.TrimSpace(in.Owner)
	a.Reserved, a.ResMAC = in.Reserved, in.ResMAC
	if !a.Reserved {
		a.ResMAC = ""
	}
	if in.Resolve {
		a.Conflict, a.ConfMAC = 0, ""
	}
	// en tom post som aldrig setts tas bort helt
	empty := !a.Reserved && a.Host == "" && a.Desc == "" && a.Owner == "" && a.Last == 0 && a.User == "" && a.Secret == ""
	if empty {
		delete(ipam.addrs[sn.ID], key)
	}
	markDirty(sn.ID, key)
	cp := IPAddr{IP: key}
	if !empty {
		cp = a.public()
	}
	ipam.mu.Unlock()
	s.flushIPAM()
	s.audit(r, "ipam", sn.ID, i18n.T("edited address ")+key)
	writeJSON(w, cp)
}

// handleAddrDelete glömmer en adress (den kommer tillbaka om den ses igen).
func (s *Server) handleAddrDelete(w http.ResponseWriter, r *http.Request) {
	sn, ok := s.subnetVisible(r, r.PathValue("id"))
	if !ok {
		httpErr(w, 404, "the subnet does not exist")
		return
	}
	key := r.PathValue("ip")
	ipam.mu.Lock()
	delete(ipam.addrs[sn.ID], key)
	markDirty(sn.ID, key)
	ipam.mu.Unlock()
	s.flushIPAM()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleSubnetScan(w http.ResponseWriter, r *http.Request) {
	sn, ok := s.subnetVisible(r, r.PathValue("id"))
	if !ok {
		httpErr(w, 404, "the subnet does not exist")
		return
	}
	var in struct {
		AgentID string `json:"agentId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	agent := sn.ScanAgent
	if in.AgentID != "" {
		agent = in.AgentID
	}
	s.mu.RLock()
	a, okA := s.agents[agent]
	okA = okA && s.customerOfAgent(a) == sn.CustomerID
	s.mu.RUnlock()
	if !okA {
		httpErr(w, 400, "choose which agent should scan the network")
		return
	}
	n, _ := parseSubnet(sn.CIDR)
	if _, _, size := span(n); size > netscan.MaxHosts {
		httpErr(w, 400, fmt.Sprintf(i18n.T("automatic scanning works for networks up to %d addresses"), netscan.MaxHosts))
		return
	}
	run, err := s.queueScan(agent, sn.CIDR, userFrom(r).Email)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	ipam.mu.Lock()
	if x, ok := ipam.subnets[sn.ID]; ok {
		x.LastScan = now()
		if x.ScanAgent == "" {
			x.ScanAgent = agent
		}
		cp := *x
		ipam.mu.Unlock()
		s.saveSubnet(&cp)
	} else {
		ipam.mu.Unlock()
	}
	s.audit(r, "ipam", sn.ID, i18n.T("network scan ")+sn.CIDR)
	writeJSON(w, run)
}

// nextFree ger första lediga adressen utanför DHCP-poolen (eller i den om inget annat finns).
func nextFree(sn *Subnet, n *net.IPNet, addrs map[string]*IPAddr) string {
	t := now()
	first, last, _ := span(n)
	df, dt, hasDHCP := dhcpRange(sn)
	gw := net.ParseIP(sn.Gateway).To4()
	taken := func(u uint32) bool {
		ip := u2ip4(u)
		if gw != nil && ip.Equal(gw) {
			return true
		}
		a := addrs[ip.String()]
		return a != nil && (a.Reserved || a.Host != "" || a.Desc != "" || (a.Last > 0 && t-a.Last < ipamUsed.Milliseconds()))
	}
	for pass := 0; pass < 2; pass++ {
		for u := first; u <= last && u >= first; u++ {
			inPool := hasDHCP && u >= df && u <= dt
			if (pass == 0) == inPool || taken(u) {
				continue
			}
			return u2ip4(u).String()
		}
	}
	return ""
}

func (s *Server) handleSubnetNext(w http.ResponseWriter, r *http.Request) {
	sn, ok := s.subnetVisible(r, r.PathValue("id"))
	if !ok {
		httpErr(w, 404, "the subnet does not exist")
		return
	}
	ipam.mu.Lock()
	ip := nextFree(ipam.subnets[sn.ID], ipam.nets[sn.ID], ipam.addrs[sn.ID])
	ipam.mu.Unlock()
	if ip == "" {
		httpErr(w, 400, "the subnet is full")
		return
	}
	writeJSON(w, map[string]string{"ip": ip})
}

// handleSubnetImport tar emot rader (från en CSV som webbläsaren läst) och lägger in dem.
func (s *Server) handleSubnetImport(w http.ResponseWriter, r *http.Request) {
	sn, ok := s.subnetVisible(r, r.PathValue("id"))
	if !ok {
		httpErr(w, 404, "the subnet does not exist")
		return
	}
	var rows []struct {
		IP       string `json:"ip"`
		Host     string `json:"host"`
		MAC      string `json:"mac"`
		Desc     string `json:"desc"`
		Owner    string `json:"owner"`
		Reserved bool   `json:"reserved"`
	}
	if !readJSON(w, r, &rows) {
		return
	}
	added, skipped := 0, 0
	ipam.mu.Lock()
	n := ipam.nets[sn.ID]
	for _, row := range rows {
		ip := net.ParseIP(strings.TrimSpace(row.IP)).To4()
		if ip == nil || !n.Contains(ip) {
			skipped++
			continue
		}
		key := ip.String()
		a := ipam.addrs[sn.ID][key]
		if a == nil {
			a = &IPAddr{IP: key}
			ipam.addrs[sn.ID][key] = a
		}
		if h := strings.TrimSpace(row.Host); h != "" {
			a.Host, a.HostSet = h, true
		}
		if d := strings.TrimSpace(row.Desc); d != "" {
			a.Desc = d
		}
		if o := strings.TrimSpace(row.Owner); o != "" {
			a.Owner = o
		}
		if hw, err := net.ParseMAC(strings.TrimSpace(row.MAC)); err == nil {
			if row.Reserved {
				a.ResMAC = hw.String()
			} else if a.MAC == "" {
				a.MAC = hw.String()
				a.Vendor = netscan.Vendor(a.MAC)
			}
		}
		if row.Reserved {
			a.Reserved = true
		}
		markDirty(sn.ID, key)
		added++
	}
	ipam.mu.Unlock()
	s.flushIPAM()
	s.audit(r, "ipam", sn.ID, fmt.Sprintf(i18n.T("imported %d addresses"), added))
	writeJSON(w, map[string]int{"added": added, "skipped": skipped})
}

// handleIPAMSearch söker på IP, MAC, namn eller beskrivning i alla synliga subnät.
func (s *Server) handleIPAMSearch(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	type hit struct {
		IPAddr
		SubnetID   string `json:"subnetId"`
		CIDR       string `json:"cidr"`
		SubnetName string `json:"subnetName"`
		CustomerID string `json:"customerId"`
	}
	out := []hit{}
	if len(q) < 2 {
		writeJSON(w, out)
		return
	}
	qmac := strings.NewReplacer("-", ":", ".", "").Replace(q)
	ipam.mu.Lock()
	for id, sn := range ipam.subnets {
		if !canSee(u, sn.CustomerID) {
			continue
		}
		for _, a := range ipam.addrs[id] {
			if strings.Contains(a.IP, q) || (a.MAC != "" && strings.Contains(a.MAC, qmac)) || strings.Contains(strings.ToLower(a.Host), q) ||
				strings.Contains(strings.ToLower(a.Desc), q) || strings.Contains(strings.ToLower(a.Owner), q) || strings.Contains(strings.ToLower(a.Vendor), q) {
				out = append(out, hit{IPAddr: a.public(), SubnetID: id, CIDR: sn.CIDR, SubnetName: sn.Name, CustomerID: sn.CustomerID})
				if len(out) >= 200 {
					break
				}
			}
		}
	}
	ipam.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return ip4u(net.ParseIP(out[i].IP)) < ip4u(net.ParseIP(out[j].IP)) })
	writeJSON(w, out)
}

// deleteCustomerIPAM tar bort kundens subnät när kunden tas bort.
func (s *Server) deleteCustomerIPAM(cust string) {
	ipam.mu.Lock()
	for id, sn := range ipam.subnets {
		if sn.CustomerID == cust {
			delete(ipam.subnets, id)
			delete(ipam.nets, id)
			delete(ipam.addrs, id)
			delete(ipam.dirty, id)
			_, _ = s.db.Exec(`DELETE FROM ipam_addrs WHERE subnet_id=?`, id)
		}
	}
	ipam.mu.Unlock()
	_, _ = s.db.Exec(`DELETE FROM ipam_subnets WHERE customer_id=?`, cust)
}
