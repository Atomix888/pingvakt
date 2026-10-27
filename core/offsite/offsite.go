// Package offsite laddar upp säkerhetskopior till en lagringstjänst utanför servern:
// S3-kompatibel lagring (AWS S3, Backblaze B2, Wasabi, MinIO …) eller WebDAV (Nextcloud, ownCloud).
package offsite

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

// Config beskriver lagringen.
type Config struct {
	Kind      string `json:"kind"` // "", s3, webdav
	Endpoint  string `json:"endpoint,omitempty"`
	Region    string `json:"region,omitempty"`
	Bucket    string `json:"bucket,omitempty"`
	AccessKey string `json:"accessKey,omitempty"`
	SecretKey string `json:"secretKey,omitempty"`
	Prefix    string `json:"prefix,omitempty"` // mapp/prefix, t.ex. pingvakt/
	URL       string `json:"url,omitempty"`    // WebDAV-mapp
	Username  string `json:"username,omitempty"`
	Password  string `json:"password,omitempty"`
	Keep      int    `json:"keep,omitempty"` // antal kopior att behålla (0 = alla)
}

// Enabled säger om uppladdning är påslagen.
func (c Config) Enabled() bool { return c.Kind == "s3" || c.Kind == "webdav" }

// Object är en fil i lagringen.
type Object struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// Store är en lagringstjänst.
type Store interface {
	Put(ctx context.Context, name string, f *os.File, size int64) error
	List(ctx context.Context) ([]Object, error)
	Delete(ctx context.Context, name string) error
}

var client = &http.Client{Timeout: 30 * time.Minute}

// Open skapar en Store för konfigurationen.
func Open(c Config) (Store, error) {
	switch c.Kind {
	case "s3":
		if c.Endpoint == "" || c.Bucket == "" || c.AccessKey == "" || c.SecretKey == "" {
			return nil, errors.New("fill in endpoint, bucket, access key and secret key")
		}
		ep := strings.TrimRight(c.Endpoint, "/")
		if !strings.Contains(ep, "://") {
			ep = "https://" + ep
		}
		u, err := url.Parse(ep)
		if err != nil || u.Host == "" {
			return nil, errors.New("invalid endpoint")
		}
		region := c.Region
		if region == "" {
			region = "us-east-1"
		}
		return &s3Store{base: u, region: region, bucket: c.Bucket, ak: c.AccessKey, sk: c.SecretKey, prefix: cleanPrefix(c.Prefix)}, nil
	case "webdav":
		if c.URL == "" {
			return nil, errors.New("enter the WebDAV address")
		}
		u := strings.TrimRight(c.URL, "/") + "/"
		if !strings.Contains(u, "://") {
			u = "https://" + u
		}
		return &davStore{base: u, user: c.Username, pass: c.Password}, nil
	}
	return nil, errors.New("off-site backup is not configured")
}

func cleanPrefix(p string) string {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return ""
	}
	return p + "/"
}

// UploadAndPrune laddar upp filen och tar bort de äldsta kopiorna så att högst keep finns kvar.
// Bara filer vars namn börjar med "pingvakt-" och slutar på ".db" räknas.
func UploadAndPrune(ctx context.Context, c Config, file string) error {
	st, err := Open(c)
	if err != nil {
		return err
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if err := st.Put(ctx, path.Base(file), f, fi.Size()); err != nil {
		return err
	}
	if c.Keep <= 0 {
		return nil
	}
	list, err := st.List(ctx)
	if err != nil {
		return fmt.Errorf("uploaded, but could not list old copies: %w", err)
	}
	var ours []string
	for _, o := range list {
		if strings.HasPrefix(o.Name, "pingvakt-") && strings.HasSuffix(o.Name, ".db") {
			ours = append(ours, o.Name)
		}
	}
	sort.Strings(ours)
	for len(ours) > c.Keep {
		if err := st.Delete(ctx, ours[0]); err != nil {
			return fmt.Errorf("uploaded, but could not delete %s: %w", ours[0], err)
		}
		ours = ours[1:]
	}
	return nil
}

// Test laddar upp och tar bort en liten fil.
func Test(ctx context.Context, c Config) error {
	st, err := Open(c)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "pingvakt-test-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	_, _ = tmp.WriteString("PingVakt test " + time.Now().Format(time.RFC3339) + "\n")
	_, _ = tmp.Seek(0, 0)
	fi, _ := tmp.Stat()
	name := "pingvakt-test.txt"
	if err := st.Put(ctx, name, tmp, fi.Size()); err != nil {
		return err
	}
	if _, err := st.List(ctx); err != nil {
		return err
	}
	return st.Delete(ctx, name)
}

func httpErr(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 600))
	msg := strings.TrimSpace(string(b))
	var e struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if xml.Unmarshal(b, &e) == nil && e.Code != "" {
		msg = e.Code + ": " + e.Message
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fmt.Errorf("access denied (HTTP %d) %s", resp.StatusCode, msg)
	}
	return fmt.Errorf("HTTP %d %s", resp.StatusCode, msg)
}

// ---------- S3 (Signature V4) ----------

type s3Store struct {
	base           *url.URL
	region, bucket string
	ak, sk, prefix string
}

