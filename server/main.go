// Command pingvakt-server tar emot mätdata från agenter och skrivbordsappar,
// lagrar historik, larmar centralt och visar allt i ett webbgränssnitt.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Version sätts vid bygge.
var Version = "0.13.0"

//go:embed all:web
var webFS embed.FS

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	addr := flag.String("listen", env("PINGVAKT_LISTEN", ":8080"), "address to listen on")
	dataDir := flag.String("data", env("PINGVAKT_DATA", "./data"), "folder for the database")
	agentsDir := flag.String("agents", env("PINGVAKT_AGENTS_DIR", "./agents"), "folder with agent files for download")
	tlsCert := flag.String("tls-cert", env("PINGVAKT_TLS_CERT", ""), "TLS certificate (optional – otherwise behind a reverse proxy)")
	tlsKey := flag.String("tls-key", env("PINGVAKT_TLS_KEY", ""), "TLS-nyckel")
	syslogAddr := flag.String("syslog", env("PINGVAKT_SYSLOG", ""), "receive syslog on this UDP address, e.g. :5514 (optional)")
	trapAddr := flag.String("traps", env("PINGVAKT_TRAPS", ""), "receive SNMP traps on this UDP address, e.g. :1162 (optional)")
	proxy := flag.Bool("trust-proxy", env("PINGVAKT_TRUST_PROXY", "") == "1", "trust X-Forwarded-* from a reverse proxy")
	flag.Parse()
	trustProxy = *proxy

	if err := os.MkdirAll(*dataDir, 0o750); err != nil {
		log.Fatal(err)
	}
	db, err := OpenDB(filepath.Join(*dataDir, "pingvakt.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// första administratören kan skapas via miljövariabler (t.ex. i Docker)
	if db.UserCount() == 0 {
		if e, p := os.Getenv("PINGVAKT_ADMIN_EMAIL"), os.Getenv("PINGVAKT_ADMIN_PASSWORD"); e != "" && p != "" {
			srv := &Server{db: db}
			if _, err := srv.createUser(e, "Administrator", "admin", "", p); err != nil {
				log.Fatalf("create administrator: %v", err)
			}
			log.Printf("created the administrator %s", e)
		}
	}

	s, err := NewServer(db, Version, *agentsDir, *dataDir)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s.Run(ctx)
	s.startLogReceiver(ctx, *syslogAddr, *trapAddr)

	sub, _ := fs.Sub(webFS, "web")
	web := spaHandler(http.FS(sub))
	hs := &http.Server{Addr: *addr, Handler: s.Routes(web), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 120 * time.Second}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(c)
	}()
	log.Printf("PingVakt server %s listening on %s (data: %s)", Version, *addr, *dataDir)
	if db.UserCount() == 0 {
		log.Printf("no user exists yet – open the web interface to create the administrator")
	}
	if *tlsCert != "" {
		err = hs.ListenAndServeTLS(*tlsCert, *tlsKey)
	} else {
		err = hs.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// spaHandler serverar webbgränssnittet; okända sökvägar ger index.html.
func spaHandler(root http.FileSystem) http.Handler {
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
	fsrv := http.FileServer(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/api/") {
			httpErr(w, 404, "unknown endpoint")
			return
		}
		if p != "/" {
			if f, err := root.Open(p); err == nil {
				f.Close()
				if strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".css") {
					w.Header().Set("Cache-Control", "no-cache")
				}
				fsrv.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		r.URL.Path = "/"
		fsrv.ServeHTTP(w, r)
	})
}
