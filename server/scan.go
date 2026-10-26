package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"pingvakt/core/agentproto"
	"pingvakt/core/i18n"
	"pingvakt/core/monitor"
	"pingvakt/core/netscan"
)

// ScanRun är en nätverksskanning som körs (eller körts) från en agent.
type ScanRun struct {
	ID      string           `json:"id"`
	AgentID string           `json:"agentId"`
	CIDR    string           `json:"cidr"`
	T       int64            `json:"t"`
	End     int64            `json:"end"`
	Devices []netscan.Device `json:"devices"`
	Err     string           `json:"err,omitempty"`
	Pending bool             `json:"pending,omitempty"`
	User    string           `json:"user,omitempty"`
}

type scanState struct {
	mu   sync.Mutex
	nets map[string][]netscan.Network // agent -> nät
	runs map[string][]*ScanRun        // agent -> senaste körningar (nyast först)
}

var scans = &scanState{nets: map[string][]netscan.Network{}, runs: map[string][]*ScanRun{}}

func (s *Server) ingestNets(agentID string, nets []netscan.Network) {
	scans.mu.Lock()
	scans.nets[agentID] = nets
	scans.mu.Unlock()
}

func (s *Server) scanAgent(r *http.Request, id string) (*Agent, bool) {
	u := userFrom(r)
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.agents[id]
	if !ok || !canSee(u, s.customerOfAgent(a)) {
		return nil, false
	}
	cp := *a
	return &cp, true
}

// handleScanInfo returnerar agentens nät och senaste skanningar.
func (s *Server) handleScanInfo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.scanAgent(r, id); !ok {
		httpErr(w, 404, "the agent does not exist")
		return
	}
	scans.mu.Lock()
	nets := scans.nets[id]
	runs := []ScanRun{}
	for _, x := range scans.runs[id] {
		runs = append(runs, *x)
	}
	scans.mu.Unlock()
	if nets == nil {
		nets = []netscan.Network{}
	}
	writeJSON(w, map[string]any{"networks": nets, "runs": runs})
}

// handleStartScan köar en skanning hos agenten.
func (s *Server) handleStartScan(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a, ok := s.scanAgent(r, id)
	if !ok {
		httpErr(w, 404, "the agent does not exist")
		return
	}
	var in struct {
		CIDR string `json:"cidr"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.CIDR = strings.TrimSpace(in.CIDR)
	if _, err := netscan.Hosts(in.CIDR); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	s.mu.RLock()
	online := s.agentOnline(a)
	s.mu.RUnlock()
	if !online {
		httpErr(w, 400, "the agent is offline")
		return
	}
	run := &ScanRun{ID: monitor.NewID(), AgentID: id, CIDR: in.CIDR, T: now(), Devices: []netscan.Device{}, Pending: true, User: userFrom(r).Email}
	scans.mu.Lock()
	list := append([]*ScanRun{run}, scans.runs[id]...)
	if len(list) > 5 {
		list = list[:5]
	}
	scans.runs[id] = list
	scans.mu.Unlock()
	traces.mu.Lock()
	traces.queue[id] = append(traces.queue[id], agentproto.Command{ID: run.ID, Kind: agentproto.CmdScan, Host: in.CIDR})
	traces.mu.Unlock()
	s.audit(r, "agent", id, i18n.T("network scan ")+in.CIDR)
	writeJSON(w, run)
}

// scanResult tar emot resultatet från agenten.
func (s *Server) scanResult(w http.ResponseWriter, agentID string, res agentproto.CommandResult) {
	scans.mu.Lock()
	var run *ScanRun
	for _, x := range scans.runs[agentID] {
		if x.ID == res.ID {
			run = x
		}
	}
	if run == nil {
		scans.mu.Unlock()
		httpErr(w, 404, "unknown command")
		return
	}
	run.Pending, run.End, run.Err = false, res.End, res.Err
	if res.Devices != nil {
		run.Devices = res.Devices
	}
	cp := *run
	scans.mu.Unlock()
	s.mu.RLock()
	cust := ""
	if a, ok := s.agents[agentID]; ok {
		cust = s.customerOfAgent(a)
	}
	s.mu.RUnlock()
	b, _ := json.Marshal(map[string]any{"type": "scan", "scan": cp})
	s.broadcastRaw(cust, b)
	writeJSON(w, map[string]bool{"ok": true})
}
