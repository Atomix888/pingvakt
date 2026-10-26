package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"

	"pingvakt/core/agentproto"
	"pingvakt/core/alert"
	"pingvakt/core/monitor"
)

// DB omsluter SQLite-databasen.
type DB struct {
	*sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS customers (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, notes TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sites (
  id TEXT PRIMARY KEY, customer_id TEXT NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
  name TEXT NOT NULL, created INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS agents (
  id TEXT PRIMARY KEY, site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
  name TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, kind TEXT NOT NULL DEFAULT '',
  hostname TEXT NOT NULL DEFAULT '', os TEXT NOT NULL DEFAULT '', version TEXT NOT NULL DEFAULT '',
  ip TEXT NOT NULL DEFAULT '', last_seen INTEGER NOT NULL DEFAULT 0, config_version INTEGER NOT NULL DEFAULT 1,
  local_hash TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS targets (
  id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  source TEXT NOT NULL DEFAULT 'server', sort INTEGER NOT NULL DEFAULT 0, data TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS targets_agent ON targets(agent_id);
CREATE TABLE IF NOT EXISTS samples (
  target_id TEXT NOT NULL, t INTEGER NOT NULL, rtt REAL NOT NULL, PRIMARY KEY (target_id, t)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS agg_1m (
  target_id TEXT NOT NULL, t INTEGER NOT NULL, n INTEGER NOT NULL, lost INTEGER NOT NULL,
  sum REAL NOT NULL, min REAL, max REAL, PRIMARY KEY (target_id, t)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS alerts (
  id TEXT PRIMARY KEY, customer_id TEXT NOT NULL DEFAULT '', data TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS alert_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT, t INTEGER NOT NULL, customer_id TEXT NOT NULL DEFAULT '', data TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS alert_events_t ON alert_events(t);
CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY, email TEXT NOT NULL UNIQUE COLLATE NOCASE, name TEXT NOT NULL DEFAULT '',
  pw_hash TEXT NOT NULL, role TEXT NOT NULL, customer_id TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, expires INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`

// OpenDB öppnar (och skapar vid behov) databasen.
func OpenDB(path string) (*DB, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	d, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(4)
	if _, err := d.Exec(schema + hostSchema + opsSchema + auditSchema + wifiSchema + traceSchema); err != nil {
		return nil, fmt.Errorf("databasschema: %w", err)
	}
	// nya kolumner i senare versioner (fel ignoreras om de redan finns)
	for _, m := range []string{
		`ALTER TABLE customers ADD COLUMN report_to TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE customers ADD COLUMN report_freq TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE customers ADD COLUMN report_last INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE customers ADD COLUMN status_key TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN totp_secret TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN totp_recovery TEXT NOT NULL DEFAULT ''`,
	} {
		_, _ = d.Exec(m)
	}
	return &DB{d}, nil
}

// ---------- typer ----------

type Customer struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Notes      string `json:"notes"`
	Created    int64  `json:"created"`
	ReportTo   string `json:"reportTo"`   // mottagare av automatiska rapporter
	ReportFreq string `json:"reportFreq"` // "", "weekly", "monthly"
	ReportLast int64  `json:"reportLast"` // slutet på senast skickade period
	StatusKey  string `json:"statusKey"`  // hemlig nyckel för publik statussida ("" = av)
}

type Site struct {
	ID         string `json:"id"`
	CustomerID string `json:"customerId"`
	Name       string `json:"name"`
	Created    int64  `json:"created"`
}

type Agent struct {
	ID            string `json:"id"`
	SiteID        string `json:"siteId"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	Version       string `json:"version"`
	IP            string `json:"ip"`
	LastSeen      int64  `json:"lastSeen"`
	ConfigVersion int64  `json:"configVersion"`
	LocalHash     string `json:"-"`
	Created       int64  `json:"created"`
}

// Target är ett mål på servern: ett vanligt mål plus vilken agent som mäter det.
type Target struct {
	monitor.Target
	AgentID string `json:"agentId"`
	Source  string `json:"source"` // server = styrs från servern, agent = skrivbordsappens egna
	Sort    int    `json:"sort"`
}

// Rule är en larmregel. CustomerID tom = gäller alla kunder.
type Rule struct {
	alert.Alert
	CustomerID string `json:"customerId"`
}

type User struct {
	ID         string `json:"id"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	Role       string `json:"role"` // admin, viewer
	CustomerID string `json:"customerId"`
	Created    int64  `json:"created"`
	TOTP       bool   `json:"totp"` // tvåstegsinloggning aktiverad
}

// ServerSettings är serverns inställningar.
type ServerSettings struct {
	PublicURL    string           `json:"publicUrl"`
	NoAutoUpdate bool             `json:"noAutoUpdate"` // agenter uppdateras inte automatiskt
	BackupKeep   int              `json:"backupKeep"`   // antal nattliga säkerhetskopior som sparas
	ReportName   string           `json:"reportName"`   // företagsnamn som står på rapporterna
	RawDays      int              `json:"rawDays"`
	AggDays      int              `json:"aggDays"`
	ReportSec    int              `json:"reportSec"`
	Monitor      monitor.Settings `json:"monitor"`
	SMTP         alert.SMTP       `json:"smtp"`
}

func defaultServerSettings() ServerSettings {
	m := monitor.DefaultSettings()
	m.HistorySize = 3600
	return ServerSettings{RawDays: 7, AggDays: 400, ReportSec: 5, Monitor: m, BackupKeep: 7,
		SMTP: alert.SMTP{Port: 587, Security: "starttls", MinMinutes: 5}}
}

func now() int64 { return time.Now().UnixMilli() }

// ---------- inställningar ----------

func (d *DB) LoadSettings() ServerSettings {
	s := defaultServerSettings()
	var raw string
	if err := d.QueryRow(`SELECT value FROM settings WHERE key='server'`).Scan(&raw); err == nil {
		_ = json.Unmarshal([]byte(raw), &s)
	}
	if s.RawDays < 1 {
		s.RawDays = 7
	}
	if s.AggDays < 7 {
		s.AggDays = 400
	}
	if s.ReportSec < 2 || s.ReportSec > 300 {
		s.ReportSec = 5
	}
	return s
}

func (d *DB) SaveSettings(s ServerSettings) error {
	b, _ := json.Marshal(s)
	_, err := d.Exec(`INSERT INTO settings(key,value) VALUES('server',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, string(b))
	return err
}

// ---------- kunder, siter, agenter ----------

func (d *DB) Customers() ([]Customer, error) {
	rows, err := d.Query(`SELECT id,name,notes,created,report_to,report_freq,report_last,status_key FROM customers ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Customer{}
	for rows.Next() {
		var c Customer
		if err := rows.Scan(&c.ID, &c.Name, &c.Notes, &c.Created, &c.ReportTo, &c.ReportFreq, &c.ReportLast, &c.StatusKey); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) Sites() ([]Site, error) {
	rows, err := d.Query(`SELECT id,customer_id,name,created FROM sites ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Site{}
	for rows.Next() {
		var s Site
		if err := rows.Scan(&s.ID, &s.CustomerID, &s.Name, &s.Created); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

const agentCols = `id,site_id,name,kind,hostname,os,version,ip,last_seen,config_version,local_hash,created`

func scanAgent(sc interface{ Scan(...any) error }) (Agent, error) {
	var a Agent
	err := sc.Scan(&a.ID, &a.SiteID, &a.Name, &a.Kind, &a.Hostname, &a.OS, &a.Version, &a.IP, &a.LastSeen, &a.ConfigVersion, &a.LocalHash, &a.Created)
	return a, err
}

func (d *DB) Agents() ([]Agent, error) {
	rows, err := d.Query(`SELECT ` + agentCols + ` FROM agents ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Agent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (d *DB) AgentByToken(hash string) (Agent, error) {
	return scanAgent(d.QueryRow(`SELECT `+agentCols+` FROM agents WHERE token_hash=?`, hash))
}

func (d *DB) Targets() ([]Target, error) {
	rows, err := d.Query(`SELECT id,agent_id,source,sort,data FROM targets ORDER BY sort, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Target{}
	for rows.Next() {
		var t Target
		var id, data string
		if err := rows.Scan(&id, &t.AgentID, &t.Source, &t.Sort, &data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(data), &t.Target); err != nil {
			continue
		}
		t.ID = id
		out = append(out, t)
	}
	return out, rows.Err()
}

func (d *DB) SaveTarget(t Target) error {
	b, _ := json.Marshal(t.Target)
	_, err := d.Exec(`INSERT INTO targets(id,agent_id,source,sort,data) VALUES(?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET agent_id=excluded.agent_id, source=excluded.source, sort=excluded.sort, data=excluded.data`,
		t.ID, t.AgentID, t.Source, t.Sort, string(b))
	return err
}

func (d *DB) DeleteTarget(id string) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM targets WHERE id=?`, `DELETE FROM samples WHERE target_id=?`, `DELETE FROM agg_1m WHERE target_id=?`} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) BumpConfig(agentID string) {
	_, _ = d.Exec(`UPDATE agents SET config_version=config_version+1 WHERE id=?`, agentID)
}

// ---------- regler och händelser ----------

func (d *DB) Rules() ([]Rule, error) {
	rows, err := d.Query(`SELECT id,customer_id,data FROM alerts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		var r Rule
		var id, data string
		if err := rows.Scan(&id, &r.CustomerID, &data); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(data), &r.Alert) != nil {
			continue
		}
		r.ID = id
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) SaveRule(r Rule) error {
	b, _ := json.Marshal(r.Alert)
	_, err := d.Exec(`INSERT INTO alerts(id,customer_id,data) VALUES(?,?,?)
		ON CONFLICT(id) DO UPDATE SET customer_id=excluded.customer_id, data=excluded.data`, r.ID, r.CustomerID, string(b))
	return err
}

// StoredEvent är en larmhändelse i databasen.
type StoredEvent struct {
	alert.Event
	CustomerID string `json:"customerId"`
}

func (d *DB) AddEvent(customerID string, ev alert.Event) {
	b, _ := json.Marshal(ev)
	_, _ = d.Exec(`INSERT INTO alert_events(t,customer_id,data) VALUES(?,?,?)`, ev.Time, customerID, string(b))
}

func (d *DB) Events(customerID string, limit int) ([]StoredEvent, error) {
	q := `SELECT customer_id,data FROM alert_events`
	var args []any
	if customerID != "" {
		q += ` WHERE customer_id=?`
		args = append(args, customerID)
	}
	q += ` ORDER BY t DESC LIMIT ?`
	args = append(args, limit)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StoredEvent{}
	for rows.Next() {
		var e StoredEvent
		var data string
		if err := rows.Scan(&e.CustomerID, &data); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(data), &e.Event) == nil {
			out = append(out, e)
		}
	}
	return out, rows.Err()
}

// ---------- mätdata ----------

// InsertSamples sparar rådata och uppdaterar minutaggregaten i en transaktion.
func (d *DB) InsertSamples(ctx context.Context, rows []storedSample) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ins, err := tx.Prepare(`INSERT OR REPLACE INTO samples(target_id,t,rtt) VALUES(?,?,?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	agg, err := tx.Prepare(`INSERT INTO agg_1m(target_id,t,n,lost,sum,min,max) VALUES(?,?,1,?,?,?,?)
		ON CONFLICT(target_id,t) DO UPDATE SET n=n+1, lost=lost+excluded.lost, sum=sum+excluded.sum,
		min=CASE WHEN excluded.min IS NULL THEN agg_1m.min WHEN agg_1m.min IS NULL THEN excluded.min ELSE min(agg_1m.min, excluded.min) END,
		max=CASE WHEN excluded.max IS NULL THEN agg_1m.max WHEN agg_1m.max IS NULL THEN excluded.max ELSE max(agg_1m.max, excluded.max) END`)
	if err != nil {
		return err
	}
	defer agg.Close()
	for _, r := range rows {
		if _, err := ins.Exec(r.id, r.t, r.rtt); err != nil {
			return err
		}
		minute := r.t - r.t%60000
		if r.rtt < 0 {
			_, err = agg.Exec(r.id, minute, 1, 0.0, nil, nil)
		} else {
			_, err = agg.Exec(r.id, minute, 0, r.rtt, r.rtt, r.rtt)
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

type storedSample struct {
	id  string
	t   int64
	rtt float64
}

// History är svaret på en historikfråga. Mode "raw" fyller R; "agg" fyller Avg/Min/Max/Loss.
type History struct {
	Mode   string     `json:"mode"`
	Bucket int64      `json:"bucket,omitempty"` // ms
	T      []int64    `json:"t"`
	R      []float64  `json:"r,omitempty"`
	Avg    []*float64 `json:"avg,omitempty"`
	Min    []*float64 `json:"min,omitempty"`
	Max    []*float64 `json:"max,omitempty"`
	Loss   []float64  `json:"loss,omitempty"` // procent
	N      []int      `json:"n,omitempty"`
}

func (d *DB) RecentSamples(id string, n int) ([]monitor.Sample, error) {
	rows, err := d.Query(`SELECT t,rtt FROM (SELECT t,rtt FROM samples WHERE target_id=? ORDER BY t DESC LIMIT ?) ORDER BY t`, id, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []monitor.Sample
	for rows.Next() {
		var s monitor.Sample
		var r float64
		if err := rows.Scan(&s.T, &r); err != nil {
			return nil, err
		}
		s.RTT = float32(r)
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) History(id string, from, to int64, rawDays int) (History, error) {
	span := to - from
	rawLimit := now() - int64(rawDays)*86400000
	if span <= 6*3600*1000 && from >= rawLimit {
		rows, err := d.Query(`SELECT t,rtt FROM samples WHERE target_id=? AND t>? AND t<=? ORDER BY t`, id, from, to)
		if err != nil {
			return History{}, err
		}
		defer rows.Close()
		h := History{Mode: "raw", T: []int64{}, R: []float64{}}
		for rows.Next() {
			var t int64
			var r float64
			if err := rows.Scan(&t, &r); err != nil {
				return h, err
			}
			h.T = append(h.T, t)
			h.R = append(h.R, r)
		}
		return h, rows.Err()
	}
	bucket := int64(60000)
	for _, b := range []int64{60000, 300000, 900000, 3600000, 4 * 3600000} {
		bucket = b
		if span/b <= 2000 {
			break
		}
	}
	rows, err := d.Query(`SELECT (t/?)*? AS b, sum(n), sum(lost), sum(sum), min(min), max(max)
		FROM agg_1m WHERE target_id=? AND t>? AND t<=? GROUP BY b ORDER BY b`, bucket, bucket, id, from-bucket, to)
	if err != nil {
		return History{}, err
	}
	defer rows.Close()
	h := History{Mode: "agg", Bucket: bucket, T: []int64{}}
	for rows.Next() {
		var t int64
		var n, lost int
		var sum float64
		var mn, mx sql.NullFloat64
		if err := rows.Scan(&t, &n, &lost, &sum, &mn, &mx); err != nil {
			return h, err
		}
		h.T = append(h.T, t)
		h.N = append(h.N, n)
		ok := n - lost
		var avg, pmin, pmax *float64
		if ok > 0 {
			a := sum / float64(ok)
			avg = &a
		}
		if mn.Valid {
			v := mn.Float64
			pmin = &v
		}
		if mx.Valid {
			v := mx.Float64
			pmax = &v
		}
		h.Avg = append(h.Avg, avg)
		h.Min = append(h.Min, pmin)
		h.Max = append(h.Max, pmax)
		loss := 0.0
		if n > 0 {
			loss = float64(lost) * 100 / float64(n)
		}
		h.Loss = append(h.Loss, loss)
	}
	return h, rows.Err()
}

// Prune tar bort gammal mätdata.
func (d *DB) Prune(rawDays, aggDays int) (int64, error) {
	var total int64
	r, err := d.Exec(`DELETE FROM samples WHERE t < ?`, now()-int64(rawDays)*86400000)
	if err != nil {
		return 0, err
	}
	n, _ := r.RowsAffected()
	total += n
	r, err = d.Exec(`DELETE FROM agg_1m WHERE t < ?`, now()-int64(aggDays)*86400000)
	if err != nil {
		return total, err
	}
	n, _ = r.RowsAffected()
	total += n
	_, _ = d.Exec(`DELETE FROM alert_events WHERE t < ?`, now()-int64(aggDays)*86400000)
	_, _ = d.Exec(`DELETE FROM sessions WHERE expires < ?`, now())
	_, _ = d.Exec(`DELETE FROM audit WHERE t < ?`, now()-int64(aggDays)*86400000)
	return total, nil
}

// ---------- användare ----------

func (d *DB) Users() ([]User, error) {
	rows, err := d.Query(`SELECT id,email,name,role,customer_id,created,totp_secret<>'' FROM users ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.CustomerID, &u.Created, &u.TOTP); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (d *DB) UserCount() int {
	var n int
	_ = d.QueryRow(`SELECT count(*) FROM users`).Scan(&n)
	return n
}

func (d *DB) UserByEmail(email string) (User, string, error) {
	var u User
	var hash string
	err := d.QueryRow(`SELECT id,email,name,role,customer_id,created,pw_hash FROM users WHERE email=?`, strings.TrimSpace(email)).
		Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.CustomerID, &u.Created, &hash)
	return u, hash, err
}

func (d *DB) UserBySession(tokenHash string) (User, error) {
	var u User
	err := d.QueryRow(`SELECT u.id,u.email,u.name,u.role,u.customer_id,u.created,u.totp_secret<>'' FROM sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token_hash=? AND s.expires>?`, tokenHash, now()).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.CustomerID, &u.Created, &u.TOTP)
	return u, err
}

var errNotFound = errors.New("hittades inte")

// toProto konverterar mål för en agent.
func toProto(ts []Target) []monitor.Target {
	out := make([]monitor.Target, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Target)
	}
	return out
}

var _ = agentproto.ReportPath
