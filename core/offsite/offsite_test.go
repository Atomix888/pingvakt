package offsite

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Exemplet "GET Bucket (List Objects)" ur AWS dokumentation för Signature Version 4.
func TestSigV4Vector(t *testing.T) {
	s := &s3Store{region: "us-east-1", ak: "AKIAIOSFODNN7EXAMPLE", sk: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	req, _ := http.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J", nil)
	s.sign(req, sha(nil), time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))
	want := "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7"
	if !strings.HasSuffix(req.Header.Get("Authorization"), "Signature="+want) {
		t.Fatalf("fel signatur: %s", req.Header.Get("Authorization"))
	}
}

func TestWebDAVKeep(t *testing.T) {
	var mu sync.Mutex
	files := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "jonas" || p != "hemligt" {
			w.WriteHeader(401)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/dav/")
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case "PUT":
			b, _ := io.ReadAll(r.Body)
			files[name] = len(b)
			w.WriteHeader(201)
		case "DELETE":
			delete(files, name)
			w.WriteHeader(204)
		case "PROPFIND":
			w.WriteHeader(207)
			out := `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"><d:response><d:href>/dav/</d:href></d:response>`
			for n, l := range files {
				out += `<d:response><d:href>/dav/` + n + `</d:href><d:propstat><d:prop><d:getcontentlength>` + string(rune('0'+l%10)) + `</d:getcontentlength></d:prop></d:propstat></d:response>`
			}
			w.Write([]byte(out + `</d:multistatus>`))
		}
	}))
	defer srv.Close()
	c := Config{Kind: "webdav", URL: srv.URL + "/dav", Username: "jonas", Password: "hemligt", Keep: 2}
	dir := t.TempDir()
	for _, n := range []string{"pingvakt-20261001-0315.db", "pingvakt-20261002-0315.db", "pingvakt-20261003-0315.db"} {
		p := filepath.Join(dir, n)
		os.WriteFile(p, []byte("data"), 0o600)
		if err := UploadAndPrune(context.Background(), c, p); err != nil {
			t.Fatal(err)
		}
	}
	files["annan-fil.txt"] = 1
	if len(files) != 3 || files["pingvakt-20261001-0315.db"] != 0 || files["pingvakt-20261003-0315.db"] != 4 {
		t.Errorf("%v", files)
	}
	if err := Test(context.Background(), c); err != nil {
		t.Error(err)
	}
	c.Password = "fel"
	if err := Test(context.Background(), c); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Errorf("fel lösenord: %v", err)
	}
}
