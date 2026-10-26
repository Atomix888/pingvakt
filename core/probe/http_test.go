package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPCheck(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fel" {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte("<h1>Välkommen</h1>"))
	}))
	defer srv.Close()
	old := httpTransport.TLSClientConfig
	httpTransport.TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	defer func() { httpTransport.TLSClientConfig = old }()
	p := New()
	defer p.Close()
	do := func(u, kw string) Result {
		return p.Do(context.Background(), Request{Host: u, Kind: KindHTTP, Timeout: 5 * time.Second, Keyword: kw})
	}
	if r := do(srv.URL, "Välkommen"); !r.OK || r.Status != 200 || r.CertDays < 0 || r.Addr == "" {
		t.Fatalf("ok-fall: %+v", r)
	}
	if r := do(srv.URL, "Saknas"); r.OK || !strings.Contains(r.Err, "Missing text") {
		t.Fatalf("nyckelord: %+v", r)
	}
	if r := do(srv.URL+"/fel", ""); r.OK || r.Status != 503 {
		t.Fatalf("503: %+v", r)
	}
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer plain.Close()
	if r := do(plain.URL, ""); !r.OK || r.CertDays != -1 {
		t.Fatalf("http: %+v", r)
	}
}

func TestDNSCheck(t *testing.T) {
	p := New()
	defer p.Close()
	r := p.Do(context.Background(), Request{Host: "localhost", Kind: KindDNS, Timeout: 3 * time.Second})
	if !r.OK || r.Addr == "" {
		t.Fatalf("localhost: %+v", r)
	}
}
