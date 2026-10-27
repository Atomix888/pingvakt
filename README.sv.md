# PingVakt

*[English](README.md) · Svenska*

PingVakt övervakar latens och paketförlust mot många mål samtidigt, på samma sätt som MultiPing och vmPing. Programmet är skrivet i Go och [Wails](https://wails.io) och körs på Windows, macOS och Linux.

PingVakt består av tre delar som använder samma mätmotor:

- **Skrivbordsappen** fungerar fristående, som MultiPing. Den kan också kopplas till en server.
- **Servern** tar emot mätdata från många agenter, delar upp den per kund och site, sparar historik, larmar centralt och har ett webbgränssnitt.
- **Agenten** körs som tjänst utan gränssnitt hos kunden (Windows, Linux, Raspberry Pi eller macOS). Den ansluter utåt till servern, så inga portar behöver öppnas.

## Funktioner

- **Många mål samtidigt** visas som rutor med statusfärg (uppe, varning, nere, pausad). Varje ruta har en minigraf med staplar, och förlorade paket syns som röda staplar.
- **ICMP-ping (IPv4 och IPv6)** och **TCP-portping**. Skriv `värd:port` för TCP.
- **HTTP(S)-kontroller:** skriv en adress som börjar med `https://` (eller välj mättypen HTTP(S)). Svarstiden mäts inklusive uppkoppling och TLS. Statuskod 400 eller högre räknas som fel, och du kan kräva att en viss text finns i svaret. Rutan visar statuskoden och hur många dagar certifikatet är giltigt.
- **DNS-kontroller:** mäter hur lång tid ett namnuppslag tar, mot datorns vanliga DNS eller en DNS-server du anger. Svar som saknas eller NXDOMAIN räknas som fel.
- **Statistik per mål:** senaste värde, snitt, min, max, jitter och förlust i %. I detaljvyn finns dessutom median och 95:e percentil.
- **Detaljgraf** över tid (1 min till 6 h). Grafen visar latens, markerar förlorade paket och har en linje för varningsgränsen. Du kan zooma genom att dra i grafen och återställa med dubbelklick.
- **Larmregler** av samma typ som i MultiPing:
  - *Nere:* X förlorade av de senaste Y paketen.
  - *Hög latens:* X av de senaste Y paketen över N ms. Förlorade paket räknas också.
  - *Paketförlust:* förlusten över de senaste Y paketen överstiger N %.

  När ett larm går kan programmet visa en popup, spela ljud, visa en systemnotis, skicka **e-post via SMTP** och skriva till **loggfil**. Larmen kan upprepas medan de pågår och meddela när de upphör. E-post kan begränsas till högst ett mejl per N minuter, och händelser däremellan samlas i nästa mejl.
- **Traceroute** med knappar som lägger till hoppen som mål.
- **Wi-Fi-survey:** knappen *Wi-Fi* visar alla nät datorn hör, med en kanalgraf per band och tips om störningar. Skriv en plats (t.ex. "Rum 204"), kryssa i *Upprepa var 30:e s* och gå runt på platsen; *Exportera CSV* sparar alla avsökningar med sina platser.
- **Nätverksskanning:** knappen *Skanna nätverk* hittar aktiva enheter i ett nät (t.ex. `192.168.1.0/24`). Den visar IP-adress, namn (DNS/NetBIOS), MAC-adress, tillverkare, trolig typ (router, skrivare, NAS, kamera, accesspunkt, Windows-dator …) och öppna portar. Markera enheterna och lägg till dem som mål med en knapp.
- **Engelska och svenska:** engelska är standard. Språket väljs under Inställningar → Utseende.
- **CSV-export** av mätdata. På svenska är filen semikolonseparerad med decimalkomma och öppnas direkt i svenskt Excel.
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

**CPU, minne och disk:** agenterna rapporterar datorns CPU-belastning, minnesanvändning och hur fulla diskarna är. Värdena visas på agenten under kunden och i agentlistan. Klickar du på en agent får du grafer från 1 timme upp till 30 dagar. Servern visar också sina egna värden på översikten. Det finns tre larmtyper för detta:
- *CPU hög:* över X % i N minuter i sträck.
- *Minne högt:* över X % i N minuter i sträck.
- *Disk full:* någon volym är mer än X % full.

Regler som gäller alla kunder kontrollerar även själva servern. Värdena sparas som minutvärden lika länge som ping-historiken (400 dagar som standard).

Skrivbordsappen kopplas in under **Inställningar → Server** med serverns adress och en agent-token. Appens egna mål syns då under kunden. Larmen väljs på servern.

**Larmhantering:**
- *Kvittera* ett aktivt larm under Larm, eller direkt i popupen. Det upprepas då inte, och kvitteringen tas bort när larmet upphör.
- *Servicefönster* lägger du in via kundens meny (⋯ → Servicefönster). De kan gälla en gång eller varje vecka, och omfatta hela kunden, en site, en agent eller ett mål. Under ett servicefönster skickas inga larm, men händelserna sparas.
- *Beror på* sätts per mål, till exempel att accesspunkterna ligger bakom routern. När routern är nere kommer ett larm för routern, inte ett för varje mål bakom den.

**Wi-Fi:** agenter och skrivbordsappar som är anslutna via Wi-Fi rapporterar signalstyrka (RSSI), brus, SNR, kanal, åtkomstpunkt (BSSID) och länkhastighet. Klicka på en agent för att se en graf över signalen där varje roaming (byte av åtkomstpunkt) är markerad, en lista över roamingar och hur länge datorn varit ansluten till varje åtkomstpunkt. Under Agenter finns en Wi-Fi-kolumn med aktuell signal. Värdena hämtas med `iw` på Linux, `netsh` på Windows och `wdutil`/`system_profiler` på macOS (brus finns inte på Windows).

**Traceroute och vägbevakning:** i målets detaljvy finns knappen *Traceroute*. Den körs från agenten som mäter målet, och svaret visas efter några sekunder. Under *Redigera → Vägbevakning* kan agenten köra traceroute var 5:e, 15:e, 30:e eller 60:e minut. När en ny väg syns två gånger i rad räknas det som en vägändring. Hoppen som är nya markeras, och regeln *Vägen ändras* larmar.

**Nätverksskanning från agenten:** i agentens meny (⋯ → *Skanna nätverk*) eller via *Lägg till mål*. Skanningen körs av agenten hos kunden, så du ser vad som finns i kundens nät på distans. Agenten skickar vilka nät den sitter på, och du kan också ange ett eget nät eller intervall (högst /20).

**Push, SMS och Teams:** en larmregel kan skicka
- *push till mobiler* – till alla som slagit på aviseringar i webbappen (kontomenyn → Aviseringar; fungerar på Android och, efter "Lägg till på hemskärmen", på iPhone), till användarnas egna ntfy-ämnen eller Pushover-nycklar, och till ett gemensamt ntfy-ämne/Pushover-nyckel från Inställningar;
- *SMS* via 46elks eller Twilio (högst ett SMS per larm och mål var N:e minut, plus ett när det upphör);
- *Teams*-kort via ett Teams-arbetsflöde ("Publicera i en kanal när en webhook-begäran tas emot").

**Jourscheman:** under Larm → *+ Jourschema* anger du vilka som turas om (varje dag, vecka eller varannan vecka) och lägger in ersättare vid semester. En larmregel kan meddela *jour*, och den som har jour får e-post, SMS och/eller push. Telefonnummer anges per användare.

**Inloggning:** under Inställningar → Inloggning kan du kräva tvåstegsinloggning för administratörer eller alla – den som saknar det måste slå på det vid nästa inloggning. Du kan också tillåta inloggning med Microsoft (Entra ID) och Google: registrera en app med omdirigerings-URI:n som visas i Inställningar. Bara befintliga användare kan logga in, kopplat via e-postadressen, och inloggning via Microsoft/Google räknas som tvåsteg.

**SNMP:** switchar, routrar, brandväggar och servrar kan läsas av med SNMP v1, v2c eller v3 (MD5/SHA/SHA-256/SHA-512, DES/AES). Agenten på platsen gör avläsningen, så inget behöver öppnas mot servern. Lägg till en enhet från kundens meny, platsens meny eller agentens meny (*Lägg till SNMP-enhet…*) och klicka på *Testa* för att kontrollera inställningarna. Enheten visas som en bricka under platsen. Klicka på den för att se drifttid, CPU, minne och alla portar med status, hastighet, trafik in/ut, last i % och fel per minut, samt en trafikgraf per port (1 timme till 30 dagar). Bocka i *Bevaka* på de portar som är viktiga. Fyra larmtyper använder detta:
- *SNMP-enhet svarar inte* under N avläsningar i rad.
- *Bevakad port nere* – en påslagen, bevakad port går ner.
- *Hög porttrafik* – över X % av porthastigheten i N minuter.
- *Portfel* – fler än N fel per minut.

**Hastighetstester:** i agentens vy (CPU, minne och disk) kan du köra ett hastighetstest mot Cloudflare eller mot PingVakt-servern, och schemalägga det varje timme, var 4:e timme, två gånger om dagen eller en gång om dagen. Resultaten visas som en graf, och regeln *Hastighetstest långsamt* larmar när nedladdningen är under N Mbit/s i X tester i rad. Ett test använder ungefär 50–500 MB beroende på linan, så kör det inte för ofta på anslutningar med datatak.

**Mallar för mål:** *Från mall…* på kundsidan lägger till en färdig uppsättning mål på en agent: *Grundläggande nätverk* (gateway, DNS-server, DNS-uppslag, internet), *Microsoft 365*, *Google Workspace* och *Internetkvalitet*. `{gateway}`, `{dns1}` och `{dns2}` ersätts med de värden agenten rapporterar för sitt nät, och övriga mål sätts att ligga bakom gatewayen. Gör egna mallar under Inställningar → Mallar för mål, eller välj *Spara mål som mall* i agentens meny.

**Wi-Fi-survey:** i agentens vy (CPU, minne och disk) under *Wi-Fi-nät i närheten* söker agenten av luften och visar alla nät den hör: SSID, BSSID, tillverkare, band, kanal, kanalbredd, signal, säkerhet och standard. En kanalgraf per band (2,4, 5 och 6 GHz) visar hur näten överlappar, och PingVakt pekar ut vanliga problem: samkanalsstörning på den egna kanalen, nät i 2,4 GHz utanför kanal 1, 6 och 11, 40 MHz i 2,4 GHz samt öppna nät och WEP. Sök av direkt eller var 5:e, 15:e eller 60:e minut; tidigare avsökningar från den senaste veckan kan öppnas igen. Linux använder `iw` (eller `nmcli`), Windows `netsh` (Windows 11 kan kräva platsåtkomst) och macOS `system_profiler` (utan BSSID i nyare macOS).

**Larm för roaming:**
- *Klistrig Wi-Fi-klient* – datorn sitter kvar på en åtkomstpunkt under N dBm fast en annan åtkomstpunkt med samma SSID är X dB starkare, i Y minuter. Kräver automatisk Wi-Fi-avsökning på agenten.
- *För många roamingar* – fler än N roamingar på X minuter, vilket ofta tyder på överlappande celler eller en klient som roamar för ivrigt.

**UniFi, Meraki och Aruba Central:** under kundmenyn → *Lägg till integration* kopplar du kundens Ubiquiti UniFi-kontroller (API-nyckel eller lokalt konto), Cisco Meraki-organisation (API-nyckel) eller HPE Aruba Central-konto (API-klient med token, som förnyas automatiskt). Servern läser av status för alla åtkomstpunkter, switchar och gateways var 1–15:e minut. Enheterna visas under kunden med status, modell, IP, klienter, drifttid och firmware, och kan läggas till som pingmål med ett klick. Regeln *UniFi/Meraki/Aruba-enhet offline* larmar när en enhet varit offline i N avläsningar – för alla enheter, bara åtkomstpunkter eller allt utom åtkomstpunkter. Servern måste kunna nå UniFi-kontrollern; Meraki och Aruba Central är molntjänster.

**Nätverkskarta:** *Nätverkskarta* på kundsidan ritar hur nätet hänger ihop, utifrån LLDP- och CDP-grannar på SNMP-enheterna (läses var 10:e minut) plus enheterna från UniFi, Meraki och Aruba. Varje enhet visar sin status, länkarna visar portarna i båda ändar och länkar på portar som är nere ritas röda. Grannar som inte övervakas (t.ex. åtkomstpunkter) ritas streckade; de får status när de också är pingmål eller kommer från en integration. Dra för att flytta, scrolla för att zooma och klicka på en enhet för att öppna den.

**Syslog och SNMP-traps:** i agentens vy (CPU, minne och disk → *Syslog och SNMP-traps*) kan agenten ta emot syslog på UDP 514 och SNMP-traps (v1/v2c) på UDP 162 från enheterna på platsen och skicka dem vidare till servern. Meddelandena visas under **Loggar** med filter för kund, allvarlighet, typ och text, och sparas i 30 dagar. Regeln *Syslog eller SNMP-trap* larmar på meddelanden som innehåller en text (eller matchar ett `/reguljärt uttryck/`) med en viss allvarlighet eller värre, och släcks efter N minuter utan nya träffar. Servern kan också ta emot direkt: starta den med `--syslog :5514 --traps :1162` (eller `PINGVAKT_SYSLOG`/`PINGVAKT_TRAPS`) och publicera portarna, t.ex. `514:5514/udp` och `162:1162/udp` i Docker. Meddelandena kopplas då till en kund via avsändarens IP-adress (SNMP-enhet, pingmål eller integrationsenhet).

**Ovanlig svarstid (dynamiska tröskelvärden):** i stället för en fast ms-gräns lär sig regeln *Ovanlig svarstid* vad som är normalt för varje mål och timme på dygnet från de senaste två veckorna, och larmar när svarstiden de senaste N minuterna ligger klart över det normala: mer än K × den normala spridningen, minst X ms och minst 25 %. Den börjar efter 5 dagars data. Målets detaljvy visar *Normalt nu* (t.ex. "12 ms ±2").

**Ärendesystem:** under Inställningar → Ärendesystem kopplar du Jira Cloud, Freshdesk eller Zendesk. Larmregler med *Skapa ärende* öppnar ett ärende när larmet går (med kund, mål, detaljer och länk) och lägger en kommentar och stänger det när larmet upphör. Aktiva larm länkar till sitt ärende. *Skapa testärende* kontrollerar anslutningen.

**Kundportal:** kundanvändare (rollen *Kund*) kommer nu till en portal med samlad status, pågående störningar, planerat underhåll, tillgänglighet per tjänst för 24 timmar och 30 dagar, PDF-rapporter att ladda ner och de senaste händelserna. De kan kryssa i *Mejla mig när något går ner eller kommer tillbaka* (administratörer får det för alla kunder). Förhandsvisa från kundmenyn → *Kundportal*.

**Säkerhetskopior utanför servern:** under Inställningar → Säkerhetskopior kan den nattliga kopian också laddas upp till S3-kompatibel lagring (AWS S3, Backblaze B2, Wasabi, MinIO …) eller WebDAV (Nextcloud, ownCloud), där de N senaste kopiorna sparas. *Testa* laddar upp och tar bort en liten fil; *Ladda upp en säkerhetskopia nu* kör direkt.

**Installera som app:** webbgränssnittet kan läggas till på mobilens hemskärm och fungerar då som en app, med push-aviseringar.

**Språk:** engelska är standard. Under Inställningar väljer du serverns standardspråk. Varje användare kan välja eget språk i kontomenyn (⋯ → Language / Språk), och varje kund kan ha eget språk för rapporter och statussida (Redigera kund).

**Nya larmtyper:** *Agentens Wi-Fi-signal svag* (under N dBm i X minuter), *HTTPS-certifikat går ut* (inom N dagar) och *Vägen (traceroute) ändras*.

**Automatisk uppdatering:** när servern uppdateras hämtar agenterna (från version 0.4.0) den nya versionen själva. Agenten kontrollerar filens kontrollsumma och startar om sig själv. Det kan stängas av under Inställningar.

**Säkerhetskopior:** servern sparar en kopia av databasen varje natt kl. 03:15 i `data/backups`. Som standard sparas de sju senaste. Under Inställningar kan du ladda ner en färsk kopia när som helst. Du återställer genom att stoppa servern och ersätta `pingvakt.db` med kopian.

**Rapporter:** under kunden → *Rapport* skapar du en PDF för förra månaden, förra veckan eller valfri period. Den innehåller:
- tillgänglighet och nertid per mål
- paketförlust och latens
- grafer per dygn
- agenternas CPU, minne och disk
- SNMP-enheter (nåbarhet, CPU, minne) och de mest belastade portarna
- hastighetstester och Wi-Fi-kvalitet (signal, tid under −70 dBm, roamingar)
- UniFi/Meraki/Aruba-enheter och deras avbrott
- servicefönster och larmhändelser

Rapporten kan mejlas automatiskt varje vecka (måndag morgon) eller varje månad (den 1:a). Företagsnamnet på rapporterna ställer du in under Inställningar.

**Publik statussida:** under kundens meny → *Publik statussida* skapar du en hemlig länk som kunden kan öppna utan inloggning. Den visar status och tillgänglighet (24 h och 30 d) per mål, men inga IP-adresser eller detaljer.

**TV-läge:** en helskärmsvy med alla kunder och det som är fel just nu. Den passar för en skärm på kontoret.

**Tvåstegsinloggning:** varje användare kan aktivera tvåstegsinloggning under kontomenyn längst ner till vänster. Det fungerar med Microsoft Authenticator, Google Authenticator, 1Password, Bitwarden m.fl. Användaren får också reservkoder. En administratör kan återställa tvåstegsinloggningen för någon som tappat telefonen.

**Ändringslogg:** inloggningar och alla ändringar (kunder, agenter, mål, larm, användare, inställningar) loggas med användare, tid och IP-adress.

**Roller:** *Administratör* kan göra allt. *Kund* har bara läsbehörighet och ser bara sin egen kund.

**Lagring:** SQLite i datamappen (`/data` i Docker). Varje enskild ping sparas i 7 dagar, och minutvärden (snitt, min, max och förlust) sparas i 400 dagar. Båda tiderna kan ändras under Inställningar. Graferna visar upp till 30 dagar.

| Miljövariabel | Betydelse |
|---|---|
| `PINGVAKT_LISTEN` | Adress, standard `:8080` |
| `PINGVAKT_DATA` | Mapp för databasen |
| `PINGVAKT_AGENTS_DIR` | Mapp med agentfiler som erbjuds för nedladdning |
| `PINGVAKT_TRUST_PROXY=1` | Lita på `X-Forwarded-*` från den omvända proxyn |
| `PINGVAKT_ADMIN_EMAIL` / `PINGVAKT_ADMIN_PASSWORD` | Skapar första administratören vid start |
| `PINGVAKT_SYSLOG` | Ta emot syslog på den här UDP-adressen, t.ex. `:5514` (valfritt) |
| `PINGVAKT_TRAPS` | Ta emot SNMP-traps på den här UDP-adressen, t.ex. `:1162` (valfritt) |

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
  probe/           ICMP (Windows-API / oprivilegierade socketar), TCP-ping, HTTP(S)- och DNS-kontroller
  wifi/            Wi-Fi-avläsning (iw, netsh, wdutil, system_profiler)
  monitor/         Schemaläggning per mål, ringbuffert med historik, statistik och status
  alert/           Larmregler, utvärdering, e-post (SMTP), systemnotiser, loggfil
  trace/           Traceroute via systemets verktyg
  netscan/         Nätverksskanning (ping, portar, ARP, tillverkare, NetBIOS), gateway och DNS
  snmp/            SNMP-avläsning (system, portar, CPU, minne) – körs i agenten
  speedtest/       Hastighetstest mot Cloudflare eller servern
  vendors/         Klienter för UniFi, Meraki och Aruba Central
  logrx/           Mottagare för syslog och SNMP-traps
  offsite/         Säkerhetskopior utanför servern (S3 Signature V4, WebDAV)
  tickets/         Ärenden i Jira, Freshdesk och Zendesk
  i18n/            Översättningar (sv.json: engelska → svenska)
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

- Kodsignering: förberett i bygget – se [docs/code-signing.md](docs/code-signing.md) för vilka certifikat som behövs och vilka hemligheter som ska läggas in

## Licenser

Se `LICENSE-THIRD-PARTY.txt`. PingVakt innehåller Wails och uPlot, som båda har MIT-licens. Funktionerna är inspirerade av vmPing (MIT), men koden är nyskriven.
