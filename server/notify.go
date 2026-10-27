package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"pingvakt/core/alert"
	"pingvakt/core/i18n"
	"pingvakt/core/monitor"
)

// NotifySettings är serverns inställningar för push och SMS.
type NotifySettings struct {
	NtfyURL       string `json:"ntfyUrl"`       // standard https://ntfy.sh
	NtfyTopic     string `json:"ntfyTopic"`     // gemensamt ämne (valfritt)
	NtfyToken     string `json:"ntfyToken"`     // åtkomsttoken (valfritt)
	PushoverToken string `json:"pushoverToken"` // applikationens token
	PushoverUser  string `json:"pushoverUser"`  // gemensam användar-/gruppnyckel (valfritt)
	SMSProvider   string `json:"smsProvider"`   // "", "46elks", "twilio"
	SMSUser       string `json:"smsUser"`       // 46elks API-användare eller Twilio Account SID
	SMSPass       string `json:"smsPass"`       // 46elks API-lösenord eller Twilio Auth Token
	SMSFrom       string `json:"smsFrom"`       // avsändare (namn eller nummer)
	SMSMinMinutes int    `json:"smsMinMinutes"` // högst ett SMS per larm och mål så här ofta
	VAPIDPublic   string `json:"vapidPublic"`
	VAPIDPrivate  string `json:"vapidPrivate,omitempty"`
}

const secretMask = "••••••••"

// Adresser till tjänsterna (variabler så att testerna kan peka om dem).
var (
	pushoverURL = "https://api.pushover.net/1/messages.json"
	elksURL     = "https://api.46elks.com/a1/sms"
	twilioURL   = "https://api.twilio.com/2010-04-01/Accounts/%s/Messages.json"
)

// redact döljer hemligheter innan inställningarna skickas till webbläsaren.
func (n NotifySettings) redact() NotifySettings {
	for _, p := range []*string{&n.NtfyToken, &n.PushoverToken, &n.SMSPass} {
		if *p != "" {
			*p = secretMask
		}
	}
	n.VAPIDPrivate = ""
	return n
}

// merge behåller hemligheter som inte ändrats.
func (n NotifySettings) merge(old NotifySettings) NotifySettings {
	keep := func(p *string, o string) {
		if *p == secretMask {
			*p = o
		}
	}
	keep(&n.NtfyToken, old.NtfyToken)
	keep(&n.PushoverToken, old.PushoverToken)
	keep(&n.SMSPass, old.SMSPass)
	n.VAPIDPublic, n.VAPIDPrivate = old.VAPIDPublic, old.VAPIDPrivate
	n.NtfyURL = strings.TrimSuffix(strings.TrimSpace(n.NtfyURL), "/")
	n.NtfyTopic = strings.TrimSpace(n.NtfyTopic)
	if n.SMSMinMinutes <= 0 {
		n.SMSMinMinutes = 10
	}
	switch n.SMSProvider {
	case "46elks", "twilio":
	default:
		n.SMSProvider = ""
	}
	return n
}

const notifySchema = `
CREATE TABLE IF NOT EXISTS webpush (
  endpoint TEXT PRIMARY KEY, user_id TEXT NOT NULL, p256dh TEXT NOT NULL, auth TEXT NOT NULL, created INTEGER NOT NULL, agent TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS oncall (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, customer_id TEXT NOT NULL, data TEXT NOT NULL
);
`

// ensureVAPID skapar nycklar för Web Push första gången.
func (s *Server) ensureVAPID() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings.Notify.VAPIDPrivate != "" {
		return
	}
	pub, priv, err := newVAPID()
	if err != nil {
		log.Printf("web push keys: %v", err)
		return
	}
	s.settings.Notify.VAPIDPublic, s.settings.Notify.VAPIDPrivate = pub, priv
	_ = s.db.SaveSettings(s.settings)
}

// ---------- kontaktuppgifter ----------

