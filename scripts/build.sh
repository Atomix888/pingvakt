#!/usr/bin/env bash
# Bygger PingVakt. Användning:
#   scripts/build.sh windows   – PingVakt.exe + installationsprogram (kräver makensis och ImageMagick för ikonen)
#   scripts/build.sh linux     – binär, .deb och .tar.gz (kräver libgtk-3-dev och libwebkit2gtk-4.1-dev)
#   scripts/build.sh macos     – PingVakt.app och .dmg (måste köras på en Mac)
#   scripts/build.sh agents    – agenten för alla plattformar (dist/agents/)
#   scripts/build.sh server    – servern för Linux/Windows + agenter, paketerat som .tar.gz/.zip
#   scripts/build.sh all       – windows + linux + server (från Linux)
set -Eeuo pipefail
# I GitHub Actions: visa felraden som annotering så att felet syns i sammanfattningen
trap 'echo "::error file=scripts/build.sh,line=$LINENO::Kommandot misslyckades: $BASH_COMMAND"' ERR
cd "$(dirname "$0")/.."
VERSION="${VERSION:-$(cat VERSION)}"
DIST="dist"
mkdir -p "$DIST"

# Kör ett kommando; om det misslyckas visas de sista raderna som en annotering i GitHub Actions.
logged() {
  local out; out="$(mktemp)"
  if "$@" >"$out" 2>&1; then cat "$out"; rm -f "$out"; return 0; fi
  cat "$out"
  if [ -n "${GITHUB_ACTIONS:-}" ]; then
    local msg; msg="$(tail -n 25 "$out" | sed -e 's/%/%25/g' | awk '{printf "%s%%0A", $0}')"
    echo "::error title=Misslyckades: $1::${msg}"
  fi
  rm -f "$out"; return 1
}
LDFLAGS="-s -w -X main.Version=${VERSION}"

windows() {
  echo "==> Windows ${VERSION}"
  go run ./tools/winres -version "$VERSION"
  CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -tags desktop,production \
    -ldflags "${LDFLAGS} -H windowsgui" -o "$DIST/PingVakt.exe" ./desktop
  local w=desktop/build/windows
  if [ ! -f "$w/icon.ico" ]; then
    convert desktop/build/appicon.png -define icon:auto-resize=256,128,64,48,32,16 "$w/icon.ico"
  fi
  cp LICENSE-THIRD-PARTY.txt "$w/license.txt"
  if command -v makensis >/dev/null; then
    makensis -V2 -DVERSION="$VERSION" -DEXE="$PWD/$DIST/PingVakt.exe" \
      -DOUT="$PWD/$DIST/PingVakt-${VERSION}-windows-x64-setup.exe" "$w/installer.nsi"
  else
    echo "makensis saknas – hoppar över installationsprogrammet"
  fi
  (cd "$DIST" && rm -f "PingVakt-${VERSION}-windows-x64-portable.zip" && zip -q -j "PingVakt-${VERSION}-windows-x64-portable.zip" PingVakt.exe ../LICENSE-THIRD-PARTY.txt)
}

linux() {
  echo "==> Linux ${VERSION}"
  local arch; arch="$(go env GOARCH)"
  local tags="desktop,production,webkit2_41"
  pkg-config --exists webkit2gtk-4.1 || tags="desktop,production"
  go build -trimpath -tags "$tags" -ldflags "$LDFLAGS" -o "$DIST/pingvakt" ./desktop
  # .deb
  local root; root="$(mktemp -d)"
  install -Dm755 "$DIST/pingvakt" "$root/usr/bin/pingvakt"
  install -Dm644 desktop/build/linux/pingvakt.desktop "$root/usr/share/applications/pingvakt.desktop"
  install -Dm644 desktop/build/appicon.png "$root/usr/share/icons/hicolor/512x512/apps/pingvakt.png"
  install -Dm644 LICENSE-THIRD-PARTY.txt "$root/usr/share/doc/pingvakt/copyright"
  mkdir -p "$root/DEBIAN"
  local debarch="$arch"
  local webkit="libwebkit2gtk-4.1-0"; [ "$tags" = "desktop,production" ] && webkit="libwebkit2gtk-4.0-37"
  sed -e "s/@VERSION@/${VERSION}/" -e "s/@ARCH@/${debarch}/" -e "s/@WEBKIT@/${webkit}/" desktop/build/linux/control > "$root/DEBIAN/control"
  install -m755 desktop/build/linux/postinst "$root/DEBIAN/postinst"
  dpkg-deb --root-owner-group --build "$root" "$DIST/pingvakt_${VERSION}_${debarch}.deb" >/dev/null
  rm -rf "$root"
  tar -C "$DIST" -czf "$DIST/pingvakt-${VERSION}-linux-${arch}.tar.gz" pingvakt
  echo "klart: $DIST/pingvakt_${VERSION}_${debarch}.deb"
}

