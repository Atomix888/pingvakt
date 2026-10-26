// genoui hämtar IEEE:s lista över MAC-prefix och skriver core/netscan/oui.txt.gz.
// Körs i byggservern före bygget: go run ./tools/genoui
package main

import (
	"compress/gzip"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const src = "https://standards-oui.ieee.org/oui/oui.csv"

var suffix = regexp.MustCompile(`(?i)[,.]?\s+(inc|incorporated|corp|corporation|co|company|ltd|limited|llc|gmbh|ag|sa|s\.a|srl|bv|b\.v|ab|oy|as|a/s|plc|pty|kg|spa|s\.p\.a|technologies|technology|electronics|communications|networks|international|holdings|group|systems)\.?$`)

func short(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.Index(v, ","); i > 0 {
		v = v[:i]
	}
	for i := 0; i < 4; i++ {
		n := strings.TrimSpace(suffix.ReplaceAllString(v, ""))
		if n == v || n == "" {
			break
		}
		v = n
	}
	return v
}

func main() {
	c := &http.Client{Timeout: 60 * time.Second}
	req, _ := http.NewRequest("GET", src, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (PingVakt build)")
	res, err := c.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "genoui:", err)
		os.Exit(1)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		fmt.Fprintln(os.Stderr, "genoui: HTTP", res.StatusCode)
		os.Exit(1)
	}
	r := csv.NewReader(res.Body)
	r.FieldsPerRecord = -1
	m := map[string]string{}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(rec) < 3 || rec[0] != "MA-L" || len(rec[1]) != 6 {
			continue
		}
		m[strings.ToUpper(rec[1])] = short(rec[2])
	}
	if len(m) < 10000 {
		fmt.Fprintln(os.Stderr, "genoui: too few entries", len(m))
		os.Exit(1)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	f, err := os.Create("core/netscan/oui.txt.gz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "genoui:", err)
		os.Exit(1)
	}
	zw, _ := gzip.NewWriterLevel(f, gzip.BestCompression)
	for _, k := range keys {
		fmt.Fprintf(zw, "%s\t%s\n", k, m[k])
	}
	_ = zw.Close()
	_ = f.Close()
	fmt.Println("genoui:", len(m), "prefixes")
}
