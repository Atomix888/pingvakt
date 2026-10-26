package main

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const sessionCookie = "pv_session"
const sessionDays = 30

type ctxKey int

const userKey ctxKey = 1

func userFrom(r *http.Request) *User {
	u, _ := r.Context().Value(userKey).(*User)
	return u
}

func hashPassword(pw string) (string, error) {
	if len(pw) < 8 {
		return "", errors.New("lösenordet måste ha minst 8 tecken")
	}
	b, err := bcrypt.GenerateFromPassword([]byte(pw), 11)
	return string(b), err
}

// createSession skapar en session och sätter kakan.
func (s *Server) createSession(w http.ResponseWriter, r *http.Request, userID string) error {
	tok, h := newToken()
	exp := time.Now().Add(sessionDays * 24 * time.Hour)
	if _, err := s.db.Exec(`INSERT INTO sessions(token_hash,user_id,expires) VALUES(?,?,?)`, h, userID, exp.UnixMilli()); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", Expires: exp, HttpOnly: true,
		Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode})
	return nil
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// withUser lägger till inloggad användare i requesten om det finns en giltig session.
func (s *Server) withUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
			if u, err := s.db.UserBySession(hashToken(c.Value)); err == nil {
				r = r.WithContext(context.WithValue(r.Context(), userKey, &u))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// need kräver inloggning (och adminroll om admin = true). Ändrande anrop
// måste skickas som JSON, vilket stoppar CSRF från andra webbplatser.
func need(admin bool, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := userFrom(r)
		if u == nil {
			httpErr(w, http.StatusUnauthorized, "inte inloggad")
			return
		}
		if admin && u.Role != "admin" {
			httpErr(w, http.StatusForbidden, "kräver administratör")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				httpErr(w, http.StatusUnsupportedMediaType, "Content-Type måste vara application/json")
				return
			}
		}
		h(w, r)
	}
}

// canSee avgör om användaren får se kunden.
func canSee(u *User, customerID string) bool {
	return u != nil && (u.Role == "admin" || u.CustomerID == customerID)
}

// visibleCustomers: nil = alla (admin).
func visibleCustomers(u *User) map[string]bool {
	if u.Role == "admin" {
		return nil
	}
	return map[string]bool{u.CustomerID: true}
}

// ---------- inloggningsspärr ----------

type loginLimiter struct {
	mu    sync.Mutex
	fails map[string][]time.Time
}

func newLoginLimiter() *loginLimiter { return &loginLimiter{fails: map[string][]time.Time{}} }

func (l *loginLimiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := time.Now().Add(-15 * time.Minute)
	var keep []time.Time
	for _, t := range l.fails[ip] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	l.fails[ip] = keep
	return len(keep) >= 8
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	l.fails[ip] = append(l.fails[ip], time.Now())
	l.mu.Unlock()
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	delete(l.fails, ip)
	l.mu.Unlock()
}

func clientIP(r *http.Request) string {
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" && trustProxy {
		return strings.TrimSpace(strings.Split(xf, ",")[0])
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" && trustProxy {
		return xr
	}
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

var trustProxy = false

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("pingvakt-dummy"), 11)

// ---------- handlers ----------

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.limiter.blocked(ip) {
		httpErr(w, http.StatusTooManyRequests, "för många misslyckade försök – vänta 15 minuter")
		return
	}
	var in struct{ Email, Password string }
	if !readJSON(w, r, &in) {
		return
	}
	u, hash, err := s.db.UserByEmail(in.Email)
	if err == nil {
		err = bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password))
	} else {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(in.Password)) // samma tidsåtgång som för en riktig användare
	}
	if err != nil {
		s.limiter.fail(ip)
		httpErr(w, http.StatusUnauthorized, "fel e-post eller lösenord")
		return
	}
	s.limiter.reset(ip)
	if err := s.createSession(w, r, u.ID); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, u)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	if u == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = jsonEnc(w, map[string]any{"error": "inte inloggad", "setup": s.db.UserCount() == 0})
		return
	}
	writeJSON(w, u)
}

// handleSetup skapar den första administratören (bara när det inte finns några användare).
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		httpErr(w, http.StatusUnsupportedMediaType, "JSON krävs")
		return
	}
	var in struct{ Email, Name, Password string }
	if !readJSON(w, r, &in) {
		return
	}
	if s.db.UserCount() > 0 {
		httpErr(w, http.StatusForbidden, "servern är redan konfigurerad")
		return
	}
	u, err := s.createUser(in.Email, in.Name, "admin", "", in.Password)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if err := s.createSession(w, r, u.ID); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, u)
}

func (s *Server) createUser(email, name, role, customerID, pw string) (User, error) {
	email = strings.TrimSpace(email)
	if !strings.Contains(email, "@") {
		return User{}, errors.New("ange en giltig e-postadress")
	}
	if role != "admin" {
		role = "viewer"
		if customerID == "" {
			return User{}, errors.New("en kundanvändare måste kopplas till en kund")
		}
	} else {
		customerID = ""
	}
	h, err := hashPassword(pw)
	if err != nil {
		return User{}, err
	}
	u := User{ID: newID(), Email: email, Name: strings.TrimSpace(name), Role: role, CustomerID: customerID, Created: now()}
	_, err = s.db.Exec(`INSERT INTO users(id,email,name,pw_hash,role,customer_id,created) VALUES(?,?,?,?,?,?,?)`,
		u.ID, u.Email, u.Name, h, u.Role, u.CustomerID, u.Created)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return User{}, errors.New("e-postadressen används redan")
	}
	return u, err
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var in struct{ Current, New string }
	if !readJSON(w, r, &in) {
		return
	}
	var hash string
	if err := s.db.QueryRow(`SELECT pw_hash FROM users WHERE id=?`, u.ID).Scan(&hash); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Current)) != nil {
		httpErr(w, 400, "nuvarande lösenord stämmer inte")
		return
	}
	nh, err := hashPassword(in.New)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	_, _ = s.db.Exec(`UPDATE users SET pw_hash=? WHERE id=?`, nh, u.ID)
	writeJSON(w, map[string]bool{"ok": true})
}

var _ = sql.ErrNoRows
