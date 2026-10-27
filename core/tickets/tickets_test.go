package tickets

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJira(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "jonas@x.se" || p != "tok" {
			w.WriteHeader(401)
			return
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/rest/api/3/issue":
			var in map[string]map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			if in["fields"]["summary"] != "AP nere" {
				t.Errorf("%v", in)
			}
			w.Write([]byte(`{"id":"1","key":"NET-7"}`))
		case strings.HasSuffix(r.URL.Path, "/transitions") && r.Method == "GET":
			w.Write([]byte(`{"transitions":[{"id":"11","to":{"statusCategory":{"key":"indeterminate"}}},{"id":"31","to":{"statusCategory":{"key":"done"}}}]}`))
		case strings.HasSuffix(r.URL.Path, "/transitions"):
			var in map[string]map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			if in["transition"]["id"] != "31" {
				t.Errorf("övergång %v", in)
			}
			w.WriteHeader(204)
		default:
			w.WriteHeader(201)
		}
	}))
	defer srv.Close()
	c := Config{Kind: "jira", URL: srv.URL, User: "jonas@x.se", Token: "tok", Project: "NET"}
	tk, err := Create(context.Background(), c, "AP nere", "rad 1\nrad 2")
	if err != nil || tk.ID != "NET-7" || tk.URL != srv.URL+"/browse/NET-7" {
		t.Fatalf("%v %+v", err, tk)
	}
	if err := Resolve(context.Background(), c, tk.ID, "Klart", true); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "POST /rest/api/3/issue,POST /rest/api/3/issue/NET-7/comment,GET /rest/api/3/issue/NET-7/transitions,POST /rest/api/3/issue/NET-7/transitions" {
		t.Errorf("%v", calls)
	}
}

func TestFreshdeskZendesk(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, _ := r.BasicAuth()
		got = append(got, r.Method+" "+r.URL.Path+" "+u+":"+p)
		switch r.URL.Path {
		case "/api/v2/tickets":
			w.Write([]byte(`{"id":42}`))
		case "/api/v2/tickets.json":
			w.Write([]byte(`{"ticket":{"id":43}}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	fd := Config{Kind: "freshdesk", URL: srv.URL, User: "larm@x.se", Token: "key"}
	tk, err := Create(context.Background(), fd, "t", "b")
	if err != nil || tk.ID != "42" || !strings.HasSuffix(tk.URL, "/a/tickets/42") {
		t.Fatal(err, tk)
	}
	_ = Resolve(context.Background(), fd, "42", "ok", true)
	zd := Config{Kind: "zendesk", URL: srv.URL, User: "jonas@x.se", Token: "zt"}
	tk, err = Create(context.Background(), zd, "t", "b")
	if err != nil || tk.ID != "43" {
		t.Fatal(err, tk)
	}
	_ = Resolve(context.Background(), zd, "43", "ok", false)
	want := []string{"POST /api/v2/tickets key:X", "POST /api/v2/tickets/42/notes key:X", "PUT /api/v2/tickets/42 key:X",
		"POST /api/v2/tickets.json jonas@x.se/token:zt", "PUT /api/v2/tickets/43.json jonas@x.se/token:zt"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("%v", got)
	}
}
