package vendors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUniFiLogin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "TOKEN", Value: "x", Path: "/"})
			w.Header().Set("X-Csrf-Token", "csrf")
			w.Write([]byte(`{}`))
		case "/proxy/network/api/s/default/stat/device":
			if c, err := r.Cookie("TOKEN"); err != nil || c.Value != "x" {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(`{"data":[{"mac":"aa","ip":"10.0.0.2","name":"AP Kontor","model":"U6LR","type":"uap","state":1,"num_sta":12,"uptime":3600,"version":"6.6"},
				{"mac":"bb","name":"","type":"usw","state":0}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	r, err := Fetch(context.Background(), Config{Kind: "unifi", URL: srv.URL, Username: "u", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Devices) != 2 || r.Devices[0].Online || r.Devices[0].Name != "bb" || r.Devices[1].Clients != 12 || r.Devices[1].Type != "ap" {
		t.Errorf("%+v", r.Devices)
	}
}

func TestUniFiClassicAndKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/login":
			http.SetCookie(w, &http.Cookie{Name: "unifises", Value: "y", Path: "/"})
		case "/api/s/hq/stat/device":
			w.Write([]byte(`{"data":[{"mac":"aa","name":"GW","type":"ugw","state":1}]}`))
		case "/proxy/network/integration/v1/sites":
			if r.Header.Get("X-API-KEY") != "k" {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(`{"data":[{"id":"s1","name":"Default"},{"id":"s2","name":"Lager"}]}`))
		case "/proxy/network/integration/v1/sites/s2/devices":
			w.Write([]byte(`{"data":[{"id":"d","name":"Switch","macAddress":"AA:BB","ipAddress":"10.1.1.1","state":"ONLINE","features":["switching"]}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	r, err := Fetch(context.Background(), Config{Kind: "unifi", URL: srv.URL, Site: "hq", Username: "u"})
	if err != nil || len(r.Devices) != 1 || r.Devices[0].Type != "gateway" || !r.Devices[0].Online {
		t.Fatalf("%v %+v", err, r.Devices)
	}
	r, err = Fetch(context.Background(), Config{Kind: "unifi", URL: srv.URL, Site: "lager", APIKey: "k"})
	if err != nil || len(r.Devices) != 1 || r.Devices[0].ID != "aa:bb" || r.Devices[0].Type != "switch" || r.Devices[0].Site != "Lager" {
		t.Fatalf("%v %+v", err, r.Devices)
	}
	if _, err = Fetch(context.Background(), Config{Kind: "unifi", URL: srv.URL, APIKey: "fel"}); err != ErrAuth {
		t.Errorf("fel nyckel: %v", err)
	}
}

func TestMeraki(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/organizations":
			w.Write([]byte(`[{"id":"o1"}]`))
		case "/organizations/o1/networks":
			w.Write([]byte(`[{"id":"N1","name":"Kontoret"},{"id":"N2","name":"Lager"}]`))
		case "/organizations/o1/devices/statuses":
			if r.URL.Query().Get("networkIds[]") != "N2" {
				t.Errorf("filter: %s", r.URL.RawQuery)
			}
			json.NewEncoder(w).Encode([]map[string]string{{"name": "MR46", "serial": "Q2", "networkId": "N2", "status": "offline", "productType": "wireless", "lanIp": "10.0.0.5"},
				{"name": "MX", "serial": "Q3", "networkId": "N2", "status": "alerting", "productType": "appliance"}})
		}
	}))
	defer srv.Close()
	r, err := Fetch(context.Background(), Config{Kind: "meraki", URL: srv.URL, APIKey: "k", Site: "lager"})
	if err != nil || len(r.Devices) != 2 || r.Devices[0].Online || r.Devices[0].Type != "ap" || !r.Devices[1].Online || r.Devices[1].Site != "Lager" {
		t.Fatalf("%v %+v", err, r.Devices)
	}
}

func TestArubaRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/oauth2/token":
			if r.URL.Query().Get("refresh_token") != "r1" {
				w.WriteHeader(400)
				return
			}
			w.Write([]byte(`{"access_token":"a2","refresh_token":"r2"}`))
		case r.Header.Get("Authorization") != "Bearer a2":
			w.WriteHeader(401)
		case r.URL.Path == "/monitoring/v2/aps":
			w.Write([]byte(`{"aps":[{"serial":"S1","name":"AP-1","status":"Up","client_count":7,"site":"HQ"},{"serial":"S2","name":"AP-2","status":"Down"}]}`))
		case strings.HasPrefix(r.URL.Path, "/monitoring/v1/switches"):
			w.Write([]byte(`{"switches":[{"serial":"S3","name":"SW","status":"Up"}]}`))
		default:
			w.WriteHeader(403)
		}
	}))
	defer srv.Close()
	r, err := Fetch(context.Background(), Config{Kind: "aruba", URL: srv.URL, AccessToken: "old", RefreshToken: "r1", ClientID: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Tokens == nil || r.Tokens[0] != "a2" || r.Tokens[1] != "r2" {
		t.Errorf("token: %v", r.Tokens)
	}
	if len(r.Devices) != 3 || r.Devices[0].Name != "AP-2" || r.Devices[0].Online || r.Devices[1].Clients != 7 {
		t.Errorf("%+v", r.Devices)
	}
}
