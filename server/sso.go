package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"pingvakt/core/i18n"
)

// AuthSettings styr inloggningen.
type AuthSettings struct {
	RequireMFA string       `json:"requireMfa"` // "", "admins" eller "all"
	Microsoft  OIDCProvider `json:"microsoft"`
	Google     OIDCProvider `json:"google"`
}

// OIDCProvider är inloggning via Microsoft Entra ID eller Google.
type OIDCProvider struct {
	Enabled  bool   `json:"enabled"`
	ClientID string `json:"clientId"`
	Secret   string `json:"secret"`
	Tenant   string `json:"tenant,omitempty"` // Microsoft: katalog-ID, domän, "organizations" eller "common"
}

func (a AuthSettings) redact() AuthSettings {
	if a.Microsoft.Secret != "" {
		a.Microsoft.Secret = secretMask
	}
	if a.Google.Secret != "" {
		a.Google.Secret = secretMask
	}
	return a
}

func (a AuthSettings) merge(old AuthSettings) AuthSettings {
	if a.Microsoft.Secret == secretMask {
		a.Microsoft.Secret = old.Microsoft.Secret
	}
	if a.Google.Secret == secretMask {
		a.Google.Secret = old.Google.Secret
	}
	switch a.RequireMFA {
	case "admins", "all":
	default:
		a.RequireMFA = ""
	}
	a.Microsoft.Tenant = strings.TrimSpace(a.Microsoft.Tenant)
	if a.Microsoft.Tenant == "" {
		a.Microsoft.Tenant = "organizations"
	}
	a.Microsoft.ClientID = strings.TrimSpace(a.Microsoft.ClientID)
	a.Google.ClientID = strings.TrimSpace(a.Google.ClientID)
	return a
}

// mfaRequired anger om användaren måste ha tvåstegsinloggning.
func (s *Server) mfaRequired(role string) bool {
	s.mu.RLock()
	p := s.settings.Auth.RequireMFA
	s.mu.RUnlock()
	return p == "all" || (p == "admins" && role == "admin")
}

type oidcEndpoints struct {
	auth, token string
	issuer      func(iss string) bool
}

func (s *Server) oidcConfig(name string) (OIDCProvider, oidcEndpoints, bool) {
	s.mu.RLock()
	a := s.settings.Auth
	s.mu.RUnlock()
	switch name {
	case "microsoft":
		t := url.PathEscape(a.Microsoft.Tenant)
		if t == "" {
			t = "organizations"
		}
		return a.Microsoft, oidcEndpoints{
			auth:  "https://login.microsoftonline.com/" + t + "/oauth2/v2.0/authorize",
			token: "https://login.microsoftonline.com/" + t + "/oauth2/v2.0/token",
			issuer: func(iss string) bool {
				return strings.HasPrefix(iss, "https://login.microsoftonline.com/") && strings.HasSuffix(iss, "/v2.0")
			},
		}, a.Microsoft.Enabled && a.Microsoft.ClientID != "" && a.Microsoft.Secret != ""
	case "google":
		return a.Google, oidcEndpoints{
			auth:   "https://accounts.google.com/o/oauth2/v2/auth",
			token:  "https://oauth2.googleapis.com/token",
			issuer: func(iss string) bool { return iss == "https://accounts.google.com" || iss == "accounts.google.com" },
		}, a.Google.Enabled && a.Google.ClientID != "" && a.Google.Secret != ""
	}
	return OIDCProvider{}, oidcEndpoints{}, false
}

type oidcFlow struct {
	provider, verifier, nonce string
	exp                       time.Time
}

var (
	oidcMu    sync.Mutex
	oidcFlows = map[string]oidcFlow{}
)

func randB64(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) redirectURI(r *http.Request, name string) string {
	return s.publicURL(r) + "/auth/" + name + "/callback"
}

// handleAuthProviders talar om vilka inloggningssätt som finns (visas på inloggningssidan).
func (s *Server) handleAuthProviders(w http.ResponseWriter, r *http.Request) {
	_, _, ms := s.oidcConfig("microsoft")
	_, _, g := s.oidcConfig("google")
	writeJSON(w, map[string]bool{"microsoft": ms, "google": g})
}

