package main

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var agentFile = regexp.MustCompile(`^pingvakt-agent-(linux|darwin|windows)-(amd64|arm64|armv7)(\.exe)?$`)

// handleDownload serverar agentprogrammet för installation.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if !agentFile.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(s.agentsDir, name)
	if _, err := os.Stat(p); err != nil {
		http.Error(w, "agentfilen finns inte på servern ("+name+")", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, p)
}

func (s *Server) handleInstallSh(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	_, _ = w.Write([]byte(strings.ReplaceAll(installSh, "@SERVER@", s.publicURL(r))))
}

func (s *Server) handleInstallPs1(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(strings.ReplaceAll(installPs1, "@SERVER@", s.publicURL(r))))
}

const installSh = `#!/bin/sh
# PingVakt-agent – installation för Linux och macOS
# Användning: curl -fsSL @SERVER@/install.sh | sudo sh -s -- --token <TOKEN>
set -eu
SERVER="@SERVER@"
TOKEN=""
INSECURE=""
while [ $# -gt 0 ]; do
  case "$1" in
    --token) TOKEN="$2"; shift 2 ;;
    --insecure) INSECURE="--insecure"; shift ;;
    *) echo "okänt argument: $1"; exit 1 ;;
  esac
done
[ -n "$TOKEN" ] || { echo "ange --token"; exit 1; }
[ "$(id -u)" = 0 ] || { echo "kör med sudo"; exit 1; }
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  armv7l|armv7) ARCH=armv7 ;;
  *) echo "arkitekturen $(uname -m) stöds inte"; exit 1 ;;
esac
BIN=/usr/local/bin/pingvakt-agent
echo "Hämtar agenten för $OS/$ARCH…"
mkdir -p /usr/local/bin
CURLK=""; [ -n "$INSECURE" ] && CURLK="-k"
curl -fsSL $CURLK -o "$BIN.tmp" "$SERVER/download/agent/pingvakt-agent-$OS-$ARCH"
chmod 755 "$BIN.tmp"
"$BIN.tmp" stop >/dev/null 2>&1 || true
mv "$BIN.tmp" "$BIN"
if [ "$OS" = darwin ]; then xattr -d com.apple.quarantine "$BIN" 2>/dev/null || true; fi
"$BIN" enroll --server "$SERVER" --token "$TOKEN" $INSECURE
"$BIN" uninstall >/dev/null 2>&1 || true
"$BIN" install
"$BIN" start
echo "Klart! Agenten körs nu som tjänst och syns i PingVakt inom några sekunder."
`

const installPs1 = `# PingVakt-agent – installation för Windows (kör i PowerShell som administratör)
# Användning: & ([scriptblock]::Create((irm @SERVER@/install.ps1))) -Token <TOKEN>
param([Parameter(Mandatory=$true)][string]$Token, [switch]$Insecure)
$ErrorActionPreference = 'Stop'
$Server = '@SERVER@'
$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Kör PowerShell som administratör.' }
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$dir = Join-Path $env:ProgramFiles 'PingVakt Agent'
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$exe = Join-Path $dir 'pingvakt-agent.exe'
if (Test-Path $exe) { & $exe stop 2>$null | Out-Null; & $exe uninstall 2>$null | Out-Null }
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
if ($Insecure) { [Net.ServicePointManager]::ServerCertificateValidationCallback = { $true } }
Write-Host "Hämtar agenten ($arch)…"
Invoke-WebRequest -UseBasicParsing -Uri "$Server/download/agent/pingvakt-agent-windows-$arch.exe" -OutFile $exe
$enrollArgs = @('enroll', '--server', $Server, '--token', $Token)
if ($Insecure) { $enrollArgs += '--insecure' }
& $exe @enrollArgs
if ($LASTEXITCODE -ne 0) { throw 'Registreringen misslyckades.' }
& $exe install
& $exe start
Write-Host 'Klart! Agenten körs nu som Windows-tjänst och syns i PingVakt inom några sekunder.'
`
