package wifi

import "testing"

const iwScan = `BSS 11:22:33:44:55:66(on wlan0) -- associated
	last seen: 120 ms ago
	freq: 5180.0
	beacon interval: 100 TUs
	capability: ESS Privacy SpectrumMgmt (0x0111)
	signal: -48.00 dBm
	SSID: Kontoret
	RSN:	 * Version: 1
		 * Group cipher: CCMP
		 * Pairwise ciphers: CCMP
		 * Authentication suites: PSK SAE
	HT capabilities:
	HT operation:
		 * primary channel: 36
		 * secondary channel offset: above
	VHT capabilities:
	VHT operation:
		 * channel width: 1 (80 MHz)
	HE capabilities:
BSS aa:bb:cc:dd:ee:ff(on wlan0)
	freq: 2437
	capability: ESS (0x0401)
	signal: -77.00 dBm
	SSID: Gäst
	HT operation:
		 * primary channel: 6
		 * secondary channel offset: no secondary
BSS 00:11:22:33:44:55(on wlan0)
	freq: 2412
	capability: ESS Privacy (0x0411)
	signal: -70.00 dBm
	SSID: \x00\x00
	RSN:	 * Version: 1
		 * Authentication suites: IEEE 802.1X
`

func TestParseIwScan(t *testing.T) {
	l := parseIwScan(iwScan)
	if len(l) != 3 {
		t.Fatalf("%d nät", len(l))
	}
	a := l[0]
	if a.SSID != "Kontoret" || a.Ch != 36 || a.Width != 80 || a.RSSI != -48 || a.Sec != "WPA2/WPA3" || !a.Conn || a.Std != "802.11ax" {
		t.Errorf("%+v", a)
	}
	if b := l[1]; b.Sec != "Open" || b.Ch != 6 || b.Width != 20 || b.Std != "" {
		t.Errorf("%+v", b)
	}
	if c := l[2]; c.SSID != "" || c.Sec != "WPA2 Enterprise" {
		t.Errorf("%+v", c)
	}
}

func TestParseNmcli(t *testing.T) {
	l := parseNmcli("*:Kontoret:11\\:22\\:33\\:44\\:55\\:66:36:5180 MHz:90:WPA2 WPA3\n :Gäst:AA\\:BB\\:CC\\:DD\\:EE\\:FF:6:2437 MHz:40:--\n")
	if len(l) != 2 || l[0].BSSID != "11:22:33:44:55:66" || l[0].Sec != "WPA2/WPA3" || !l[0].Conn || l[0].RSSI != -55 || l[1].Sec != "Open" || l[1].Ch != 6 {
		t.Errorf("%+v", l)
	}
}

func TestParseNetshNetworks(t *testing.T) {
	out := "\r\nInterface name : Wi-Fi \r\nThere are 2 networks currently visible. \r\n\r\nSSID 1 : Kontoret\r\n    Network type            : Infrastructure\r\n    Authentication          : WPA2-Enterprise\r\n    Encryption              : CCMP \r\n    BSSID 1                 : 11:22:33:44:55:66\r\n         Signal             : 90%  \r\n         Radio type         : 802.11ax\r\n         Band               : 5 GHz\r\n         Channel            : 44 \r\n    BSSID 2                 : 11:22:33:44:55:67\r\n         Signal             : 40%  \r\n         Channel            : 1 \r\n\r\nSSID 2 : \r\n    Autentisering           : Öppen\r\n    BSSID 1                 : aa:bb:cc:dd:ee:ff\r\n         Signal             : 20%  \r\n         Kanal              : 149 \r\n"
	l := parseNetshNetworks(out)
	if len(l) != 3 {
		t.Fatalf("%+v", l)
	}
	if l[0].Ch != 44 || l[0].Freq != 5220 || l[0].Sec != "WPA2 Enterprise" || l[0].RSSI != -55 || l[0].Std != "802.11ax" {
		t.Errorf("%+v", l[0])
	}
	if l[1].Ch != 1 || l[1].Freq != 2412 || l[1].SSID != "Kontoret" {
		t.Errorf("%+v", l[1])
	}
	if l[2].SSID != "" || l[2].Sec != "Open" || l[2].Freq != 5745 {
		t.Errorf("%+v", l[2])
	}
}

func TestParseSPNetworks(t *testing.T) {
	out := `Wi-Fi:

      Interfaces:
        en0:
          Card Type: Wi-Fi  (0x14E4, 0x4387)
          Status: Connected
          Current Network Information:
            Kontoret:
              PHY Mode: 802.11ax
              Channel: 36 (5GHz, 80MHz)
              Security: WPA2 Personal
              Signal / Noise: -52 dBm / -94 dBm
          Other Local Wi-Fi Networks:
            Granne:
              PHY Mode: 802.11ac
              Channel: 11 (2GHz, 20MHz)
              Network Type: Infrastructure
              Security: WPA3 Personal
              Signal / Noise: -80 dBm / -92 dBm
            <redacted>:
              Channel: 100 (5GHz, 40MHz)
              Security: WPA2 Enterprise
              Signal / Noise: -71 dBm / -92 dBm
        awdl0:
          MAC Address: 11:22:33:44:55:66
`
	l := parseSPNetworks(out)
	if len(l) != 3 {
		t.Fatalf("%+v", l)
	}
	if !l[0].Conn || l[0].Width != 80 || l[0].Freq != 5180 || l[0].Sec != "WPA2" {
		t.Errorf("%+v", l[0])
	}
	if l[1].Conn || l[1].Ch != 11 || l[1].Freq != 2462 || l[1].Sec != "WPA3" {
		t.Errorf("%+v", l[1])
	}
	if l[2].SSID != "" || l[2].Width != 40 || l[2].Sec != "WPA2 Enterprise" {
		t.Errorf("%+v", l[2])
	}
}
