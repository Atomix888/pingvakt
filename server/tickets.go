package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"pingvakt/core/alert"
	"pingvakt/core/i18n"
	"pingvakt/core/tickets"
)

const ticketSchema = `CREATE TABLE IF NOT EXISTS tickets (key TEXT PRIMARY KEY, id TEXT NOT NULL, url TEXT NOT NULL, t INTEGER NOT NULL);`

func redactTickets(c tickets.Config) tickets.Config { c.Token = maskIf(c.Token); return c }

func mergeTickets(c, old tickets.Config) tickets.Config {
	if c.Token == secretMask {
		c.Token = old.Token
	}
	c.URL, c.User, c.Project, c.IssueType = strings.TrimSpace(c.URL), strings.TrimSpace(c.User), strings.TrimSpace(c.Project), strings.TrimSpace(c.IssueType)
	return c
}

// handleTicket skapar ett ärende när larmet går och löser det när larmet upphör.
func (s *Server) handleTicket(a alert.Alert, ev alert.Event) {
	if !a.Ticket || (ev.Kind != "fire" && ev.Kind != "clear") {
		return
	}
	s.mu.RLock()
	cfg := s.settings.Tickets
	s.mu.RUnlock()
	if !cfg.Enabled() {
		return
	}
	key := a.ID + "|" + ev.TargetID
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	when := time.UnixMilli(ev.Time).Format("2006-01-02 15:04")
	if ev.Kind == "fire" {
		var id string
		if s.db.QueryRow(`SELECT id FROM tickets WHERE key=?`, key).Scan(&id) == nil {
			return // ett ärende är redan öppet
		}
		title := fmt.Sprintf("%s: %s", a.Name, ev.TargetName)
		if len(title) > 240 {
			title = title[:240]
		}
		body := fmt.Sprintf("%s\n\n%s: %s\n%s: %s\n%s: %s", ev.Detail, i18n.T("Target"), ev.TargetName, i18n.T("Alert"), a.Name, i18n.T("Time"), when)
		if link := s.eventLink(ev); link != "" {
			body += "\n" + link
		}
		body += "\n\n" + i18n.T("Created by PingVakt. The ticket is updated when the alert clears.")
		t, err := tickets.Create(ctx, cfg, title, body)
		if err != nil {
			log.Printf("ticket %s: %v", cfg.Name(), err)
			return
		}
		_, _ = s.db.Exec(`INSERT OR REPLACE INTO tickets(key,id,url,t) VALUES(?,?,?,?)`, key, t.ID, t.URL, now())
		return
	}
	var id string
	if s.db.QueryRow(`SELECT id FROM tickets WHERE key=?`, key).Scan(&id) != nil {
		return
	}
	_, _ = s.db.Exec(`DELETE FROM tickets WHERE key=?`, key)
	msg := fmt.Sprintf("%s %s: %s", i18n.T("Cleared"), when, ev.Detail)
	if err := tickets.Resolve(ctx, cfg, id, msg, cfg.AutoClose); err != nil {
		log.Printf("ticket %s %s: %v", cfg.Name(), id, err)
	}
}

func (s *Server) ticketURLs() map[string]string {
	out := map[string]string{}
	rows, err := s.db.Query(`SELECT key,url FROM tickets`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k, u string
		if rows.Scan(&k, &u) == nil {
			out[k] = u
		}
	}
	return out
}

// handleTestTicket skapar ett testärende och stänger det direkt.
func (s *Server) handleTestTicket(w http.ResponseWriter, r *http.Request) {
	var in tickets.Config
	if !readJSON(w, r, &in) {
		return
	}
	s.mu.RLock()
	in = mergeTickets(in, s.settings.Tickets)
	s.mu.RUnlock()
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	t, err := tickets.Create(ctx, in, i18n.T("PingVakt test ticket"), i18n.T("This ticket was created to test the connection from PingVakt and is closed right away."))
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if err := tickets.Resolve(ctx, in, t.ID, i18n.T("Test finished."), true); err != nil {
		writeJSON(w, map[string]any{"ok": true, "url": t.URL, "warning": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "url": t.URL})
}
