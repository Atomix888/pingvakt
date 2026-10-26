package netscan

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"strings"
	"sync"
)

// oui.txt.gz innehåller tillverkare per MAC-prefix ("AABBCC\tTillverkare").
// Byggservern hämtar IEEE:s fullständiga lista (go run ./tools/genoui);
// filen i repot är en liten reservlista.
//
//go:embed oui.txt.gz
var ouiGz []byte

var (
	ouiOnce sync.Once
	ouiMap  map[string]string
)

func loadOUI() {
	ouiMap = map[string]string{}
	zr, err := gzip.NewReader(bytes.NewReader(ouiGz))
	if err != nil {
		return
	}
	sc := bufio.NewScanner(zr)
	for sc.Scan() {
		p, v, ok := strings.Cut(sc.Text(), "\t")
		if ok && len(p) == 6 {
			ouiMap[p] = v
		}
	}
}

// Vendor returnerar tillverkaren för en MAC-adress ("" om okänd).
func Vendor(mac string) string {
	mac = normMAC(mac)
	if len(mac) < 8 {
		return ""
	}
	if isRandomMAC(mac) {
		return "Private address"
	}
	ouiOnce.Do(loadOUI)
	return ouiMap[strings.ToUpper(strings.ReplaceAll(mac[:8], ":", ""))]
}

// VendorCount är antalet kända tillverkarprefix.
func VendorCount() int {
	ouiOnce.Do(loadOUI)
	return len(ouiMap)
}