// handleOIDCStart skickar användaren till Microsoft/Google.
func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("provider")
	p, ep, ok := s.oidcConfig(name)
	if !ok {
		http.Redirect(w, r, "/#/login?sso=off", http.StatusFound)
		return
	}
	state, verifier, nonce := randB64(24), randB64(48), randB64(18)
	oidcMu.Lock()
	for k, f := range oidcFlows {
		if time.Now().After(f.exp) {
			delete(oidcFlows, k)
		}
	}
	oidcFlows[state] = oidcFlow{provider: name, verifier: verifier, nonce: nonce, exp: time.Now().Add(10 * time.Minute)}
	oidcMu.Unlock()
	ch := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"client_id": {p.ClientID}, "response_type": {"code"}, "redirect_uri": {s.redirectURI(r, name)},
		"scope": {"openid email profile"}, "state": {state}, "nonce": {nonce},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(ch[:])}, "code_challenge_method": {"S256"},
		"prompt": {"select_account"},
	}
	http.Redirect(w, r, ep.auth+"?"+q.Encode(), http.StatusFound)
}

// handleOIDCCallback tar emot svaret, kontrollerar ID-token och loggar in användaren
// med samma e-postadress. ID-token hämtas direkt från leverantörens token-endpoint
// över TLS, så signaturen behöver inte kontrolleras separat (OpenID Connect Core 3.1.3.7).
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("provider")
	fail := func(msg string) {
		log.Printf("sso %s: %s", name, msg)
		http.Redirect(w, r, "/#/login?ssoerr="+url.QueryEscape(msg), http.StatusFound)
	}
	if e := r.URL.Query().Get("error"); e != "" {
		fail(e + ": " + r.URL.Query().Get("error_description"))
		return
	}
	state := r.URL.Query().Get("state")
	oidcMu.Lock()
	f, ok := oidcFlows[state]
	delete(oidcFlows, state)
	oidcMu.Unlock()
	if !ok || f.provider != name || time.Now().After(f.exp) {
		fail("the sign-in has expired – start over")
		return
	}
	p, ep, enabled := s.oidcConfig(name)
	if !enabled {
		fail("sign-in with this provider is turned off")
		return
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {r.URL.Query().Get("code")}, "redirect_uri": {s.redirectURI(r, name)},
		"client_id": {p.ClientID}, "client_secret": {p.Secret}, "code_verifier": {f.verifier}}
	res, err := (&http.Client{Timeout: 20 * time.Second}).PostForm(ep.token, form)
	if err != nil {
		fail(err.Error())
		return
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	var tok struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
		Desc    string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &tok)
	if res.StatusCode != 200 || tok.IDToken == "" {
		fail(strings.TrimSpace(tok.Error + " " + tok.Desc))
		return
	}
	claims, err := parseIDToken(tok.IDToken)
	if err != nil {
		fail(err.Error())
		return
	}
	if !ep.issuer(claims.Iss) || !claims.hasAud(p.ClientID) || claims.Nonce != f.nonce || time.Now().Unix() > claims.Exp+60 {
		fail("invalid ID token")
		return
	}
	email := claims.Email
	if email == "" && name == "microsoft" {
		email = claims.PreferredUsername
	}
	if name == "google" && !claims.EmailVerified {
		fail("the Google account's email address is not verified")
		return
	}
	email = strings.ToLower(strings.TrimSpace(email))
	var userID string
	if err := s.db.QueryRow(`SELECT id FROM users WHERE lower(email)=?`, email).Scan(&userID); err != nil || email == "" {
		s.auditAs(email, clientIP(r), "sign-in", "", i18n.T("failed sign-in")+" ("+name+")")
		fail("no PingVakt user with the email address " + email)
		return
	}
	if err := s.createSession(w, r, userID, true); err != nil {
		fail(err.Error())
		return
	}
	s.auditAs(email, clientIP(r), "sign-in", userID, i18n.T("signed in")+" ("+name+")")
	http.Redirect(w, r, "/", http.StatusFound)
}

type idClaims struct {
	Iss               string          `json:"iss"`
	Aud               json.RawMessage `json:"aud"`
	Exp               int64           `json:"exp"`
	Nonce             string          `json:"nonce"`
	Email             string          `json:"email"`
	EmailVerified     bool            `json:"email_verified"`
	PreferredUsername string          `json:"preferred_username"`
}

func (c idClaims) hasAud(id string) bool {
	var one string
	if json.Unmarshal(c.Aud, &one) == nil {
		return one == id
	}
	var many []string
	if json.Unmarshal(c.Aud, &many) == nil {
		for _, a := range many {
			if a == id {
				return true
			}
		}
	}
	return false
}

func parseIDToken(t string) (idClaims, error) {
	var c idClaims
	parts := strings.Split(t, ".")
	if len(parts) != 3 {
		return c, errors.New("invalid ID token")
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return c, errors.New("invalid ID token")
	}
	// email_verified kan vara sträng hos vissa leverantörer
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	if v, ok := raw["email_verified"].(string); ok {
		raw["email_verified"] = v == "true"
		b, _ = json.Marshal(raw)
	}
	err = json.Unmarshal(b, &c)
	return c, err
}
