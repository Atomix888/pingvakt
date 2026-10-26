# PingVakt

PingVakt övervakar latens och paketförlust mot många mål samtidigt, på samma sätt som MultiPing och vmPing. Programmet är skrivet i Go och [Wails](https://wails.io) och körs på Windows, macOS och Linux.

PingVakt består av tre delar som använder samma mätmotor:

- **Skrivbordsappen** fungerar fristående, som MultiPing. Den kan också kopplas till en server.
- **Servern** tar emot mätdata från många agenter, delar upp den per kund och site, sparar historik, larmar centralt och har ett webbgränssnitt.
- **Agenten** körs som tjänst utan gränssnitt hos kunden (Windows, Linux, Raspberry Pi eller macOS). Den ansluter utåt till servern, så inga portar behöver öppnas.

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

## Servern

**Snabbstart med Docker** (`deploy/docker-compose.yml` har Caddy, som ordnar HTTPS automatiskt):

```bash
cd deploy
# ändra domänen i Caddyfile och peka DNS mot servern
docker compose up -d
```

Utan Docker: packa upp `pingvakt-server-<ver>-linux-amd64.tar.gz` och följ instruktionerna i `pingvakt-server.service`. Servern lyssnar på port 8080 och bör ligga bakom en omvänd proxy med HTTPS (Caddy, Traefik, nginx). Har du redan certifikat kan du i stället använda `--tls-cert` och `--tls-key`.

Första gången du öppnar webbgränssnittet skapar du administratörskontot. Sedan gör du så här:

1. Skapa en **kund**. En första site skapas automatiskt.
2. Klicka **Ny agent**. Du får en token och färdiga installationskommandon:
   - Linux/Raspberry Pi/macOS: `curl -fsSL https://din-server/install.sh | sudo sh -s -- --token pva_…`
   - Windows (PowerShell som admin): `& ([scriptblock]::Create((irm https://din-server/install.ps1))) -Token pva_…`
3. Klicka **Lägg till mål** och välj agenten som ska mäta. Agenten hämtar sina mål från servern inom några sekunder.
4. Skapa **larmregler**, antingen per kund eller för alla kunder. Larmen kan skicka e-post och webhook (Teams, Slack m.fl.) och visa popup och ljud i webbläsaren. *Agent offline* larmar när en agent slutar rapportera.

Skrivbordsappen kopplas in under **Inställningar → Server** med serverns adress och en agent-token. Appens egna mål syns då under kunden. Larmen väljs på servern.

**Roller:** *Administratör* kan göra allt. *Kund* har bara läsbehörighet och ser bara sin egen kund.

**Lagring:** SQLite i datamappen (`/data` i Docker). Varje enskild ping sparas i 7 dagar, och minutvärden (snitt, min, max och förlust) sparas i 400 dagar. Båda tiderna kan ändras under Inställningar. Graferna visar upp till 30 dagar.

| Miljövariabel | Betydelse |
|---|---|
| `PINGVAKT_LISTEN` | Adress, standard `:8080` |
| `PINGVAKT_DATA` | Mapp för databasen |
| `PINGVAKT_AGENTS_DIR` | Mapp med agentfiler som erbjuds för nedladdning |
| `PINGVAKT_TRUST_PROXY=1` | Lita på `X-Forwarded-*` från den omvända proxyn |
| `PINGVAKT_ADMIN_EMAIL` / `PINGVAKT_ADMIN_PASSWORD` | Skapar första administratören vid start |

## Agenten

```
pingvakt-agent enroll --server https://din-server --token pva_…   # registrera
pingvakt-agent install && pingvakt-agent start                   # som tjänst (systemd, Windows-tjänst, launchd)
pingvakt-agent status
```

Konfigurationen sparas i `/etc/pingvakt/agent.json`, `%ProgramData%\PingVakt\agent.json` eller `/Library/Application Support/PingVakt/agent.json`. Agenten sparar senast kända mål lokalt och mäter även när servern inte går att nå. Mätdata buffras i minnet och skickas när kontakten är tillbaka.

## Installera skrivbordsappen

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
scripts/build.sh server     # server för Linux/Windows + agenter för alla plattformar
docker build -t pingvakt-server .
```

**GitHub Actions** (`.github/workflows/build.yml`) bygger allt vid varje push och lägger Docker-avbildningen i `ghcr.io/atomix888/pingvakt-server`. Taggen `:edge` byggs från `main`, och `:<version>` och `:latest` byggs från releaser. När du skapar en release med en tagg, till exempel `v0.2.0`, publiceras alla installationsfiler på releasesidan.

Repot är privat, så Docker-avbildningen är också privat. Logga in med `docker login ghcr.io -u Atomix888` och en personlig åtkomsttoken med behörigheten `read:packages`. Alternativt bygger du själv med `build: ..` i compose-filen.

**Utveckla gränssnittet utan att bygga:** öppna `desktop/frontend/dist/index.html` via en lokal webbserver, till exempel med `python3 -m http.server`. Sidan går då i *demoläge* med simulerad data.

## Struktur

```
core/              Mätmotorn – delas av skrivbordsapp, agent och server
  agentproto/      Protokollet agent ↔ server och klienten som skickar mätdata
  probe/           ICMP (Windows-API / oprivilegierade socketar) och TCP-ping, DNS-cache
  monitor/         Schemaläggning per mål, ringbuffert med historik, statistik och status
  alert/           Larmregler, utvärdering, e-post (SMTP), systemnotiser, loggfil
  trace/           Traceroute via systemets verktyg
  config/          JSON-konfiguration
desktop/           Skrivbordsappen (Wails)
  app.go           Brygga mellan gränssnittet och mätmotorn
  frontend/dist/   Gränssnittet (HTML/CSS/JS + uPlot), inget byggsteg
  build/           Ikon, Windows-installationsprogram (NSIS), .deb-filer, macOS Info.plist
server/            Servern: SQLite, API, inloggning, central larmning, webbgränssnitt (server/web)
agent/             Agenten (tjänst utan gränssnitt)
deploy/            Docker Compose + Caddy, systemd-tjänst
tools/winres/      Skapar Windows-ikon, manifest och versionsinfo
scripts/build.sh   Byggskript för alla plattformar
```

## Färdplan

- PDF-rapporter per kund och period
- Kodsignering (Windows/macOS)
- Engelska som språkval
- HTTP(S)-kontroller som mättyp
- Tvåstegsinloggning och inloggning via Microsoft/Google för kunder

## Licenser

Se `LICENSE-THIRD-PARTY.txt`. PingVakt innehåller Wails och uPlot, som båda har MIT-licens. Funktionerna är inspirerade av vmPing (MIT), men koden är nyskriven.