// Contact är en användares kanaler för larm.
type Contact struct {
	UserID   string `json:"userId"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Customer string `json:"customerId"`
	Phone    string `json:"phone"`
	Pushover string `json:"pushover"`
	Ntfy     string `json:"ntfy"`
}

func (s *Server) contacts() map[string]Contact {
	out := map[string]Contact{}
	rows, err := s.db.Query(`SELECT id,email,name,role,customer_id,phone,pushover,ntfy FROM users`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var c Contact
		if rows.Scan(&c.UserID, &c.Email, &c.Name, &c.Role, &c.Customer, &c.Phone, &c.Pushover, &c.Ntfy) == nil {
			out[c.UserID] = c
		}
	}
	return out
}

var rePhoneJunk = regexp.MustCompile(`[\s\-()./]`)

// normPhone gör om ett telefonnummer till internationellt format (+46…).
func normPhone(p string) string {
	p = rePhoneJunk.ReplaceAllString(strings.TrimSpace(p), "")
	switch {
	case p == "":
		return ""
	case strings.HasPrefix(p, "+"):
		return p
	case strings.HasPrefix(p, "00"):
		return "+" + p[2:]
	case strings.HasPrefix(p, "0"):
		return "+46" + p[1:] // svenskt nummer
	}
	return "+" + p
}

// ---------- kanaler ----------

func eventIcon(kind string) string {
	switch kind {
	case "fire", "repeat":
		return "🔴"
	case "clear":
		return "🟢"
	}
	return "🔵"
}

func eventTitle(ev alert.Event) string {
	return fmt.Sprintf("%s %s: %s", eventIcon(ev.Kind), ev.KindText(), ev.TargetName)
}

func eventText(ev alert.Event) string {
	t := ev.AlertName
	if ev.Detail != "" {
		t += " – " + ev.Detail
	}
	return t
}

func postForm(u, user, pass string, form url.Values) error {
	req, err := http.NewRequest(http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	return doReq(req)
}

func doReq(req *http.Request) error {
	res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return fmt.Errorf("%s responded %d: %s", req.URL.Host, res.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func sendNtfy(base, token, topic string, ev alert.Event, link string) error {
	if base == "" {
		base = "https://ntfy.sh"
	}
	// JSON-publicering klarar UTF-8 i rubriken (http-huvuden gör det inte)
	prio, tag := 3, "large_blue_circle"
	switch ev.Kind {
	case "fire", "repeat":
		prio, tag = 4, "red_circle"
	case "clear":
		tag = "green_circle"
	}
	msg := map[string]any{"topic": topic, "title": strings.TrimSpace(strings.TrimPrefix(eventTitle(ev), eventIcon(ev.Kind))), "message": eventText(ev),
		"priority": prio, "tags": []string{tag}}
	if link != "" {
		msg["click"] = link
	}
	body, _ := json.Marshal(msg)
	req, err := http.NewRequest(http.MethodPost, base, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return doReq(req)
}

func sendPushover(token, user string, ev alert.Event, link string) error {
	prio := "0"
	if ev.Kind == "fire" || ev.Kind == "repeat" {
		prio = "1"
	}
	f := url.Values{"token": {token}, "user": {user}, "title": {eventTitle(ev)}, "message": {eventText(ev)}, "priority": {prio}}
	if link != "" {
		f.Set("url", link)
	}
	return postForm(pushoverURL, "", "", f)
}

func sendSMS(n NotifySettings, to, text string) error {
	to = normPhone(to)
	if to == "" {
		return errors.New("no phone number")
	}
	if len([]rune(text)) > 300 {
		text = string([]rune(text)[:297]) + "…"
	}
	from := n.SMSFrom
	if from == "" {
		from = "PingVakt"
	}
	switch n.SMSProvider {
	case "46elks":
		return postForm(elksURL, n.SMSUser, n.SMSPass, url.Values{"from": {from}, "to": {to}, "message": {text}})
	case "twilio":
		return postForm(fmt.Sprintf(twilioURL, url.PathEscape(n.SMSUser)), n.SMSUser, n.SMSPass,
			url.Values{"From": {from}, "To": {to}, "Body": {text}})
	}
	return errors.New("no SMS provider configured")
}

func sendTeams(hook string, ev alert.Event, link string) error {
	color := "Accent"
	switch ev.Kind {
	case "fire", "repeat":
		color = "Attention"
	case "clear":
		color = "Good"
	}
	facts := []map[string]string{{"title": i18n.T("Alert"), "value": ev.AlertName}}
	if ev.Host != "" {
		facts = append(facts, map[string]string{"title": i18n.T("Host"), "value": ev.Host})
	}
	if ev.Detail != "" {
		facts = append(facts, map[string]string{"title": i18n.T("Detail"), "value": ev.Detail})
	}
	facts = append(facts, map[string]string{"title": i18n.T("Time"), "value": time.UnixMilli(ev.Time).Format("2006-01-02 15:04:05")})
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json", "type": "AdaptiveCard", "version": "1.4",
		"body": []any{
			map[string]any{"type": "TextBlock", "size": "Medium", "weight": "Bolder", "wrap": true, "color": color, "text": eventTitle(ev)},
			map[string]any{"type": "FactSet", "facts": facts},
		},
	}
	if link != "" {
		card["actions"] = []any{map[string]any{"type": "Action.OpenUrl", "title": i18n.T("Open PingVakt"), "url": link}}
	}
	body, _ := json.Marshal(map[string]any{"type": "message", "attachments": []any{map[string]any{
		"contentType": "application/vnd.microsoft.card.adaptive", "contentUrl": nil, "content": card}}})
	req, err := http.NewRequest(http.MethodPost, hook, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return doReq(req)
}

// ---------- utskick ----------

type notifyState struct {
	mu      sync.Mutex
	lastSMS map[string]time.Time // regel|mål|nummer
	sentSMS map[string]bool      // regel|mål -> ett larm-SMS har gått iväg (då skickas även "upphört")
}

var nstate = &notifyState{lastSMS: map[string]time.Time{}, sentSMS: map[string]bool{}}

func (s *Server) customerOfEvent(ev alert.Event) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if t, ok := s.targets[ev.TargetID]; ok {
		return s.customerOfTarget(t)
	}
	if a, ok := s.agents[ev.TargetID]; ok {
		return s.customerOfAgent(a)
	}
	s.mu.RUnlock()
	_, c, _ := s.extraInfo(ev.TargetID)
	s.mu.RLock()
	return c
}

func (s *Server) eventLink(ev alert.Event) string {
	s.mu.RLock()
	pub := s.settings.PublicURL
	s.mu.RUnlock()
	if pub == "" {
		return ""
	}
	return pub + "/#/alerts"
}

// notifyExtra skickar larmet via serverns egna kanaler (anropas av larmmotorn).
func (s *Server) notifyExtra(a alert.Alert, ev alert.Event) {
	if ev.Kind == "test" {
		return
	}
	go func() {
		if err := s.notify(a, ev, false); err != nil {
			log.Printf("notification %s: %v", a.Name, err)
		}
	}()
}

// notify skickar till alla kanaler som regeln har. test = skicka direkt, utan spärrar.
func (s *Server) notify(a alert.Alert, ev alert.Event, test bool) error {
	s.mu.RLock()
	n := s.settings.Notify
	s.mu.RUnlock()
	cust := s.customerOfEvent(ev)
	link := s.eventLink(ev)
	var errs []string
	add := func(what string, err error) {
		if err != nil {
			errs = append(errs, what+": "+err.Error())
		}
	}
	key := a.ID + "|" + ev.TargetID

	// SMS – med spärr så att det inte blir dyrt
	smsOK := func(number string) bool {
		if test {
			return true
		}
		nstate.mu.Lock()
		defer nstate.mu.Unlock()
		k := key + "|" + number
		switch ev.Kind {
		case "clear":
			return nstate.sentSMS[key]
		case "fire", "repeat":
			if time.Since(nstate.lastSMS[k]) < time.Duration(n.SMSMinMinutes)*time.Minute {
				return false
			}
			nstate.lastSMS[k] = time.Now()
			return true
		}
		return false
	}
	smsText := strings.TrimSpace(fmt.Sprintf("PingVakt %s: %s – %s", ev.KindText(), ev.TargetName, eventText(ev)))
	numbers := map[string]bool{}
	if a.SMS {
		for _, p := range alert.SplitAddresses(strings.ReplaceAll(a.SMSTo, ";", ",")) {
			numbers[normPhone(p)] = true
		}
	}

	// jour
	contacts := s.contacts()
	pushUsers := map[string]bool{}
	if a.OnCall != "" {
		if sch, ok := s.oncall(a.OnCall); ok {
			if uid := sch.Current(time.Now()); uid != "" {
				c := contacts[uid]
				if sch.SMS && c.Phone != "" {
					numbers[normPhone(c.Phone)] = true
				}
				if sch.Push {
					pushUsers[uid] = true
				}
				if sch.Email && c.Email != "" && s.mailer != nil {
					if test {
						add(i18n.T("email"), s.mailer.SendNow([]string{c.Email}, []alert.Event{ev}))
					} else {
						s.mailer.Queue(a.ID+"|oncall|"+uid, []string{c.Email}, ev)
					}
				}
			} else if test {
				add(i18n.T("On-call schedule"), errors.New(i18n.T("nobody is on call right now")))
			}
		}
	}
	if len(numbers) > 0 {
		if n.SMSProvider == "" {
			add("SMS", errors.New(i18n.T("no SMS provider configured")))
		} else {
			sent := false
			for num := range numbers {
				if num != "" && smsOK(num) {
					add("SMS "+num, sendSMS(n, num, smsText))
					sent = true
				}
			}
			if sent && !test {
				nstate.mu.Lock()
				if ev.Kind == "clear" {
					delete(nstate.sentSMS, key)
				} else {
					nstate.sentSMS[key] = true
				}
				nstate.mu.Unlock()
			}
		}
	}

	// push
	if a.Push {
		if n.NtfyTopic != "" {
			add("ntfy", sendNtfy(n.NtfyURL, n.NtfyToken, n.NtfyTopic, ev, link))
		}
		if n.PushoverToken != "" && n.PushoverUser != "" {
			add("Pushover", sendPushover(n.PushoverToken, n.PushoverUser, ev, link))
		}
		for id, c := range contacts {
			if c.Role == "admin" || (cust != "" && c.Customer == cust) {
				pushUsers[id] = true
			}
		}
	}
	for uid := range pushUsers {
		c := contacts[uid]
		if c.Ntfy != "" {
			add("ntfy "+c.Email, sendNtfy(n.NtfyURL, n.NtfyToken, c.Ntfy, ev, link))
		}
		if c.Pushover != "" && n.PushoverToken != "" {
			add("Pushover "+c.Email, sendPushover(n.PushoverToken, c.Pushover, ev, link))
		}
		add("Web Push "+c.Email, s.webPushUser(uid, ev, link))
	}

	if a.Teams && a.TeamsURL != "" {
		add("Teams", sendTeams(a.TeamsURL, ev, link))
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// webPushUser skickar till alla webbläsare/telefoner där användaren slagit på notiser.
func (s *Server) webPushUser(userID string, ev alert.Event, link string) error {
	s.mu.RLock()
	n := s.settings.Notify
	s.mu.RUnlock()
	if n.VAPIDPrivate == "" {
		return nil
	}
	k, err := parseVAPID(n.VAPIDPrivate)
	if err != nil {
		return err
	}
	rows, err := s.db.Query(`SELECT endpoint,p256dh,auth FROM webpush WHERE user_id=?`, userID)
	if err != nil {
		return err
	}
	var subs []WebPushSub
	for rows.Next() {
		var sub WebPushSub
		if rows.Scan(&sub.Endpoint, &sub.Keys.P256dh, &sub.Keys.Auth) == nil {
			subs = append(subs, sub)
		}
	}
	rows.Close()
	if len(subs) == 0 {
		return nil
	}
	payload, _ := json.Marshal(map[string]any{"title": eventTitle(ev), "body": eventText(ev), "tag": ev.AlertID + "|" + ev.TargetID, "url": "/#/alerts",
		"kind": ev.Kind})
	subject := "mailto:pingvakt@localhost"
	if link != "" {
		subject = strings.TrimSuffix(link, "/#/alerts")
	}
	var last error
	for _, sub := range subs {
		err := sendWebPush(sub, payload, k, n.VAPIDPublic, subject, ev.Kind == "fire")
		if errors.Is(err, errGone) {
			_, _ = s.db.Exec(`DELETE FROM webpush WHERE endpoint=?`, sub.Endpoint)
			continue
		}
		if err != nil {
			last = err
		}
	}
	return last
}

// ---------- jourscheman ----------

// OnCallOverride ersätter schemat under en period (t.ex. semester).
type OnCallOverride struct {
	From   int64  `json:"from"`
	To     int64  `json:"to"`
	UserID string `json:"userId"`
}

// OnCall är ett jourschema som roterar mellan medlemmarna.
type OnCall struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	CustomerID string           `json:"customerId"`
	Members    []string         `json:"members"`    // användar-ID i turordning
	Start      int64            `json:"start"`      // när första personen börjar (ms)
	PeriodDays int              `json:"periodDays"` // hur länge varje pass varar
	Email      bool             `json:"email"`
	SMS        bool             `json:"sms"`
	Push       bool             `json:"push"`
	Overrides  []OnCallOverride `json:"overrides"`
}

// Current returnerar vem som har jour vid tiden t.
func (o OnCall) Current(t time.Time) string {
	ms := t.UnixMilli()
	for _, ov := range o.Overrides {
		if ms >= ov.From && ms < ov.To && ov.UserID != "" {
			return ov.UserID
		}
	}
	if len(o.Members) == 0 {
		return ""
	}
	period := int64(o.PeriodDays) * 86400000
	if period <= 0 {
		period = 7 * 86400000
	}
	d := ms - o.Start
	idx := int64(0)
	if d > 0 {
		idx = d / period
	} else {
		idx = -((-d + period - 1) / period)
	}
	n := int64(len(o.Members))
	return o.Members[((idx%n)+n)%n]
}

// Upcoming returnerar de kommande passen (start, slut, användare).
func (o OnCall) Upcoming(from time.Time, count int) []OnCallOverride {
	var out []OnCallOverride
	if len(o.Members) == 0 {
		return out
	}
	period := int64(o.PeriodDays) * 86400000
	if period <= 0 {
		period = 7 * 86400000
	}
	ms := from.UnixMilli()
	start := o.Start
	if ms > start {
		start += (ms - start) / period * period
	} else {
		start -= ((start - ms + period - 1) / period) * period
	}
	for i := 0; i < count; i++ {
		t0 := start + int64(i)*period
		out = append(out, OnCallOverride{From: t0, To: t0 + period, UserID: o.Current(time.UnixMilli(t0 + 1))})
	}
	return out
}

func (s *Server) oncall(id string) (OnCall, bool) {
	var data string
	if err := s.db.QueryRow(`SELECT data FROM oncall WHERE id=?`, id).Scan(&data); err != nil {
		return OnCall{}, false
	}
	var o OnCall
	if json.Unmarshal([]byte(data), &o) != nil {
		return OnCall{}, false
	}
	return o, true
}

func (s *Server) oncalls() []OnCall {
	out := []OnCall{}
	rows, err := s.db.Query(`SELECT data FROM oncall ORDER BY name`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var data string
		var o OnCall
		if rows.Scan(&data) == nil && json.Unmarshal([]byte(data), &o) == nil {
			out = append(out, o)
		}
	}
	return out
}

// ---------- API ----------

func (s *Server) handleOnCallList(w http.ResponseWriter, r *http.Request) {
	list := s.oncalls()
	type outT struct {
		OnCall
		Now      string           `json:"now"`
		Upcoming []OnCallOverride `json:"upcoming"`
	}
	out := []outT{}
	for _, o := range list {
		out = append(out, outT{o, o.Current(time.Now()), o.Upcoming(time.Now(), 6)})
	}
	writeJSON(w, out)
}

func (s *Server) handleOnCallSave(w http.ResponseWriter, r *http.Request) {
	var in OnCall
	if !readJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		httpErr(w, 400, "name missing")
		return
	}
	if id := r.PathValue("id"); id != "" {
		in.ID = id
	}
	if in.ID == "" {
		in.ID = monitor.NewID()
	}
	if in.PeriodDays <= 0 {
		in.PeriodDays = 7
	}
	if in.Start == 0 {
		in.Start = now()
	}
	sort.SliceStable(in.Overrides, func(i, j int) bool { return in.Overrides[i].From < in.Overrides[j].From })
	b, _ := json.Marshal(in)
	if _, err := s.db.Exec(`INSERT INTO oncall(id,name,customer_id,data) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name, customer_id=excluded.customer_id, data=excluded.data`,
		in.ID, in.Name, in.CustomerID, string(b)); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.audit(r, "alert", in.ID, i18n.T("saved on-call schedule ")+in.Name)
	writeJSON(w, in)
}

func (s *Server) handleOnCallDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, _ = s.db.Exec(`DELETE FROM oncall WHERE id=?`, id)
	s.audit(r, "alert", id, i18n.T("deleted on-call schedule"))
	writeJSON(w, map[string]bool{"ok": true})
}

// handleMyNotify sparar den inloggades egna kanaler.
func (s *Server) handleMyNotify(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Phone, Pushover, Ntfy string
	}
	if !readJSON(w, r, &in) {
		return
	}
	u := userFrom(r)
	_, _ = s.db.Exec(`UPDATE users SET phone=?, pushover=?, ntfy=? WHERE id=?`, normPhone(in.Phone), strings.TrimSpace(in.Pushover), strings.TrimSpace(in.Ntfy), u.ID)
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleMyNotifyGet(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	c := s.contacts()[u.ID]
	var subs int
	_ = s.db.QueryRow(`SELECT count(*) FROM webpush WHERE user_id=?`, u.ID).Scan(&subs)
	s.mu.RLock()
	n := s.settings.Notify
	s.mu.RUnlock()
	writeJSON(w, map[string]any{"phone": c.Phone, "pushover": c.Pushover, "ntfy": c.Ntfy, "webPushDevices": subs,
		"vapidPublic": n.VAPIDPublic, "hasPushover": n.PushoverToken != "", "hasSMS": n.SMSProvider != "", "ntfyUrl": n.NtfyURL})
}

func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	var in WebPushSub
	if !readJSON(w, r, &in) {
		return
	}
	if !strings.HasPrefix(in.Endpoint, "https://") || in.Keys.P256dh == "" || in.Keys.Auth == "" {
		httpErr(w, 400, "invalid subscription")
		return
	}
	u := userFrom(r)
	_, err := s.db.Exec(`INSERT INTO webpush(endpoint,user_id,p256dh,auth,created,agent) VALUES(?,?,?,?,?,?)
		ON CONFLICT(endpoint) DO UPDATE SET user_id=excluded.user_id, p256dh=excluded.p256dh, auth=excluded.auth`,
		in.Endpoint, u.ID, in.Keys.P256dh, in.Keys.Auth, now(), r.UserAgent())
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var in struct{ Endpoint string }
	if !readJSON(w, r, &in) {
		return
	}
	_, _ = s.db.Exec(`DELETE FROM webpush WHERE endpoint=? AND user_id=?`, in.Endpoint, userFrom(r).ID)
	writeJSON(w, map[string]bool{"ok": true})
}

// handleNotifyTest skickar ett test till den inloggades egna kanaler.
func (s *Server) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	c := s.contacts()[u.ID]
	s.mu.RLock()
	n := s.settings.Notify
	s.mu.RUnlock()
	ev := alert.Event{Kind: "test", AlertName: i18n.T("Test alert"), TargetName: i18n.T("Test target"), Host: "192.0.2.1", Time: now(), Detail: i18n.T("This is a test of the alert")}
	link := s.eventLink(ev)
	var errs []string
	sent := 0
	try := func(what string, err error) {
		sent++
		if err != nil {
			errs = append(errs, what+": "+err.Error())
		}
	}
	if c.Ntfy != "" {
		try("ntfy", sendNtfy(n.NtfyURL, n.NtfyToken, c.Ntfy, ev, link))
	}
	if c.Pushover != "" && n.PushoverToken != "" {
		try("Pushover", sendPushover(n.PushoverToken, c.Pushover, ev, link))
	}
	if c.Phone != "" && n.SMSProvider != "" {
		try("SMS", sendSMS(n, c.Phone, "PingVakt: "+i18n.T("This is a test of the alert")))
	}
	var subs int
	_ = s.db.QueryRow(`SELECT count(*) FROM webpush WHERE user_id=?`, u.ID).Scan(&subs)
	if subs > 0 {
		try("Web Push", s.webPushUser(u.ID, ev, link))
	}
	if sent == 0 {
		httpErr(w, 400, "no channels to test – enter a phone number, Pushover key or ntfy topic, or turn on notifications on this device")
		return
	}
	if len(errs) > 0 {
		httpErr(w, 400, strings.Join(errs, "; "))
		return
	}
	writeJSON(w, map[string]int{"sent": sent})
}
