# PingVakt

*English · [Svenska](README.sv.md)*

PingVakt monitors latency and packet loss to many targets at once, like MultiPing and vmPing. It is written in Go and [Wails](https://wails.io) and runs on Windows, macOS and Linux. The user interface is available in English and Swedish.

PingVakt has three parts that share the same measurement engine:

- **The desktop app** works on its own, like MultiPing. It can also be connected to a server.
- **The server** receives measurements from many agents, organises them per customer and site, stores history, raises alerts centrally and has a web interface.
- **The agent** runs as a headless service at the customer (Windows, Linux, Raspberry Pi or macOS). It connects outbound to the server, so no ports need to be opened.

## Features

- **Many targets at once**, shown as tiles with a status colour (up, warning, down, paused). Each tile has a small bar graph, and lost packets show as red bars.
- **ICMP ping (IPv4 and IPv6)** and **TCP port ping**. Write `host:port` for TCP.
- **HTTP(S) checks:** enter an address starting with `https://` (or choose the HTTP(S) check type). The response time includes connecting and TLS. Status code 400 or higher counts as an error, and you can require a certain text in the response. The tile shows the status code and how many days the certificate is valid.
- **DNS checks:** measure how long a name lookup takes, against the computer's normal DNS or a DNS server you choose. Missing answers and NXDOMAIN count as errors.
- **Statistics per target:** last value, average, min, max, jitter and loss in %. The detail view also shows median and 95th percentile.
- **Detail graph** over time (1 min to 6 h) with latency, lost packets and a line for the warning threshold. Drag to zoom, double-click to reset.
- **Alert rules** of the same kind as in MultiPing:
  - *Down:* X lost of the last Y packets.
  - *High latency:* X of the last Y packets over N ms. Lost packets also count.
  - *Packet loss:* loss over the last Y packets exceeds N %.

  When an alert fires the app can show a popup, play a sound, show a system notification, send **email via SMTP** and write to a **log file**. Alerts can repeat while active and notify when they clear. Email can be limited to one message per N minutes; events in between are collected in the next email.
- **Traceroute** with buttons that add the hops as targets.
- **Network scan:** the *Scan network* button finds active devices in a network (e.g. `192.168.1.0/24`). It shows IP address, name (DNS/NetBIOS), MAC address, manufacturer, likely type (router, printer, NAS, camera, access point, Windows computer …) and open ports. Select the devices and add them as targets with one click.
- **CSV export** of measurements. In Swedish the file uses semicolons and decimal commas so it opens directly in Swedish Excel.
- Custom order with drag and drop, filtering, sorting, number of columns, compact mode and dark/light theme.
- **English and Swedish:** English is the default. Choose the language under Settings → Appearance.
- Settings are stored in:
  - Windows: `%APPDATA%\PingVakt\config.json`
  - macOS: `~/Library/Application Support/PingVakt/config.json`
  - Linux: `~/.config/PingVakt/config.json`

## The server

**Quick start with Docker** (`deploy/docker-compose.yml` includes Caddy, which handles HTTPS automatically):

```bash
cd deploy
# change the domain in Caddyfile and point DNS at the server
docker compose up -d
```

Without Docker: unpack `pingvakt-server-<ver>-linux-amd64.tar.gz` and follow the instructions in `pingvakt-server.service`. The server listens on port 8080 and should sit behind a reverse proxy with HTTPS (Caddy, Traefik, nginx). If you already have certificates you can use `--tls-cert` and `--tls-key` instead.

The first time you open the web interface you create the administrator account. Then:

1. Create a **customer**. A first site is created automatically.
2. Click **New agent**. You get a token and ready-made install commands:
   - Linux/Raspberry Pi/macOS: `curl -fsSL https://your-server/install.sh | sudo sh -s -- --token pva_…`
   - Windows (PowerShell as admin): `& ([scriptblock]::Create((irm https://your-server/install.ps1))) -Token pva_…`
3. Click **Add targets** and choose the agent that should measure. The agent fetches its targets from the server within seconds. Use **Find devices with a network scan…** to discover what is on the customer's network.
4. Create **alert rules**, per customer or for all customers. Alerts can send email and webhooks (Teams, Slack etc.) and show popups and sounds in the browser. *Agent offline* fires when an agent stops reporting.

**CPU, memory and disk:** agents report CPU load, memory use and how full the disks are. The values are shown on the agent under the customer and in the agent list. Click an agent for graphs from 1 hour to 30 days. The server also shows its own values on the overview. Three alert types use this:
- *CPU high:* over X % for N minutes in a row.
- *Memory high:* over X % for N minutes in a row.
- *Disk full:* any volume more than X % full.

Rules for all customers also check the server itself. The values are stored as minute values as long as the ping history (400 days by default).

The desktop app connects under **Settings → Server** with the server address and an agent token. The app's own targets then appear under the customer. Alerts are chosen on the server.

**Alert handling:**
- *Acknowledge* an active alert under Alerts, or directly in the popup. It is then not repeated, and the acknowledgement is removed when the alert clears.
- *Maintenance windows* are added from the customer menu (⋯ → Maintenance windows). They can be one-off or weekly and cover the whole customer, a site, an agent or a target. No alerts are sent during a maintenance window, but events are still recorded.
- *Depends on* is set per target, e.g. that the access points sit behind the router. When the router is down you get one alert for the router instead of one per target behind it.

**Wi-Fi:** agents and desktop apps connected via Wi-Fi report signal strength (RSSI), noise, SNR, channel, access point (BSSID) and link speed. Click an agent to see a signal graph where every roam (change of access point) is marked, a list of roams and how long the computer was connected to each access point. The Agents page has a Wi-Fi column with the current signal. Values come from `iw` on Linux, `netsh` on Windows and `wdutil`/`system_profiler` on macOS (noise is not available on Windows).

**Traceroute and path monitoring:** the target's detail view has a *Traceroute* button. It runs from the agent measuring the target, and the result appears after a few seconds. Under *Edit → Path monitoring* the agent can run traceroute every 5, 15, 30 or 60 minutes. When a new path is seen twice in a row it counts as a path change. New hops are highlighted, and the *Path changes* rule fires.

**Network scan from the agent:** from the agent menu (⋯ → *Scan network*) or via *Add targets*. The scan runs on the agent at the customer, so you can see what is on the customer's network remotely. The agent reports which networks it is on, and you can also enter your own network or range (at most /20).

**More alert types:** *Agent Wi-Fi signal weak* (below N dBm for X minutes), *HTTPS certificate expiring* (within N days) and *Path (traceroute) changes*.

**Push, SMS and Teams:** an alert rule can send
- *push to phones* – to everyone who turned on notifications in the web app (account menu → Notifications; works on Android and, after "Add to Home Screen", on iPhone), to users' own ntfy topics or Pushover keys, and to a shared ntfy topic/Pushover key from Settings;
- *SMS* via 46elks or Twilio (at most one SMS per alert and target every N minutes, plus one when it clears);
- *Teams* cards via a Teams workflow ("Post to a channel when a webhook request is received").

**On-call schedules:** under Alerts → *+ On-call schedule* you set who takes turns (daily, weekly or every two weeks) and add replacements for holidays. An alert rule can notify *on call*, and the person on call gets email, SMS and/or push. Phone numbers are set per user.

**Sign-in:** under Settings → Sign-in you can require two-factor authentication for administrators or everyone – users without it must turn it on at their next sign-in. You can also allow sign-in with Microsoft (Entra ID) and Google: register an app with the redirect URI shown in Settings. Only existing users can sign in, matched on email address, and sign-in through Microsoft/Google counts as two-factor.

**SNMP:** switches, routers, firewalls and servers can be read over SNMP v1, v2c or v3 (MD5/SHA/SHA-256/SHA-512, DES/AES). The agent at the site does the polling, so nothing has to be opened towards the server. Add a device from the customer menu, the site menu or the agent menu (*Add SNMP device…*) and click *Test* to check the settings. The device shows up as a chip under the site. Click it to see uptime, CPU, memory and every port with status, speed, traffic in/out, load in % and errors per minute, plus a traffic graph per port (1 hour to 30 days). Tick *Watch* on the ports that matter. Four alert types use this:
- *SNMP device not responding* for N polls in a row.
- *Watched port down* – an enabled, watched port goes down.
- *Port traffic high* – over X % of the port speed for N minutes.
- *Port errors* – more than N errors per minute.

**Speed tests:** in the agent view (CPU, memory and disk) you can run a speed test against Cloudflare or against the PingVakt server, and schedule it every hour, every 4 hours, twice a day or once a day. Results are shown as a graph, and the *Speed test slow* rule fires when the download speed is below N Mbit/s in X tests in a row. A test uses roughly 50–500 MB depending on the line, so do not run it too often on metered connections.

**Target templates:** *From template…* on the customer page adds a ready-made set of targets to an agent: *Basic network* (gateway, DNS server, DNS lookup, internet), *Microsoft 365*, *Google Workspace* and *Internet quality*. `{gateway}`, `{dns1}` and `{dns2}` are replaced with the values the agent reports for its network, and the other targets are set to depend on the gateway. Make your own templates under Settings → Target templates, or choose *Save targets as template* in the agent menu.

**Install as an app:** the web interface can be added to the phone's home screen and then works like an app, with push notifications.

**Language:** English is the default. Under Settings you choose the server's default language. Each user can choose their own language in the account menu (⋯ → Language / Språk), and each customer can have its own language for reports and the status page (Edit customer).

**Automatic updates:** when the server is updated, agents (from version 0.4.0) download the new version themselves. The agent verifies the checksum and restarts itself. This can be turned off under Settings.

**Backups:** the server saves a copy of the database every night at 03:15 in `data/backups`. By default the seven latest are kept. Under Settings you can download a fresh copy at any time. Restore by stopping the server and replacing `pingvakt.db` with the copy.

**Reports:** under the customer → *Report* you create a PDF for last month, last week or any period. It contains:
- availability and downtime per target
- packet loss and latency
- daily graphs
- the agents' CPU, memory and disk
- maintenance windows and alert events

The report can be emailed automatically every week (Monday morning) or every month (on the 1st). The company name on the reports is set under Settings.

**Public status page:** under the customer menu → *Public status page* you create a secret link the customer can open without signing in. It shows status and availability (24 h and 30 d) per target, but no IP addresses or details.

**TV mode:** a full-screen view of all customers and everything that is wrong right now, suitable for a screen in the office.

**Two-factor authentication:** each user can enable two-factor authentication in the account menu at the bottom left. It works with Microsoft Authenticator, Google Authenticator, 1Password, Bitwarden and others. The user also gets backup codes. An administrator can reset two-factor authentication for someone who lost their phone.

**Audit log:** sign-ins and all changes (customers, agents, targets, alerts, users, settings) are logged with user, time and IP address.

**Roles:** *Administrator* can do everything. *Customer* is read-only and only sees their own customer.

**Storage:** SQLite in the data folder (`/data` in Docker). Every single ping is kept for 7 days, and minute values (avg, min, max and loss) for 400 days. Both can be changed under Settings. Graphs show up to 30 days.

| Environment variable | Meaning |
|---|---|
| `PINGVAKT_LISTEN` | Address, default `:8080` |
| `PINGVAKT_DATA` | Folder for the database |
| `PINGVAKT_AGENTS_DIR` | Folder with agent files offered for download |
| `PINGVAKT_TRUST_PROXY=1` | Trust `X-Forwarded-*` from the reverse proxy |
| `PINGVAKT_ADMIN_EMAIL` / `PINGVAKT_ADMIN_PASSWORD` | Creates the first administrator at startup |

## The agent

```
pingvakt-agent enroll --server https://your-server --token pva_…   # enroll
pingvakt-agent install && pingvakt-agent start                     # as a service (systemd, Windows service, launchd)
pingvakt-agent status
```

The configuration is stored in `/etc/pingvakt/agent.json`, `%ProgramData%\PingVakt\agent.json` or `/Library/Application Support/PingVakt/agent.json`. The agent keeps its last known targets locally and keeps measuring when the server is unreachable. Measurements are buffered in memory and sent when contact is restored.

## Installing the desktop app

| Platform | File |
|---|---|
| Windows 10/11 (x64) | `PingVakt-<ver>-windows-x64-setup.exe` (installer) or `…-portable.zip` |
| Debian/Ubuntu | `pingvakt_<ver>_amd64.deb` → `sudo apt install ./pingvakt_<ver>_amd64.deb` |
| Other Linux | `pingvakt-<ver>-linux-amd64.tar.gz` (requires GTK 3 and WebKitGTK 4.1) |
| macOS 11+ (Intel and Apple Silicon) | `PingVakt-<ver>-macos.dmg`, built in GitHub Actions |

The app is not code-signed, so Windows SmartScreen ("Run anyway") and macOS Gatekeeper (right-click the app → Open) warn the first time.

### ICMP permissions

- **Windows:** uses the system ICMP API (same as `ping.exe`) and does not require administrator rights.
- **macOS:** uses unprivileged ICMP sockets and needs no special rights.
- **Linux:** uses unprivileged ICMP sockets if `net.ipv4.ping_group_range` allows it, which is the default on Ubuntu and Fedora. The .deb package also gives the app `cap_net_raw` as a fallback. If ping still fails, run:
  `sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"`

Traceroute uses the system tool: `tracert` on Windows and `traceroute` on macOS/Linux. On Linux the `traceroute` package must be installed.

## Building

Requirements: Go 1.22+.
- **Linux:** `libgtk-3-dev` and `libwebkit2gtk-4.1-dev`.
- **Windows installer:** `nsis` and `imagemagick` (built from Linux).
- **macOS:** Xcode Command Line Tools.

```bash
go mod tidy                 # first time
go run ./tools/genoui       # full MAC manufacturer list from IEEE (optional)
go test ./core/...          # tests for the measurement engine
scripts/build.sh linux      # binary + .deb + .tar.gz
scripts/build.sh windows    # PingVakt.exe + installer + portable zip (cross-compiled from Linux)
scripts/build.sh macos      # PingVakt.app + .dmg (on a Mac)
scripts/build.sh server     # server for Linux/Windows + agents for all platforms
docker build -t pingvakt-server .
```

**GitHub Actions** (`.github/workflows/build.yml`) builds everything on every push and publishes the Docker image to `ghcr.io/atomix888/pingvakt-server`. The `:edge` tag is built from `main`, and `:<version>` and `:latest` from releases. When you create a release with a tag such as `v0.7.0`, all installation files are published on the release page.

The repository is private, so the Docker image is private too. Sign in with `docker login ghcr.io -u Atomix888` and a personal access token with the `read:packages` scope, or build it yourself with `build: ..` in the compose file.

**Developing the interface without building:** open `desktop/frontend/dist/index.html` through a local web server, e.g. `python3 -m http.server`. The page then runs in *demo mode* with simulated data.

**Translations:** the source texts in the code are English. `core/i18n/sv.json` maps English to Swedish and is used by the Go code, the web interface and the desktop app.

## Structure

```
core/              The measurement engine – shared by desktop app, agent and server
  agentproto/      Agent ↔ server protocol and the client that sends measurements
  probe/           ICMP (Windows API / unprivileged sockets), TCP ping, HTTP(S) and DNS checks
  wifi/            Wi-Fi readings (iw, netsh, wdutil, system_profiler)
  netscan/         Network scan (ping, ports, ARP, manufacturer, NetBIOS), gateway and DNS
  snmp/            SNMP polling (system, ports, CPU, memory) – runs in the agent
  speedtest/       Speed test against Cloudflare or the server
  monitor/         Per-target scheduling, history ring buffer, statistics and status
  alert/           Alert rules, evaluation, email (SMTP), system notifications, log file
  trace/           Traceroute via the system tool
  i18n/            Translations (sv.json: English → Swedish)
  config/          JSON configuration
desktop/           The desktop app (Wails)
  app.go           Bridge between the interface and the engine
  frontend/dist/   The interface (HTML/CSS/JS + uPlot), no build step
  build/           Icon, Windows installer (NSIS), .deb files, macOS Info.plist
server/            The server: SQLite, API, sign-in, central alerting, web interface (server/web)
agent/             The agent (headless service)
deploy/            Docker Compose + Caddy, systemd service
tools/             winres (Windows icon/manifest), genoui (MAC manufacturer list)
scripts/build.sh   Build script for all platforms
```

## Roadmap

- Wi-Fi survey (neighbouring networks and channels), roaming alerts
- UniFi, Meraki and Aruba Central integrations
- Code signing: prepared in the build – see [docs/code-signing.md](docs/code-signing.md) for the certificates to buy and the secrets to add

## Licences

See `LICENSE-THIRD-PARTY.txt`. PingVakt includes Wails and uPlot, both MIT licensed. The features are inspired by vmPing (MIT), but the code is written from scratch. The MAC manufacturer list comes from the IEEE registration authority.