macos() {
  echo "==> macOS ${VERSION}"
  [ "$(uname)" = "Darwin" ] || { echo "macOS-bygget måste köras på en Mac (eller i GitHub Actions)"; exit 1; }
  for a in arm64 amd64; do
    CGO_ENABLED=1 GOOS=darwin GOARCH=$a MACOSX_DEPLOYMENT_TARGET=11.0 CGO_LDFLAGS="-framework UniformTypeIdentifiers -mmacosx-version-min=11.0" CGO_CFLAGS="-mmacosx-version-min=11.0" \
      logged go build -trimpath -tags desktop,production -ldflags "$LDFLAGS" -o "$DIST/pingvakt-darwin-$a" ./desktop
  done
  local app="$DIST/PingVakt.app"
  rm -rf "$app"; mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
  logged lipo -create -output "$app/Contents/MacOS/PingVakt" "$DIST/pingvakt-darwin-arm64" "$DIST/pingvakt-darwin-amd64"
  sed "s/@VERSION@/${VERSION}/g" desktop/build/darwin/Info.plist > "$app/Contents/Info.plist"
  local iconset; iconset="$(mktemp -d)/PingVakt.iconset"; mkdir -p "$iconset"
  for s in 16 32 128 256 512; do
    logged sips -z $s $s desktop/build/appicon.png --out "$iconset/icon_${s}x${s}.png" >/dev/null
    logged sips -z $((s*2)) $((s*2)) desktop/build/appicon.png --out "$iconset/icon_${s}x${s}@2x.png" >/dev/null
  done
  logged iconutil -c icns "$iconset" -o "$app/Contents/Resources/iconfile.icns"
  logged codesign --force --deep -s - "$app" # ad hoc-signering (ingen Apple-utvecklarlicens)
  rm -f "$DIST/PingVakt-${VERSION}-macos.dmg"
  # hdiutil misslyckas ibland tillfälligt ("Resource busy") på byggservrar – försök igen
  local ok=0
  for i in 1 2 3 4 5; do
    if hdiutil create -volname "PingVakt" -srcfolder "$app" -ov -format UDZO "$DIST/PingVakt-${VERSION}-macos.dmg"; then ok=1; break; fi
    sleep 5
  done
  [ "$ok" = 1 ] || { echo "::error::hdiutil kunde inte skapa .dmg"; exit 1; }
  (cd "$DIST" && ditto -c -k --keepParent PingVakt.app "PingVakt-${VERSION}-macos-app.zip")
  echo "klart: $DIST/PingVakt-${VERSION}-macos.dmg"
}

agents() {
  echo "==> Agenter ${VERSION}"
  mkdir -p "$DIST/agents"
  for p in linux/amd64 linux/arm64 linux/arm darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
    local os=${p%/*} arch=${p#*/} name ext=""
    name=$arch; [ "$arch" = arm ] && name=armv7; [ "$os" = windows ] && ext=.exe
    CGO_ENABLED=0 GOOS=$os GOARCH=$arch GOARM=7 logged go build -trimpath -ldflags "$LDFLAGS" -o "$DIST/agents/pingvakt-agent-$os-$name$ext" ./agent
  done
}

server() {
  agents
  echo "==> Server ${VERSION}"
  for p in linux/amd64 linux/arm64 windows/amd64; do
    local os=${p%/*} arch=${p#*/} ext=""
    [ "$os" = windows ] && ext=.exe
    local dir="$DIST/pingvakt-server-${VERSION}-$os-$arch"
    rm -rf "$dir"; mkdir -p "$dir"
    CGO_ENABLED=0 GOOS=$os GOARCH=$arch logged go build -trimpath -ldflags "$LDFLAGS" -o "$dir/pingvakt-server$ext" ./server
    cp -r "$DIST/agents" "$dir/agents"
    cp deploy/pingvakt-server.service deploy/docker-compose.yml deploy/Caddyfile LICENSE-THIRD-PARTY.txt "$dir/"
    if [ "$os" = windows ]; then (cd "$DIST" && rm -f "$(basename "$dir").zip" && zip -qr "$(basename "$dir").zip" "$(basename "$dir")")
    else tar -C "$DIST" -czf "$dir.tar.gz" "$(basename "$dir")"; fi
    rm -rf "$dir"
  done
  echo "klart: $DIST/pingvakt-server-${VERSION}-*"
}

case "${1:-}" in
  windows) windows ;;
  linux) linux ;;
  macos) macos ;;
  agents) agents ;;
  server) server ;;
  all) windows; linux; server ;;
  *) echo "användning: $0 windows|linux|macos|agents|server|all"; exit 1 ;;
esac
