// Package vendors hämtar enhetsstatus från tillverkarnas molntjänster och kontrollrar:
// Ubiquiti UniFi, Cisco Meraki och HPE Aruba Central.
package vendors

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Config är en koppling till en tillverkares system.
type Config struct {
	Kind     string `json:"kind"` // unifi, meraki, aruba
	URL      string `json:"url,omitempty"`
	Site     string `json:"site,omitempty"` // UniFi-plats, Meraki-nätverk eller Aruba-plats (tomt = alla/standard)
	APIKey   string `json:"apiKey,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	OrgID    string `json:"orgId,omitempty"`
	Insecure bool   `json:"insecure,omitempty"` // godta självsignerat certifikat (UniFi-kontroller i nätet)
	// Aruba Central (OAuth): åtkomst- och förnyelsetoken roteras och sparas tillbaka
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
	AccessToken  string `json:"accessToken,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
}

// Device är en enhet hos tillverkaren.
type Device struct {
	ID       string `json:"id"` // MAC eller serienummer
	Name     string `json:"name"`
	Model    string `json:"model,omitempty"`
	Type     string `json:"type,omitempty"` // ap, switch, gateway, other
	MAC      string `json:"mac,omitempty"`
	IP       string `json:"ip,omitempty"`
	Online   bool   `json:"online"`
	Status   string `json:"status,omitempty"` // tillverkarens egen status
	Clients  int    `json:"clients,omitempty"`
	Uptime   int64  `json:"uptime,omitempty"` // sekunder
	Firmware string `json:"firmware,omitempty"`
	Site     string `json:"site,omitempty"`
}

// Result är en avläsning.
type Result struct {
	Devices []Device
	// Tokens är satt när Aruba Central gav nya token som måste sparas.
	Tokens *[2]string
}

var ErrAuth = errors.New("sign-in failed – check the key or username and password")

// Fetch hämtar enheterna.
func Fetch(ctx context.Context, c Config) (Result, error) {
	var r Result
	var err error
	switch c.Kind {
	case "unifi":
		r.Devices, err = fetchUniFi(ctx, c)
	case "meraki":
		r.Devices, err = fetchMeraki(ctx, c)
	case "aruba":
		r, err = fetchAruba(ctx, c)
	default:
		return r, errors.New("unknown integration: " + c.Kind)
	}
	sort.Slice(r.Devices, func(i, j int) bool {
		if r.Devices[i].Online != r.Devices[j].Online {
			return !r.Devices[i].Online
		}
		return strings.ToLower(r.Devices[i].Name) < strings.ToLower(r.Devices[j].Name)
	})
	return r, err
}

func client(insecure bool) *http.Client {
	jar, _ := cookiejar.New(nil)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // användaren har valt det för en kontroller med eget certifikat
	}
	return &http.Client{Timeout: 30 * time.Second, Jar: jar, Transport: tr}
}

// call gör en begäran och avkodar JSON-svaret till out.
func call(ctx context.Context, cl *http.Client, method, u string, hdr map[string]string, body any, out any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return resp, ErrAuth
	}
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return resp, fmt.Errorf("HTTP %d: %s: %s", resp.StatusCode, req.URL.Host, msg)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp, errors.New("unexpected response from " + req.URL.Host)
		}
	}
	return resp, nil
}

func base(u string) (string, error) {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	if u == "" {
		return "", errors.New("enter the address")
	}
	if !strings.Contains(u, "://") {
		u = "https://" + u
	}
	p, err := url.Parse(u)
	if err != nil || p.Host == "" {
		return "", errors.New("invalid address")
	}
	return u, nil
}

// ---------- UniFi ----------

func uniType(t string) string {
	switch t {
	case "uap":
		return "ap"
	case "usw":
		return "switch"
	case "ugw", "udm", "uxg":
		return "gateway"
	}
	return "other"
}

