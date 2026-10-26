package wifi

import (
	"strings"
	"testing"
)

const iwOut = `Connected to 3c:84:6a:aa:bb:cc (on wlp2s0)
	SSID: Kontoret
	freq: 5180.0
	RX: 12345 bytes (100 packets)
	TX: 2345 bytes (20 packets)
	signal: -58 dBm
	rx bitrate: 866.7 MBit/s VHT-MCS 9 80MHz short GI VHT-NSS 2
	tx bitrate: 650.0 MBit/s VHT-MCS 7 80MHz short GI VHT-NSS 2
`

const survey = `Survey data from wlp2s0
	frequency:			5170 MHz
	noise:				-96 dBm
Survey data from wlp2s0
	frequency:			5180 MHz [in use]
	noise:				-92 dBm
`

const netshSv = "    Name                   : X\r\n    SSID : \x86\x84\x94-n\x84t\r\n    BSSID : 01:02:03:04:05:06\r\n    Signal : 50%\r\n\r\nDet finns 1 gränssnitt i systemet:\r\n\r\n    Namn                   : Wi-Fi\r\n    Beskrivning            : Intel(R) Wi-Fi 6 AX201 160MHz\r\n    Fysisk adress          : 11:22:33:44:55:66\r\n    Tillstånd              : ansluten\r\n    SSID                   : Lager\r\n    AP BSSID               : A0:B1:C2:D3:E4:F5\r\n    Band                   : 5 GHz\r\n    Kanal                  : 44\r\n    Radiotyp               : 802.11ax\r\n    Mottagningshastighet (Mbit/s)  : 1201\r\n    \x99verföringshastighet (Mbit/s)  : 960\r\n    Signal                 : 82%\r\n    Profil                 : Lager\r\n"

const netshEn = `
There is 2 interfaces on the system:

    Name                   : Wi-Fi 2
    State                  : disconnected

    Name                   : Wi-Fi
    State                  : connected
    SSID                   : Butik
    BSSID                  : 00:11:22:33:44:55
    Network type           : Infrastructure
    Radio type             : 802.11n
    Channel                : 6
    Receive rate (Mbps)    : 144.4
    Transmit rate (Mbps)   : 130
    Signal                 : 70%
    Rssi                   : -61
`

const wdutil = `————————————————————————————————————————————————
NETWORK
————————————————————————————————————————————————
    Primary IPv4         : en0 (Wi-Fi / 1234)
————————————————————————————————————————————————
WIFI
————————————————————————————————————————————————
    MAC Address          : aa:aa:aa:aa:aa:aa (hw=aa:aa:aa:aa:aa:aa)
    Interface Name       : en0
    Power                : On [On]
    SSID                 : Hemma
    BSSID                : 9c:1c:12:00:00:01
    RSSI                 : -49 dBm
    CCA                  : 7 %
    Noise                : -94 dBm
    Tx Rate              : 1200.0 Mbps
    PHY Mode             : 11ax
    Channel              : 5g100/80
————————————————————————————————————————————————
BLUETOOTH
————————————————————————————————————————————————
    Power                : On
`

const sp = `Wi-Fi:

      Software Versions:
          CoreWLAN: 16.0 (1657)
      Interfaces:
        en0:
          Card Type: Wi-Fi  (0x14E4, 0x4387)
          Status: Connected
          Current Network Information:
            <redacted>:
              PHY Mode: 802.11ac
              Channel: 36 (5GHz, 80MHz)
              Country Code: SE
              Network Type: Infrastructure
              Security: WPA2 Personal
              Signal / Noise: -55 dBm / -91 dBm
              Transmit Rate: 866
              MCS Index: 9
          Other Local Wi-Fi Networks:
            Granne:
              Channel: 1 (2GHz, 20MHz)
`

func TestParsers(t *testing.T) {
	in := parseIwLink(iwOut)
	if in == nil || in.SSID != "Kontoret" || in.BSSID != "3c:84:6a:aa:bb:cc" || in.RSSI != -58 || in.Ch != 36 || in.Width != 80 || in.Rx != 866.7 || in.Tx != 650 || in.Std != "802.11ac" || in.Iface != "wlp2s0" {
		t.Fatalf("iw: %+v", in)
	}
	if parseIwLink("Not connected.\n") != nil {
		t.Fatal("iw: inte ansluten")
	}
	if n := parseSurveyNoise(survey); n != -92 {
		t.Fatalf("survey: %d", n)
	}
	if s, n := parseProcWireless("Inter-| sta-|   Quality        |\n face | tus | link level noise |\nwlp2s0: 0000   52.  -58.  -256        0      0      0      0      0        0\n", "wlp2s0"); s != -58 || n != 0 {
		t.Fatalf("proc: %d %d", s, n)
	}
	if in := parseNetsh(netshSv); in == nil || in.SSID != "åäö-nät" || in.RSSI != -75 {
		t.Fatalf("netsh cp850: %+v", in)
	}
	in = parseNetsh(netshSv[strings.Index(netshSv, "Det finns"):])
	if in == nil || in.SSID != "Lager" || in.BSSID != "A0:B1:C2:D3:E4:F5" || in.Ch != 44 || in.Freq != 5220 || in.RSSI != -59 || in.Rx != 1201 || in.Tx != 960 {
		t.Fatalf("netsh sv: %+v", in)
	}
	in = parseNetsh(netshEn)
	if in == nil || in.SSID != "Butik" || in.RSSI != -61 || in.Ch != 6 || in.Freq != 2437 || in.Tx != 130 || in.Iface != "Wi-Fi" {
		t.Fatalf("netsh en: %+v", in)
	}
	in = parseWdutil(wdutil)
	if in == nil || in.SSID != "Hemma" || in.BSSID != "9c:1c:12:00:00:01" || in.RSSI != -49 || in.Noise != -94 || in.Ch != 100 || in.Width != 80 || in.Freq != 5500 || in.Tx != 1200 {
		t.Fatalf("wdutil: %+v", in)
	}
	in = parseSystemProfiler(sp)
	if in == nil || in.SSID != "" || in.RSSI != -55 || in.Noise != -91 || in.Ch != 36 || in.Width != 80 || in.Tx != 866 || in.SNR() != 36 || in.Band() != "5 GHz" {
		t.Fatalf("system_profiler: %+v", in)
	}
}
