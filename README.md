# PingVakt

PingVakt övervakar latens och paketförlust mot många mål samtidigt, på samma sätt som MultiPing och vmPing. Programmet är skrivet i Go och [Wails](https://wails.io) och körs på Windows, macOS och Linux.

Version 0.1 (MVP) innehåller **mätmotorn** och **skrivbordsappen**. Agenten och servern kommer i nästa etapp och kommer att använda samma mätmotor (se *Färdplan*).

## Funktioner

- **Många mål samtidigt** visas som rutor med statusfärg (uppe, varning, nere, pausad). Varje ruta har en minigraf med staplar, och förlorade paket syns som röda staplar.
- **ICMP-ping (IPv4 och IPv6)** och **TCP-portping**. Skriv `värd:port` för TCP.
- **Statistik per mål:** senaste värde, snitt, min, max, jitter och förlust i %. I detaljvyn finns dessutom median och 95:e percentil.
- **Detaljgraf** över tid (1 min till 6 h). Grafen visar latens, markerar förlorade paket och har en linje för varningsgränsen. Du kan zooma genom att dra i grafen och återställa med dubbelklick.
- **Larmregler** av samma typ som i MultiPing:
  - *Nere:* X förlorade av de senaste Y paketen.
  - *Hög latens:* X av de senaste Y paketen över N ms. Förlorade paket räknas också.
  - *Paketförlust:* förlusten över de senaste Y paketen överstiger N %.

  När ett larm går kan programmet visa en popup, spela ljud, visa en systemnotis, skicka **e-post via SMTP** och skriva till **loggfil**. Larmen kan upprepas medan de pågår och meddela när de upphör. E-post kan begränsas till högst ett mejl per N minuter, och händelser däremellan samlas i nästa mejl.
- **Traceroute** med knappar som lägger till hoppen som mål.
- **CSV-export** av mätdata. Filen är semikolonseparerad och öppnas direkt i svenskt Excel.
- Egen sorteringsordning med dra och släpp, filtrering, sortering, val av antal kolumner, kompakt läge och mörkt/ljust tema.
- Inställningarna sparas i:
  - Windows: `%APPDATA%\PingVakt\config.json`
  - macOS: `~/Library/Application Support/PingVakt/config.json`
  - Linux: `~/.config/PingVakt/config.json`

## Installera

| Plattform | Fil |
|---|---|
| Windows 10/11 (x64) | `PingVakt-<ver>-windows-x64-setup.exe` (installationsprogram) eller `…-portable.zip` |
| Debian/Ubuntu | `pingvakt_<ver>_amd64.deb` → `sudo apt install ./pingvakt_<ver>_amd64.deb` |
| Övriga Linux | `pingvakt-<ver>-linux-amd64.tar.gz` (kräver GTK 3 och WebKitGTK 4.1) |
| macOS 11+ (Intel och Apple Silicon) | `PingVakt-<ver>-macos.dmg`, byggs i GitHub Actions |

Programmet är inte kodsignerat. Därför varnar Windows SmartScreen ("Kör ändå") och macOS Gatekeeper första gången (högerklicka på appen → Öppna).

### Behörigheter för ICMP

- **Windows:** använder systemets ICMP-API (samma som `ping.exe`) och kräver inte administratörsrättigheter.
- **macOS:** använder oprivilegierade ICMP-socketar och kräver inga särskilda rättigheter.
- **Linux:** använder oprivilegierade ICMP-socketar om `net.ipv4.ping_group_range` tillåter det, vilket är standard i Ubuntu och Fedora. .deb-paketet ger dessutom programmet `cap_net_raw` som reserv. Ger ping fel ändå kan du köra:
  `sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"`

Traceroute använder systemets verktyg: `tracert` på Windows och `traceroute` på macOS/Linux. På Linux behöver paketet `traceroute` vara installerat.

## Bygga själv

Krav: Go 1.22+.
- **Linux:** `libgtk-3-dev` och `libwebkit2gtk-4.1-dev`.
- **Windows-installationsprogrammet:** `nsis` och `imagemagick` (byggs från Linux).
- **macOS:** Xcode Command Line Tools.

```bash
go mod tidy                 # första gången
go test ./core/...          # tester för mätmotorn
scripts/build.sh linux      # binär + .deb + .tar.gz
scripts/build.sh windows    # PingVakt.exe + installationsprogram + portabel zip (korskompileras från Linux)
scripts/build.sh macos      # PingVakt.app + .dmg (på en Mac)
```

**GitHub Actions** (`.github/workflows/build.yml`) bygger alla tre plattformarna vid varje push. När du pushar en tagg, till exempel `v0.1.0`, skapas en release med alla installationsfiler.

**Utveckla gränssnittet utan att bygga:** öppna `desktop/frontend/dist/index.html` via en lokal webbserver, till exempel med `python3 -m http.server`. Sidan går då i *demoläge* med simulerad data.

## Struktur

```
core/              Mätmotorn – delas av skrivbordsapp, agent och server
  probe/           ICMP (Windows-API / oprivilegierade socketar) och TCP-ping, DNS-cache
  monitor/         Schemaläggning per mål, ringbuffert med historik, statistik och status
  alert/           Larmregler, utvärdering, e-post (SMTP), systemnotiser, loggfil
  trace/           Traceroute via systemets verktyg
  config/          JSON-konfiguration
desktop/           Skrivbordsappen (Wails)
  app.go           Brygga mellan gränssnittet och mätmotorn
  frontend/dist/   Gränssnittet (HTML/CSS/JS + uPlot), inget byggsteg
  build/           Ikon, Windows-installationsprogram (NSIS), .deb-filer, macOS Info.plist
tools/winres/      Skapar Windows-ikon, manifest och versionsinfo
scripts/build.sh   Byggskript för alla plattformar
```

## Färdplan

**Etapp 2 – agent och server (flera kunder)**
- `agent/`: tjänst utan gränssnitt (Windows-tjänst, systemd, launchd, Raspberry Pi) som använder `core/monitor`. Agenten ansluter **utåt** till servern över HTTPS/WebSocket med en token, så det fungerar bakom NAT och brandvägg hos kunden. Den hämtar sina mål och larmregler från servern och skickar upp mätdata, med buffring om förbindelsen bryts.
- `server/`: tar emot mätdata från agenter och skrivbordsappar. Den sparar historik i en databas och har ett webbgränssnitt som bygger på samma frontend. Servern delar upp data per **kund och site**, har användare och roller (konsult, kund med läsrätt) och skickar larm centralt.
- Skrivbordsappen får inställningen "Anslut till server", som gör att den samtidigt fungerar som agent.

**Senare:** rapporter som PDF per kund, kodsignering, engelska som språkval, lösenord i operativsystemets nyckelring och HTTP(S)-kontroller som mättyp.

## Licenser

Se `LICENSE-THIRD-PARTY.txt`. PingVakt innehåller Wails och uPlot, som båda har MIT-licens. Funktionerna är inspirerade av vmPing (MIT), men koden är nyskriven.
