package probe

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"time"
)

var httpTransport = &http.Transport{
	Proxy:                 nil, // mät vägen direkt, inte via proxy
	DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
	TLSHandshakeTimeout:   10 * time.Second,
	DisableKeepAlives:     true, // varje mätning ska räkna med uppkoppling och TLS
	MaxIdleConns:          0,
	ResponseHeaderTimeout: 30 * time.Second,
}

// NormalizeURL lägger till https:// om schemat saknas.
func NormalizeURL(u string) string {
	u = strings.TrimSpace(u)
	if u != "" && !strings.Contains(u, "://") {
		u = "https://" + u
	}
	return u
}

func httpCheck(ctx context.Context, req Request, res *Result) {
	raw := NormalizeURL(req.Host)
	pu, err := url.Parse(raw)
	if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
		res.Err = "ogiltig URL"
		return
	}
	cctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	trace := &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) {
		if a, ok := i.Conn.RemoteAddr().(*net.TCPAddr); ok {
			res.Addr = a.IP.String()
		}
	}}
	hr, err := http.NewRequestWithContext(httptrace.WithClientTrace(cctx, trace), http.MethodGet, raw, nil)
	if err != nil {
		res.Err = err.Error()
		return
	}
	hr.Header.Set("User-Agent", "PingVakt/1 (+övervakning)")
	client := &http.Client{Transport: httpTransport, Timeout: req.Timeout}
	start := time.Now()
	resp, err := client.Do(hr)
	if err != nil {
		var ce *tls.CertificateVerificationError
		switch {
		case errors.As(err, &ce):
			res.Err = "Certifikatfel: " + shortErr(ce.Err)
		case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
			res.Err = ErrTimeout.Error()
		default:
			res.Err = shortErr(err)
		}
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	rtt := time.Since(start)
	res.Status = resp.StatusCode
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		res.CertDays = int(time.Until(resp.TLS.PeerCertificates[0].NotAfter).Hours() / 24)
	}
	if resp.StatusCode >= 400 {
		res.Err = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return
	}
	if kw := strings.TrimSpace(req.Keyword); kw != "" && !bytes.Contains(body, []byte(kw)) {
		res.Err = "Texten \"" + kw + "\" saknas i svaret"
		return
	}
	res.RTT, res.OK = rtt, true
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func dnsCheck(ctx context.Context, req Request, res *Result) {
	name := strings.TrimSuffix(strings.TrimSpace(req.Host), ".")
	if name == "" {
		res.Err = "namn saknas"
		return
	}
	r := net.DefaultResolver
	if srv := strings.TrimSpace(req.DNSSrv); srv != "" {
		if _, _, err := net.SplitHostPort(srv); err != nil {
			srv = net.JoinHostPort(strings.Trim(srv, "[]"), "53")
		}
		r = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: req.Timeout}
			return d.DialContext(ctx, "udp", srv)
		}}
	}
	cctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	start := time.Now()
	ips, err := r.LookupIPAddr(cctx, name)
	rtt := time.Since(start)
	if err != nil {
		var de *net.DNSError
		switch {
		case errors.As(err, &de) && de.IsNotFound:
			res.Err = "Namnet finns inte (NXDOMAIN)"
		case errors.As(err, &de) && de.IsTimeout, isTimeout(err):
			res.Err = ErrTimeout.Error()
		default:
			res.Err = shortErr(err)
		}
		return
	}
	if len(ips) == 0 {
		res.Err = "inga adresser i svaret"
		return
	}
	res.Addr = ips[0].IP.String()
	res.RTT, res.OK = rtt, true
}
