// Package tickets skapar och stänger ärenden i Jira, Freshdesk och Zendesk när larm går och upphör.
package tickets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Config är kopplingen till ärendesystemet.
type Config struct {
	Kind      string `json:"kind"` // "", jira, freshdesk, zendesk
	URL       string `json:"url,omitempty"`
	User      string `json:"user,omitempty"`  // Jira/Zendesk: e-post. Freshdesk: beställarens e-post
	Token     string `json:"token,omitempty"` // API-token / API-nyckel
	Project   string `json:"project,omitempty"`
	IssueType string `json:"issueType,omitempty"`
	AutoClose bool   `json:"autoClose,omitempty"` // stäng ärendet när larmet upphör (annars bara en kommentar)
}

// Enabled säger om ett ärendesystem är valt.
func (c Config) Enabled() bool {
	return c.Kind == "jira" || c.Kind == "freshdesk" || c.Kind == "zendesk"
}

// Name är systemets namn.
func (c Config) Name() string {
	return map[string]string{"jira": "Jira", "freshdesk": "Freshdesk", "zendesk": "Zendesk"}[c.Kind]
}

// Ticket är ett skapat ärende.
type Ticket struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

var client = &http.Client{Timeout: 30 * time.Second}

func (c Config) base() (string, error) {
	u := strings.TrimRight(strings.TrimSpace(c.URL), "/")
	if u == "" {
		return "", errors.New("enter the address of the ticket system")
	}
	if !strings.Contains(u, "://") {
		u = "https://" + u
	}
	return u, nil
}

func (c Config) call(ctx context.Context, method, path string, body any, out any) error {
	b, err := c.base()
	if err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		j, _ := json.Marshal(body)
		rd = bytes.NewReader(j)
	}
	req, err := http.NewRequestWithContext(ctx, method, b+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	switch c.Kind {
	case "jira":
		req.SetBasicAuth(c.User, c.Token)
	case "freshdesk":
		req.SetBasicAuth(c.Token, "X")
	case "zendesk":
		req.SetBasicAuth(c.User+"/token", c.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return errors.New("access denied – check the user and API token")
	}
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return errors.New("unexpected response from the ticket system")
		}
	}
	return nil
}

// adf gör Jiras dokumentformat av vanlig text (ett stycke per rad).
func adf(text string) map[string]any {
	var paras []any
	for _, l := range strings.Split(text, "\n") {
		p := map[string]any{"type": "paragraph"}
		if l != "" {
			p["content"] = []any{map[string]any{"type": "text", "text": l}}
		}
		paras = append(paras, p)
	}
	return map[string]any{"type": "doc", "version": 1, "content": paras}
}

func htmlLines(text string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return strings.ReplaceAll(r.Replace(text), "\n", "<br>")
}

// Create skapar ett ärende.
func Create(ctx context.Context, c Config, title, body string) (Ticket, error) {
	b, err := c.base()
	if err != nil {
		return Ticket{}, err
	}
	switch c.Kind {
	case "jira":
		if c.Project == "" {
			return Ticket{}, errors.New("enter the Jira project key")
		}
		it := c.IssueType
		if it == "" {
			it = "Task"
		}
		var res struct {
			Key string `json:"key"`
		}
		err := c.call(ctx, "POST", "/rest/api/3/issue", map[string]any{"fields": map[string]any{
			"project": map[string]string{"key": c.Project}, "summary": title, "issuetype": map[string]string{"name": it}, "description": adf(body)}}, &res)
		return Ticket{ID: res.Key, URL: b + "/browse/" + res.Key}, err
	case "freshdesk":
		if c.User == "" {
			return Ticket{}, errors.New("enter the requester email")
		}
		var res struct {
			ID int64 `json:"id"`
		}
		err := c.call(ctx, "POST", "/api/v2/tickets", map[string]any{"subject": title, "description": htmlLines(body), "email": c.User,
			"priority": 3, "status": 2, "tags": []string{"pingvakt"}}, &res)
		id := fmt.Sprint(res.ID)
		return Ticket{ID: id, URL: b + "/a/tickets/" + id}, err
	case "zendesk":
		var res struct {
			Ticket struct {
				ID int64 `json:"id"`
			} `json:"ticket"`
		}
		err := c.call(ctx, "POST", "/api/v2/tickets.json", map[string]any{"ticket": map[string]any{"subject": title,
			"comment": map[string]any{"body": body}, "priority": "high", "tags": []string{"pingvakt"}}}, &res)
		id := fmt.Sprint(res.Ticket.ID)
		return Ticket{ID: id, URL: b + "/agent/tickets/" + id}, err
	}
	return Ticket{}, errors.New("no ticket system is configured")
}

// Resolve lägger en kommentar och stänger ärendet om close är satt.
func Resolve(ctx context.Context, c Config, id, comment string, close bool) error {
	switch c.Kind {
	case "jira":
		if err := c.call(ctx, "POST", "/rest/api/3/issue/"+id+"/comment", map[string]any{"body": adf(comment)}, nil); err != nil {
			return err
		}
		if !close {
			return nil
		}
		var tr struct {
			Transitions []struct {
				ID string `json:"id"`
				To struct {
					Category struct {
						Key string `json:"key"`
					} `json:"statusCategory"`
				} `json:"to"`
			} `json:"transitions"`
		}
		if err := c.call(ctx, "GET", "/rest/api/3/issue/"+id+"/transitions", nil, &tr); err != nil {
			return err
		}
		for _, t := range tr.Transitions {
			if t.To.Category.Key == "done" {
				return c.call(ctx, "POST", "/rest/api/3/issue/"+id+"/transitions", map[string]any{"transition": map[string]string{"id": t.ID}}, nil)
			}
		}
		return errors.New("found no transition to a done status")
	case "freshdesk":
		if err := c.call(ctx, "POST", "/api/v2/tickets/"+id+"/notes", map[string]any{"body": htmlLines(comment), "private": true}, nil); err != nil {
			return err
		}
		if close {
			return c.call(ctx, "PUT", "/api/v2/tickets/"+id, map[string]any{"status": 4}, nil)
		}
		return nil
	case "zendesk":
		t := map[string]any{"comment": map[string]any{"body": comment, "public": false}}
		if close {
			t["status"] = "solved"
		}
		return c.call(ctx, "PUT", "/api/v2/tickets/"+id+".json", map[string]any{"ticket": t}, nil)
	}
	return nil
}