func (s *s3Store) objURL(key string, q url.Values) *url.URL {
	u := *s.base
	u.Path = strings.TrimRight(u.Path, "/") + "/" + s.bucket + "/" + key // path-style fungerar hos alla leverantörer
	if q != nil {
		u.RawQuery = q.Encode()
	}
	return &u
}

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func hmacSHA(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

// uriEncode kodar enligt AWS regler (allt utom A-Z a-z 0-9 - _ . ~, och / i sökvägar).
func uriEncode(s string, slash bool) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && slash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func (s *s3Store) sign(req *http.Request, payloadHash string, t time.Time) {
	amz := t.UTC().Format("20060102T150405Z")
	day := amz[:8]
	req.Header.Set("x-amz-date", amz)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	host := req.URL.Host
	// kanonisk begäran
	keys := make([]string, 0)
	q := req.URL.Query()
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var qs []string
	for _, k := range keys {
		for _, v := range q[k] {
			qs = append(qs, uriEncode(k, false)+"="+uriEncode(v, false))
		}
	}
	signed := "host;x-amz-content-sha256;x-amz-date"
	canon := strings.Join([]string{req.Method, uriEncode(req.URL.Path, true), strings.Join(qs, "&"),
		"host:" + host + "\nx-amz-content-sha256:" + payloadHash + "\nx-amz-date:" + amz + "\n", signed, payloadHash}, "\n")
	scope := day + "/" + s.region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amz + "\n" + scope + "\n" + sha([]byte(canon))
	k := hmacSHA([]byte("AWS4"+s.sk), day)
	k = hmacSHA(k, s.region)
	k = hmacSHA(k, "s3")
	k = hmacSHA(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA(k, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.ak+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

func (s *s3Store) do(ctx context.Context, method string, u *url.URL, body io.Reader, size int64, payloadHash string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
	}
	s.sign(req, payloadHash, time.Now())
	return client.Do(req)
}

func (s *s3Store) Put(ctx context.Context, name string, f *os.File, size int64) error {
	// räkna hash först (filen läses två gånger, men inget behöver ligga i minnet)
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	resp, err := s.do(ctx, "PUT", s.objURL(s.prefix+name, nil), f, size, hex.EncodeToString(h.Sum(nil)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return httpErr(resp)
	}
	return nil
}

func (s *s3Store) List(ctx context.Context) ([]Object, error) {
	var out []Object
	token := ""
	for i := 0; i < 50; i++ {
		q := url.Values{"list-type": {"2"}, "prefix": {s.prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		u := s.objURL("", q)
		u.Path = strings.TrimSuffix(u.Path, "/")
		resp, err := s.do(ctx, "GET", u, nil, 0, sha(nil))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 300 {
			err := httpErr(resp)
			resp.Body.Close()
			return nil, err
		}
		var res struct {
			Contents []struct {
				Key  string `xml:"Key"`
				Size int64  `xml:"Size"`
			} `xml:"Contents"`
			Truncated bool   `xml:"IsTruncated"`
			Next      string `xml:"NextContinuationToken"`
		}
		err = xml.NewDecoder(resp.Body).Decode(&res)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("unexpected response from the storage: %w", err)
		}
		for _, c := range res.Contents {
			name := strings.TrimPrefix(c.Key, s.prefix)
			if name != "" && !strings.Contains(name, "/") {
				out = append(out, Object{Name: name, Size: c.Size})
			}
		}
		if !res.Truncated || res.Next == "" {
			break
		}
		token = res.Next
	}
	return out, nil
}

func (s *s3Store) Delete(ctx context.Context, name string) error {
	resp, err := s.do(ctx, "DELETE", s.objURL(s.prefix+name, nil), nil, 0, sha(nil))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != 404 {
		return httpErr(resp)
	}
	return nil
}

// ---------- WebDAV ----------

type davStore struct {
	base, user, pass string
}

func (d *davStore) req(ctx context.Context, method, name string, body io.Reader, size int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, d.base+url.PathEscape(name), body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
	}
	if d.user != "" {
		req.SetBasicAuth(d.user, d.pass)
	}
	if method == "PROPFIND" {
		req.Header.Set("Depth", "1")
		req.Header.Set("Content-Type", "application/xml")
	}
	return client.Do(req)
}

func (d *davStore) Put(ctx context.Context, name string, f *os.File, size int64) error {
	resp, err := d.req(ctx, "PUT", name, f, size)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 || resp.StatusCode == 409 {
		return errors.New("the folder does not exist – create it first")
	}
	if resp.StatusCode >= 300 {
		return httpErr(resp)
	}
	return nil
}

func (d *davStore) List(ctx context.Context) ([]Object, error) {
	resp, err := d.req(ctx, "PROPFIND", "", strings.NewReader(`<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:getcontentlength/></d:prop></d:propfind>`), -1)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 207 {
		return nil, httpErr(resp)
	}
	var ms struct {
		Resp []struct {
			Href string `xml:"href"`
			Len  int64  `xml:"propstat>prop>getcontentlength"`
		} `xml:"response"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return nil, fmt.Errorf("unexpected response from WebDAV: %w", err)
	}
	var out []Object
	for _, r := range ms.Resp {
		h, _ := url.PathUnescape(r.Href)
		if strings.HasSuffix(h, "/") {
			continue
		}
		out = append(out, Object{Name: path.Base(h), Size: r.Len})
	}
	return out, nil
}

func (d *davStore) Delete(ctx context.Context, name string) error {
	resp, err := d.req(ctx, "DELETE", name, nil, 0)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != 404 {
		return httpErr(resp)
	}
	return nil
}
