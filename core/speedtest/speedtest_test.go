package speedtest

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/down":
			n, _ := strconv.Atoi(r.URL.Query().Get("bytes"))
			buf := make([]byte, 64<<10)
			for n > 0 {
				k := len(buf)
				if n < k {
					k = n
				}
				if _, err := w.Write(buf[:k]); err != nil {
					return
				}
				n -= k
			}
		case "/up":
			_, _ = io.Copy(io.Discard, r.Body)
		}
	}))
	defer srv.Close()
	r := Run(context.Background(), Endpoints{Ping: srv.URL + "/down?bytes=0", Down: srv.URL + "/down?bytes=%d", Up: srv.URL + "/up"}, 1, "test")
	if r.Err != "" || r.Down <= 0 || r.Up <= 0 || r.Latency <= 0 {
		t.Fatalf("%+v", r)
	}
	t.Logf("ned %.0f Mbit/s, upp %.0f Mbit/s, svarstid %.2f ms", r.Down, r.Up, r.Latency)
}
