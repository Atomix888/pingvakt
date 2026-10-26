// Package i18n översätter PingVakts texter. Källtexterna i koden är engelska;
// sv.json innehåller de svenska översättningarna (engelska -> svenska).
//
// En nyckel kan ha ett sammanhang efter "|" (t.ex. "Paused|pl") när samma
// engelska ord behöver olika svenska översättningar. Sammanhanget tas bort
// när texten visas på engelska.
package i18n

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync/atomic"
)

//go:embed sv.json
var svJSON []byte

var sv map[string]string

var cur atomic.Value

func init() {
	sv = map[string]string{}
	_ = json.Unmarshal(svJSON, &sv)
	cur.Store("en")
}

// Languages är de språk som stöds.
var Languages = []string{"en", "sv"}

// Norm returnerar ett giltigt språk ("en" eller "sv"). Tomt eller okänt blir def.
func Norm(lang, def string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if i := strings.IndexAny(lang, "-_"); i > 0 {
		lang = lang[:i]
	}
	switch lang {
	case "en", "sv":
		return lang
	}
	if def == "" {
		return "en"
	}
	return def
}

// SetLang väljer språk för T.
func SetLang(lang string) { cur.Store(Norm(lang, "en")) }

// Lang returnerar aktuellt språk.
func Lang() string { return cur.Load().(string) }

// T översätter s till aktuellt språk.
func T(s string) string { return TL(Lang(), s) }

// TL översätter s till språket lang.
func TL(lang, s string) string {
	if lang == "sv" {
		if v, ok := sv[s]; ok {
			return v
		}
	}
	return strip(s)
}

func strip(s string) string {
	if i := strings.LastIndexByte(s, '|'); i > 0 && i < len(s)-1 {
		ctx := s[i+1:]
		if !strings.ContainsAny(ctx, " \t\n") && strings.ToLower(ctx) == ctx && len(ctx) <= 12 {
			return s[:i]
		}
	}
	return s
}

// Dict returnerar ordboken för språket (tom för engelska).
func Dict(lang string) map[string]string {
	if Norm(lang, "en") == "sv" {
		return sv
	}
	return map[string]string{}
}

// Script returnerar JavaScript som sätter språket och ordboken för webbgränssnitten.
func Script(lang string) []byte {
	lang = Norm(lang, "en")
	d, _ := json.Marshal(Dict(lang))
	var b strings.Builder
	b.WriteString("window.PV_LANG=")
	l, _ := json.Marshal(lang)
	b.Write(l)
	b.WriteString(";window.PV_DICT=")
	b.Write(d)
	b.WriteString(";document.documentElement.lang=window.PV_LANG;\n")
	return []byte(b.String())
}
