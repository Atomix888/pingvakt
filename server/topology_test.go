package main

import "testing"

func TestGuessType(t *testing.T) {
	cases := map[string]string{"AP Kontoret U6-Pro": "ap", "core-sw": "switch", "UDM Pro": "gateway", "MR46 Kassan": "ap", "MS120-8LP": "switch",
		"MX68": "gateway", "Comms room printer": "other", "edge-sw USW-16-PoE": "switch", "Cisco Catalyst 9200": "switch"}
	for in, want := range cases {
		if got := guessType(in); got != want {
			t.Errorf("%q: %s, ville ha %s", in, got, want)
		}
	}
	if normName("core-sw.example.local") != "core-sw" || normName("192.168.1.2") != "192.168.1.2" {
		t.Error("normName")
	}
}
