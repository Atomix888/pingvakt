package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"pingvakt/core/alert"
)

func TestOnCallRotation(t *testing.T) {
	start := time.Date(2026, 10, 26, 8, 0, 0, 0, time.Local)
	o := OnCall{Members: []string{"a", "b", "c"}, Start: start.UnixMilli(), PeriodDays: 7}
	cases := map[time.Time]string{
		start.Add(time.Hour):    "a",
		start.AddDate(0, 0, 7):  "b",
		start.AddDate(0, 0, 15): "c",
		start.AddDate(0, 0, 21): "a",
		start.AddDate(0, 0, -1): "c", // före start: bakåt i turordningen
	}
	for at, want := range cases {
		if got := o.Current(at); got != want {
			t.Errorf("%v: fick %s, ville %s", at, got, want)
		}
	}
	o.Overrides = []OnCallOverride{{From: start.AddDate(0, 0, 8).UnixMilli(), To: start.AddDate(0, 0, 10).UnixMilli(), UserID: "x"}}
	if got := o.Current(start.AddDate(0, 0, 9)); got != "x" {
		t.Errorf("ersättare: %s", got)
	}
	if up := o.Upcoming(start.Add(time.Hour), 3); len(up) != 3 || up[1].UserID != "b" {
		t.Errorf("kommande: %+v", up)
	}
	if normPhone("070-123 45 67") != "+46701234567" || normPhone("0046 70 1234567") != "+46701234567" || normPhone("+4670123") != "+4670123" {
		t.Error("telefonnummer")
	}
}

func TestChannels(t *testing.T) {
	var mu sync.Mutex
	got := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u, p, _ := r.BasicAuth()
		mu.Lock()
		got[r.URL.Path] = string(b) + "|" + r.Header.Get("Title") + "|" + u + ":" + p
		mu.Unlock()
	}))
	defer srv.Close()
	pushoverURL, elksURL, twilioURL = srv.URL+"/pushover", srv.URL+"/elks", srv.URL+"/twilio/%s"
	ev := alert.Event{Kind: "fire", AlertName: "Nere", TargetName: "Router", Host: "10.0.0.1", Detail: "3 av 3", Time: time.Now().UnixMilli()}
	if err := sendNtfy(srv.URL, "", "topic", ev, ""); err != nil {
		t.Fatal(err)
	}
	if err := sendPushover("tok", "usr", ev, "https://x"); err != nil {
		t.Fatal(err)
	}
	if err := sendSMS(NotifySettings{SMSProvider: "46elks", SMSUser: "u", SMSPass: "p"}, "0701234567", "hej"); err != nil {
		t.Fatal(err)
	}
	if err := sendSMS(NotifySettings{SMSProvider: "twilio", SMSUser: "AC1", SMSPass: "tok", SMSFrom: "+4610"}, "0701234567", "hej"); err != nil {
		t.Fatal(err)
	}
	if err := sendTeams(srv.URL+"/teams", ev, "https://x"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got["/"], `"topic":"topic"`) || !strings.Contains(got["/"], "Router") {
		t.Errorf("ntfy: %q", got["/"])
	}
	if v, _ := url.ParseQuery(strings.Split(got["/pushover"], "|")[0]); v.Get("user") != "usr" || v.Get("priority") != "1" {
		t.Errorf("pushover: %q", got["/pushover"])
	}
	if v, _ := url.ParseQuery(strings.Split(got["/elks"], "|")[0]); v.Get("to") != "+46701234567" || !strings.HasSuffix(got["/elks"], "u:p") {
		t.Errorf("46elks: %q", got["/elks"])
	}
	if v, _ := url.ParseQuery(strings.Split(got["/twilio/AC1"], "|")[0]); v.Get("To") != "+46701234567" || v.Get("From") != "+4610" {
		t.Errorf("twilio: %q", got["/twilio/AC1"])
	}
	if !strings.Contains(got["/teams"], "AdaptiveCard") {
		t.Errorf("teams: %q", got["/teams"])
	}
}
