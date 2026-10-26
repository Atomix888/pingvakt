#!/usr/bin/env bash
# Bygger PingVakt. Användning:
#   scripts/build.sh windows   – PingVakt.exe + installationsprogram (kräver makensis och ImageMagick för ikonen)
#   scripts/build.sh linux     – binär, .deb och .tar.gz (kräver libgtk-3-dev och libwebkit2gtk-4.1-dev)
#   scripts/build.sh macos     – PingVakt.app och .dmg (måste köras på en Mac)
#   scripts/build.sh all       – windows + linux (från Linux)
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${VERSION:-$(cat VERSION)}"
DIST="dist"
mkdir -p "$DIST"
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
    CGO_ENABLED=1 GOOS=darwin GOARCH=$a MACOSX_DEPLOYMENT_TARGET=11.0 \
      go build -trimpath -tags desktop,production -ldflags "$LDFLAGS" -o "$DIST/pingvakt-darwin-$a" ./desktop
  done
  local app="$DIST/PingVakt.app"
  rm -rf "$app"; mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
  lipo -create -output "$app/Contents/MacOS/PingVakt" "$DIST/pingvakt-darwin-arm64" "$DIST/pingvakt-darwin-amd64"
  sed "s/@VERSION@/${VERSION}/g" desktop/build/darwin/Info.plist > "$app/Contents/Info.plist"
  local iconset; iconset="$(mktemp -d)/PingVakt.iconset"; mkdir -p "$iconset"
  for s in 16 32 128 256 512; do
    sips -z $s $s desktop/build/appicon.png --out "$iconset/icon_${s}x${s}.png" >/dev/null
    sips -z $((s*2)) $((s*2)) desktop/build/appicon.png --out "$iconset/icon_${s}x${s}@2x.png" >/dev/null
  done
  iconutil -c icns "$iconset" -o "$app/Contents/Resources/iconfile.icns"
  codesign --force --deep -s - "$app" # ad hoc-signering (ingen Apple-utvecklarlicens)
  rm -f "$DIST/PingVakt-${VERSION}-macos.dmg"
  hdiutil create -volname "PingVakt" -srcfolder "$app" -ov -format UDZO "$DIST/PingVakt-${VERSION}-macos.dmg" >/dev/null
  echo "klart: $DIST/PingVakt-${VERSION}-macos.dmg"
}

case "${1:-}" in
  windows) windows ;;
  linux) linux ;;
  macos) macos ;;
  all) windows; linux ;;
  *) echo "användning: $0 windows|linux|macos|all"; exit 1 ;;
esac