func fetchUniFi(ctx context.Context, c Config) ([]Device, error) {
	b, err := base(c.URL)
	if err != nil {
		return nil, err
	}
	cl := client(c.Insecure)
	site := c.Site
	if site == "" {
		site = "default"
	}
	if c.APIKey != "" {
		return fetchUniFiIntegration(ctx, cl, b, c)
	}
	if c.Username == "" {
		return nil, errors.New("enter an API key or username and password")
	}
	creds := map[string]any{"username": c.Username, "password": c.Password, "remember": false}
	hdr := map[string]string{}
	prefix := "/proxy/network"
	resp, err := call(ctx, cl, "POST", b+"/api/auth/login", nil, creds, nil)
	if err != nil && (resp == nil || resp.StatusCode == 404) {
		// äldre kontroller (inte UniFi OS)
		prefix = ""
		if _, err = call(ctx, cl, "POST", b+"/api/login", nil, creds, nil); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else if t := resp.Header.Get("X-Csrf-Token"); t != "" {
		hdr["X-Csrf-Token"] = t
	}
	var res struct {
		Data []struct {
			MAC     string `json:"mac"`
			IP      string `json:"ip"`
			Name    string `json:"name"`
			Model   string `json:"model"`
			Type    string `json:"type"`
			State   int    `json:"state"`
			NumSta  int    `json:"num_sta"`
			Uptime  int64  `json:"uptime"`
			Version string `json:"version"`
		} `json:"data"`
	}
	if _, err := call(ctx, cl, "GET", b+prefix+"/api/s/"+url.PathEscape(site)+"/stat/device", hdr, nil, &res); err != nil {
		return nil, err
	}
	var out []Device
	for _, d := range res.Data {
		name := d.Name
		if name == "" {
			name = d.MAC
		}
		st := map[int]string{0: "offline", 1: "online", 2: "pending adoption", 4: "upgrading", 5: "provisioning", 6: "heartbeat missed"}[d.State]
		out = append(out, Device{ID: d.MAC, Name: name, Model: d.Model, Type: uniType(d.Type), MAC: d.MAC, IP: d.IP, Online: d.State == 1 || d.State == 4 || d.State == 5,
			Status: st, Clients: d.NumSta, Uptime: d.Uptime, Firmware: d.Version, Site: site})
	}
	return out, nil
}

// fetchUniFiIntegration använder UniFi Network-programmets officiella API (API-nyckel, version 9+).
func fetchUniFiIntegration(ctx context.Context, cl *http.Client, b string, c Config) ([]Device, error) {
	hdr := map[string]string{"X-API-KEY": c.APIKey}
	api := b + "/proxy/network/integration/v1"
	var sites struct {
		Data []struct {
			ID                string `json:"id"`
			Name              string `json:"name"`
			InternalReference string `json:"internalReference"`
		} `json:"data"`
	}
	if _, err := call(ctx, cl, "GET", api+"/sites?limit=200", hdr, nil, &sites); err != nil {
		return nil, err
	}
	var out []Device
	for _, s := range sites.Data {
		if c.Site != "" && !strings.EqualFold(c.Site, s.Name) && !strings.EqualFold(c.Site, s.InternalReference) && c.Site != s.ID {
			continue
		}
		var devs struct {
			Data []struct {
				ID       string   `json:"id"`
				Name     string   `json:"name"`
				Model    string   `json:"model"`
				MAC      string   `json:"macAddress"`
				IP       string   `json:"ipAddress"`
				State    string   `json:"state"`
				Firmware string   `json:"firmwareVersion"`
				Features []string `json:"features"`
			} `json:"data"`
		}
		if _, err := call(ctx, cl, "GET", api+"/sites/"+url.PathEscape(s.ID)+"/devices?limit=500", hdr, nil, &devs); err != nil {
			return nil, err
		}
		for _, d := range devs.Data {
			typ := "other"
			for _, f := range d.Features {
				switch f {
				case "accessPoint":
					typ = "ap"
				case "switching":
					if typ == "other" {
						typ = "switch"
					}
				case "gateway":
					typ = "gateway"
				}
			}
			name := d.Name
			if name == "" {
				name = d.MAC
			}
			out = append(out, Device{ID: strings.ToLower(d.MAC), Name: name, Model: d.Model, Type: typ, MAC: strings.ToLower(d.MAC), IP: d.IP,
				Online: strings.EqualFold(d.State, "ONLINE"), Status: strings.ToLower(d.State), Firmware: d.Firmware, Site: s.Name})
		}
	}
	return out, nil
}

// ---------- Meraki ----------

var merakiBase = "https://api.meraki.com/api/v1"

func fetchMeraki(ctx context.Context, c Config) ([]Device, error) {
	if c.APIKey == "" {
		return nil, errors.New("enter the Meraki API key")
	}
	b := merakiBase
	if c.URL != "" {
		if u, err := base(c.URL); err == nil {
			b = u // t.ex. Meraki i Kina eller en testserver
		}
	}
	cl := client(false)
	hdr := map[string]string{"Authorization": "Bearer " + c.APIKey}
	org := c.OrgID
	if org == "" {
		var orgs []struct {
			ID string `json:"id"`
		}
		if _, err := call(ctx, cl, "GET", b+"/organizations", hdr, nil, &orgs); err != nil {
			return nil, err
		}
		if len(orgs) == 0 {
			return nil, errors.New("the API key has no organisation")
		}
		org = orgs[0].ID
	}
	var nets []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if _, err := call(ctx, cl, "GET", b+"/organizations/"+url.PathEscape(org)+"/networks?perPage=1000", hdr, nil, &nets); err != nil {
		return nil, err
	}
	netName := map[string]string{}
	for _, n := range nets {
		netName[n.ID] = n.Name
	}
	q := "/organizations/" + url.PathEscape(org) + "/devices/statuses?perPage=1000"
	if c.Site != "" {
		for _, n := range nets {
			if strings.EqualFold(n.Name, c.Site) || n.ID == c.Site {
				q += "&networkIds[]=" + url.QueryEscape(n.ID)
			}
		}
		if !strings.Contains(q, "networkIds") {
			return nil, errors.New("found no network named " + c.Site)
		}
	}
	var st []struct {
		Name        string `json:"name"`
		Serial      string `json:"serial"`
		MAC         string `json:"mac"`
		LanIP       string `json:"lanIp"`
		PublicIP    string `json:"publicIp"`
		NetworkID   string `json:"networkId"`
		Status      string `json:"status"`
		Model       string `json:"model"`
		ProductType string `json:"productType"`
	}
	if _, err := call(ctx, cl, "GET", b+q, hdr, nil, &st); err != nil {
		return nil, err
	}
	var out []Device
	for _, d := range st {
		typ := map[string]string{"wireless": "ap", "switch": "switch", "appliance": "gateway", "cellularGateway": "gateway"}[d.ProductType]
		if typ == "" {
			typ = "other"
		}
		ip := d.LanIP
		if ip == "" {
			ip = d.PublicIP
		}
		name := d.Name
		if name == "" {
			name = d.Serial
		}
		out = append(out, Device{ID: d.Serial, Name: name, Model: d.Model, Type: typ, MAC: d.MAC, IP: ip,
			Online: d.Status == "online" || d.Status == "alerting", Status: d.Status, Site: netName[d.NetworkID]})
	}
	return out, nil
}

// ---------- Aruba Central ----------

func fetchAruba(ctx context.Context, c Config) (Result, error) {
	var r Result
	b, err := base(c.URL)
	if err != nil {
		return r, err
	}
	cl := client(false)
	tok := c.AccessToken
	get := func(path string, out any) error {
		_, err := call(ctx, cl, "GET", b+path, map[string]string{"Authorization": "Bearer " + tok}, nil, out)
		return err
	}
	refresh := func() error {
		if c.RefreshToken == "" || c.ClientID == "" {
			return ErrAuth
		}
		q := url.Values{"client_id": {c.ClientID}, "client_secret": {c.ClientSecret}, "grant_type": {"refresh_token"}, "refresh_token": {c.RefreshToken}}
		var t struct {
			Access  string `json:"access_token"`
			Refresh string `json:"refresh_token"`
		}
		if _, err := call(ctx, cl, "POST", b+"/oauth2/token?"+q.Encode(), nil, nil, &t); err != nil {
			return err
		}
		if t.Access == "" {
			return ErrAuth
		}
		tok = t.Access
		c.RefreshToken = t.Refresh
		r.Tokens = &[2]string{t.Access, t.Refresh}
		return nil
	}
	if tok == "" {
		if err := refresh(); err != nil {
			return r, err
		}
	}
	site := ""
	if c.Site != "" {
		site = "&site=" + url.QueryEscape(c.Site)
	}
	type dev struct {
		Serial   string `json:"serial"`
		Name     string `json:"name"`
		MAC      string `json:"macaddr"`
		IP       string `json:"ip_address"`
		Status   string `json:"status"`
		Model    string `json:"model"`
		Clients  int    `json:"client_count"`
		Firmware string `json:"firmware_version"`
		Site     string `json:"site"`
		Uptime   int64  `json:"uptime"`
	}
	var aps struct {
		APs []dev `json:"aps"`
	}
	err = get("/monitoring/v2/aps?limit=1000"+site, &aps)
	if errors.Is(err, ErrAuth) {
		if err = refresh(); err == nil {
			err = get("/monitoring/v2/aps?limit=1000"+site, &aps)
		}
	}
	if err != nil {
		return r, err
	}
	var sw struct {
		Switches []dev `json:"switches"`
	}
	var gw struct {
		Gateways []dev `json:"gateways"`
	}
	_ = get("/monitoring/v1/switches?limit=1000"+site, &sw) // kräver kanske andra rättigheter – hoppa över vid fel
	_ = get("/monitoring/v1/gateways?limit=1000"+site, &gw)
	add := func(list []dev, typ string) {
		for _, d := range list {
			name := d.Name
			if name == "" {
				name = d.Serial
			}
			r.Devices = append(r.Devices, Device{ID: d.Serial, Name: name, Model: d.Model, Type: typ, MAC: d.MAC, IP: d.IP,
				Online: strings.EqualFold(d.Status, "up"), Status: strings.ToLower(d.Status), Clients: d.Clients, Uptime: d.Uptime, Firmware: d.Firmware, Site: d.Site})
		}
	}
	add(aps.APs, "ap")
	add(sw.Switches, "switch")
	add(gw.Gateways, "gateway")
	return r, nil
}
