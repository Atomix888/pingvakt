// Package speedtest mäter bandbredd (ned- och uppladdning) och svarstid,
// antingen mot Cloudflares hastighetstest eller mot PingVakt-servern.
package speedtest

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Config styr när och hur agenten mäter.
type Config struct {
	IntervalMin int    `json:"intervalMin"` // 0 = av
	Target      string `json:"target"`      // "cloudflare" (standard) eller "server"
	Seconds     int    `json:"seconds"`     // mättid per riktning (standard 8)
}

// Result är en mätning.
type Result struct {
	T        int64   `json:"t"`
	Target   string  `json:"target"`
	Down     float64 `json:"down"`    // Mbit/s
	Up       float64 `json:"up"`      // Mbit/s
	Latency  float64 `json:"latency"` // ms (median)
	Jitter   float64 `json:"jitter"`  // ms
	BytesIn  int64   `json:"bytesIn"`
	BytesOut int64   `json:"bytesOut"`
	Err      string  `json:"err,omitempty"`
}

// Endpoints anger var mätningen görs. Header används för inloggning mot servern.
type Endpoints struct {
	Ping   string // liten GET
	Down   string // GET som ger många byte; "%d" ersätts med antal byte
	Up     string // POST
	Header http.Header
	Client *http.Client
}

// Cloudflare är Cloudflares öppna hastighetstest.
func Cloudflare() Endpoints {
	return Endpoints{Ping: "https://speed.cloudflare.com/__down?bytes=0", Down: "https://speed.cloudflare.com/__down?bytes=%d", Up: "https://speed.cloudflare.com/__up"}
}

// Run gör en mätning: svarstid, nedladdning och uppladdning.
func Run(ctx context.Context, ep Endpoints, seconds int, name string) Result {
	if seconds <= 0 {
		seconds = 8
	}
	c := ep.Client
	if c == nil {
		c = &http.Client{Timeout: time.Duration(seconds+20) * time.Second}
	}
	res := Result{T: time.Now().UnixMilli(), Target: name}
	req := func(ctx context.Context, method, u string, body io.Reader, n int64) (*http.Response, error) {
		r, err := http.NewRequestWithContext(ctx, method, u, body)
		if err != nil {
			return nil, err
		}
		for k, v := range ep.Header {
			r.Header[k] = v
		}
		if body != nil {
			r.ContentLength = n
			r.Header.Set("Content-Type", "application/octet-stream")
		}
		return c.Do(r)
	}
	// svarstid: 8 små anrop (första räknas inte – uppkoppling)
	var rtts []float64
	for i := 0; i < 9; i++ {
		t0 := time.Now()
		r, err := req(ctx, http.MethodGet, ep.Ping, nil, 0)
		if err != nil {
			res.Err = err.Error()
			return res
		}
		_, _ = io.Copy(io.Discard, r.Body)
		r.Body.Close()
		if i > 0 {
			rtts = append(rtts, float64(time.Since(t0).Microseconds())/1000)
		}
	}
	sort.Float64s(rtts)
	res.Latency = rtts[len(rtts)/2]
	var jit float64
	for i := 1; i < len(rtts); i++ {
		jit += rtts[i] - rtts[i-1]
	}
	res.Jitter = jit / float64(len(rtts)-1)

	dur := time.Duration(seconds) * time.Second
	const streams = 4
	// nedladdning
	{
		var total int64
		dctx, cancel := context.WithTimeout(ctx, dur)
		start := time.Now()
		var wg sync.WaitGroup
		for i := 0; i < streams; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				buf := make([]byte, 64<<10)
				for dctx.Err() == nil {
					r, err := req(dctx, http.MethodGet, fmt.Sprintf(ep.Down, 25_000_000), nil, 0)
					if err != nil {
						return
					}
					for {
						n, err := r.Body.Read(buf)
						atomic.AddInt64(&total, int64(n))
						if err != nil {
							break
						}
					}
					r.Body.Close()
				}
			}()
		}
		wg.Wait()
		cancel()
		el := time.Since(start).Seconds()
		res.BytesIn = total
		res.Down = float64(total) * 8 / el / 1e6
	}
	// uppladdning
	{
		var total int64
		uctx, cancel := context.WithTimeout(ctx, dur)
		start := time.Now()
		chunk := make([]byte, 4<<20)
		_, _ = rand.Read(chunk)
		var wg sync.WaitGroup
		for i := 0; i < streams; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for uctx.Err() == nil {
					cr := &countReader{data: chunk, n: &total}
					r, err := req(uctx, http.MethodPost, ep.Up, cr, int64(len(chunk)))
					if err != nil {
						return
					}
					_, _ = io.Copy(io.Discard, r.Body)
					r.Body.Close()
				}
			}()
		}
		wg.Wait()
		cancel()
		el := time.Since(start).Seconds()
		res.BytesOut = total
		res.Up = float64(total) * 8 / el / 1e6
	}
	if res.BytesIn == 0 && res.BytesOut == 0 {
		res.Err = errors.New("no data transferred").Error()
	}
	return res
}

type countReader struct {
	data []byte
	off  int
	n    *int64
}

func (c *countReader) Read(p []byte) (int, error) {
	if c.off >= len(c.data) {
		return 0, io.EOF
	}
	n := copy(p, c.data[c.off:])
	c.off += n
	atomic.AddInt64(c.n, int64(n))
	return n, nil
}

// Normalize sätter rimliga gränser.
func (c *Config) Normalize() {
	if c.Target != "server" {
		c.Target = "cloudflare"
	}
	if c.IntervalMin < 0 {
		c.IntervalMin = 0
	}
	if c.IntervalMin > 0 && c.IntervalMin < 15 {
		c.IntervalMin = 15
	}
	if c.IntervalMin > 7*24*60 {
		c.IntervalMin = 7 * 24 * 60
	}
	if c.Seconds <= 0 {
		c.Seconds = 8
	}
	if c.Seconds > 20 {
		c.Seconds = 20
	}
}
