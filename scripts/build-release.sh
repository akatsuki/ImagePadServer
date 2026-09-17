#!/usr/bin/env sh
set -eu

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
# git-bash/MSYS exposes POSIX-style paths (/c/Users/...) that the Windows go
# toolchain rejects ("empty path element"). Convert to the mixed form
# (C:/Users/...) when cygpath is available; native shells are unaffected.
if command -v cygpath >/dev/null 2>&1; then
  ROOT_DIR="$(cygpath -m "$ROOT_DIR")"
fi
DIST_DIR="$ROOT_DIR/dist"
APP_NAME="imagepadserver"
VERSION="$(sed -n 's/.*Version[[:space:]]*=[[:space:]]*"\(v[^"]*\)".*/\1/p' "$ROOT_DIR/internal/about/about.go" | head -n 1)"
VERSION_NUMBER="${VERSION#v}"
if printf '%s\n' "$VERSION_NUMBER" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+-dev[0-9]+$'; then
  RELEASE_VERSION="${VERSION_NUMBER%%-dev*}"
  DEV_NAME="${VERSION_NUMBER#*-}"
  BUILD_DIR="$DIST_DIR/$RELEASE_VERSION/dev/$DEV_NAME"
else
  RELEASE_VERSION="$VERSION_NUMBER"
  BUILD_DIR="$DIST_DIR/$RELEASE_VERSION/release"
fi
WIN_DIR="$BUILD_DIR/win"
MAC_DIR="$BUILD_DIR/mac"
LINUX_DIR="$BUILD_DIR/linux"
AUTHOR="$(sed -n 's/.*Author[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "$ROOT_DIR/internal/about/about.go" | head -n 1)"
COPYRIGHT="$(sed -n 's/.*Copyright[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "$ROOT_DIR/internal/about/about.go" | head -n 1)"
GO_VERSION="$(go env GOVERSION 2>/dev/null || true)"
if [ -z "$GO_VERSION" ]; then
  GO_VERSION="$(go version | awk '{print $3}')"
fi

# The runtime archive is intentionally not guessed or downloaded by the
# release job. A maintainer may provide a pinned HTTPS asset after its
# license/source bundle has been reviewed; otherwise the application remains
# fail-closed and reports that the runtime bootstrap is not configured.
AIRPLAY_RUNTIME_SET_ID="${AIRPLAY_RUNTIME_SET_ID:-$VERSION_NUMBER}"
AIRPLAY_RUNTIME_ARCHIVE_URL="${AIRPLAY_RUNTIME_ARCHIVE_URL:-}"
AIRPLAY_RUNTIME_ARCHIVE_SHA256="${AIRPLAY_RUNTIME_ARCHIVE_SHA256:-}"
AIRPLAY_RUNTIME_ARCHIVE_SIZE="${AIRPLAY_RUNTIME_ARCHIVE_SIZE:-}"
AIRPLAY_RUNTIME_LICENSE_MANIFEST_SHA256="${AIRPLAY_RUNTIME_LICENSE_MANIFEST_SHA256:-}"
AIRPLAY_RUNTIME_SOURCE_OFFER_SHA256="${AIRPLAY_RUNTIME_SOURCE_OFFER_SHA256:-}"
# When pins are supplied, this is the local archive copied into the Go
# package and embedded into the Windows EXE. It is intentionally a build
# input, never a file shipped beside the EXE.
AIRPLAY_RUNTIME_ARCHIVE_PATH="${AIRPLAY_RUNTIME_ARCHIVE_PATH:-}"
AIRPLAY_RUNTIME_SOURCES_PATH="${AIRPLAY_RUNTIME_SOURCES_PATH:-}"
AIRPLAY_RUNTIME_BOOTSTRAP_B64=""
AIRPLAY_RUNTIME_BUILD_TAG=""

mkdir -p "$WIN_DIR" "$MAC_DIR" "$LINUX_DIR"

platform_dir() {
  case "$1" in
    windows) printf '%s\n' "$WIN_DIR" ;;
    darwin) printf '%s\n' "$MAC_DIR" ;;
    linux) printf '%s\n' "$LINUX_DIR" ;;
    *) printf '%s\n' "$BUILD_DIR/$1" ;;
  esac
}

build_one() {
  goos="$1"
  goarch="$2"
  ext="$3"
  if [ "$goos/$goarch" = "darwin/arm64" ] && printf '%s\n' "$GO_VERSION" | grep -Eq '^go1\.(1[0-5])(\.|$)'; then
    echo "skipping darwin/arm64: Go 1.16 or newer is required for this target"
    return
  fi
  out_dir="$(platform_dir "$goos")"
  mkdir -p "$out_dir"
  out="$out_dir/${APP_NAME}-${VERSION}-${goos}-${goarch}${ext}"
  echo "building $out"
  ldflags=""
  if [ "$goos" = "windows" ]; then
    ldflags="-H=windowsgui"
  fi
  if [ -n "$AIRPLAY_RUNTIME_BOOTSTRAP_B64" ]; then
    ldflags="$ldflags -X imagepadserver/internal/airplay.embeddedRuntimeBootstrapBase64=$AIRPLAY_RUNTIME_BOOTSTRAP_B64"
  fi
  build_tags=""
  if [ "$goos" = "windows" ]; then
    build_tags="$AIRPLAY_RUNTIME_BUILD_TAG"
    if [ "$goarch" = "amd64" ]; then
      prepare_nico_compositor
      if [ -n "$build_tags" ]; then
        build_tags="$build_tags,nico_native_embedded"
      else
        build_tags="nico_native_embedded"
      fi
    fi
  fi
  if [ -n "$build_tags" ]; then
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -tags "$build_tags" -trimpath -ldflags "$ldflags" -o "$out" "$ROOT_DIR/cmd/imagepadserver"
  else
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags "$ldflags" -o "$out" "$ROOT_DIR/cmd/imagepadserver"
  fi
}

prepare_nico_compositor() {
  case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*)
      pwsh -NoProfile -File "$ROOT_DIR/scripts/build-nico-compositor.ps1" -Stage
      ;;
  esac
  # Cross builds must stage a Windows-tested helper first. Source and payload
  # hashes prevent silently shipping a stale helper with a new protocol.
  python3 "$ROOT_DIR/scripts/verify-nico-payload.py" "$ROOT_DIR"
}

pack_windows_zip() {
  goarch="$1"
  exe="$WIN_DIR/${APP_NAME}-${VERSION}-windows-${goarch}.exe"
  archive="$WIN_DIR/${APP_NAME}-${VERSION}-windows-${goarch}.zip"
  if [ ! -f "$exe" ]; then
    return
  fi
  echo "packing $archive"
  rm -f "$archive"
  if command -v zip >/dev/null 2>&1; then
    zip -q -j -X "$archive" "$exe"
  elif command -v powershell >/dev/null 2>&1; then
    powershell -NoProfile -Command "Compress-Archive -Path '$exe' -DestinationPath '$archive' -Force"
  else
    echo "warning: neither zip nor powershell available; skipping $archive"
  fi
}

write_airplay_runtime_bootstrap() {
  if [ -z "$AIRPLAY_RUNTIME_ARCHIVE_URL$AIRPLAY_RUNTIME_ARCHIVE_SHA256$AIRPLAY_RUNTIME_ARCHIVE_SIZE$AIRPLAY_RUNTIME_LICENSE_MANIFEST_SHA256$AIRPLAY_RUNTIME_SOURCE_OFFER_SHA256" ]; then
    echo "AirPlay runtime bootstrap: NOT_CONFIGURED (no pinned asset supplied)"
    return
  fi
  if [ -z "$AIRPLAY_RUNTIME_ARCHIVE_URL" ] || [ -z "$AIRPLAY_RUNTIME_ARCHIVE_SHA256" ] ||
     [ -z "$AIRPLAY_RUNTIME_ARCHIVE_SIZE" ] || [ -z "$AIRPLAY_RUNTIME_LICENSE_MANIFEST_SHA256" ] ||
     [ -z "$AIRPLAY_RUNTIME_SOURCE_OFFER_SHA256" ]; then
    echo "all AirPlay runtime archive and metadata pins must be supplied together" >&2
    exit 1
  fi
  case "$AIRPLAY_RUNTIME_ARCHIVE_URL" in
    https://*) ;;
    *) echo "AIRPLAY_RUNTIME_ARCHIVE_URL must use HTTPS" >&2; exit 1 ;;
  esac
  if ! printf '%s\n' "$AIRPLAY_RUNTIME_ARCHIVE_SHA256" | grep -Eq '^[0-9a-fA-F]{64}$'; then
    echo "AIRPLAY_RUNTIME_ARCHIVE_SHA256 must be 64 hexadecimal characters" >&2
    exit 1
  fi
  if ! printf '%s\n' "$AIRPLAY_RUNTIME_LICENSE_MANIFEST_SHA256" | grep -Eq '^[0-9a-fA-F]{64}$' ||
     ! printf '%s\n' "$AIRPLAY_RUNTIME_SOURCE_OFFER_SHA256" | grep -Eq '^[0-9a-fA-F]{64}$'; then
    echo "AirPlay runtime metadata hashes must be 64 hexadecimal characters" >&2
    exit 1
  fi
  if ! printf '%s\n' "$AIRPLAY_RUNTIME_ARCHIVE_SIZE" | grep -Eq '^[1-9][0-9]*$'; then
    echo "AIRPLAY_RUNTIME_ARCHIVE_SIZE must be a positive integer" >&2
    exit 1
  fi
  if [ -z "$AIRPLAY_RUNTIME_ARCHIVE_PATH" ] || [ ! -f "$AIRPLAY_RUNTIME_ARCHIVE_PATH" ]; then
    echo "AIRPLAY_RUNTIME_ARCHIVE_PATH must point to the pinned local archive for EXE embedding" >&2
    exit 1
  fi
  archive_size="$(wc -c < "$AIRPLAY_RUNTIME_ARCHIVE_PATH" | tr -d '[:space:]')"
  archive_hash="$(python3 -c 'import hashlib,sys; print(hashlib.file_digest(open(sys.argv[1], "rb"), "sha256").hexdigest())' "$AIRPLAY_RUNTIME_ARCHIVE_PATH")"
  if [ "$archive_size" != "$AIRPLAY_RUNTIME_ARCHIVE_SIZE" ] ||
     ! printf '%s\n' "$archive_hash" | grep -Eiq "^$AIRPLAY_RUNTIME_ARCHIVE_SHA256$"; then
    echo "AIRPLAY_RUNTIME_ARCHIVE_PATH does not match the pinned archive size/hash" >&2
    exit 1
  fi
  # Validate the actual ZIP before staging it into the EXE. Old runtimes can
  # have valid archive hashes while still emitting incompatible multi-slice H.264.
  python3 "$ROOT_DIR/scripts/verify-airplay-h264-archive.py" "$AIRPLAY_RUNTIME_ARCHIVE_PATH"
  python3 "$ROOT_DIR/scripts/verify-airplay-license-archive.py" "$AIRPLAY_RUNTIME_ARCHIVE_PATH" "$AIRPLAY_RUNTIME_SOURCES_PATH"
  bootstrap="$WIN_DIR/airplay-runtime-bootstrap.json"
  cat > "$bootstrap" <<JSON
{
  "schema": 1,
  "runtimeSetID": "$AIRPLAY_RUNTIME_SET_ID",
  "archiveUrl": "$AIRPLAY_RUNTIME_ARCHIVE_URL",
  "archiveSha256": "$AIRPLAY_RUNTIME_ARCHIVE_SHA256",
  "archiveSize": $AIRPLAY_RUNTIME_ARCHIVE_SIZE,
  "licenseManifestSha256": "${AIRPLAY_RUNTIME_LICENSE_MANIFEST_SHA256:-}",
  "sourceOfferSha256": "${AIRPLAY_RUNTIME_SOURCE_OFFER_SHA256:-}"
}
JSON
  AIRPLAY_RUNTIME_BOOTSTRAP_B64="$(cat "$bootstrap" | base64 | tr -d '\r\n')"
  payload_dir="$ROOT_DIR/internal/airplay/payload"
  mkdir -p "$payload_dir"
  cp "$AIRPLAY_RUNTIME_ARCHIVE_PATH" "$payload_dir/runtime.zip"
  cp "$bootstrap" "$payload_dir/runtime-bootstrap.json"
  AIRPLAY_RUNTIME_BUILD_TAG="airplay_runtime_embedded"
  echo "AirPlay runtime payload staged for EXE embedding: $payload_dir/runtime.zip"
  echo "AirPlay runtime bootstrap: $bootstrap"
}

build_macos_app() {
  goarch="$1"
  app_dir="$MAC_DIR/ImagePadServer-$goarch.app"
  contents_dir="$app_dir/Contents"
  macos_dir="$contents_dir/MacOS"
  resources_dir="$contents_dir/Resources"
  exe="$macos_dir/ImagePadServer"

  echo "building $app_dir"
  rm -rf "$app_dir"
  mkdir -p "$macos_dir" "$resources_dir"
  CGO_ENABLED=1 GOOS=darwin GOARCH="$goarch" go build -trimpath -o "$exe" "$ROOT_DIR/cmd/imagepadserver"
  cp "$ROOT_DIR/assets/imagepad-icon.icns" "$resources_dir/ImagePadServer.icns"
  cat > "$contents_dir/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleExecutable</key>
  <string>ImagePadServer</string>
  <key>CFBundleIconFile</key>
  <string>ImagePadServer.icns</string>
  <key>CFBundleIdentifier</key>
  <string>jp.akatsuki.imagepadserver</string>
  <key>CFBundleName</key>
  <string>ImagePadServer</string>
  <key>CFBundleDisplayName</key>
  <string>ImagePadServer</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>CFBundleShortVersionString</key>
  <string>$VERSION_NUMBER</string>
  <key>CFBundleVersion</key>
  <string>$VERSION_NUMBER</string>
  <key>CFBundleGetInfoString</key>
  <string>ImagePadServer $VERSION by $AUTHOR</string>
  <key>NSHumanReadableCopyright</key>
  <string>$COPYRIGHT</string>
  <key>LSMinimumSystemVersion</key>
  <string>10.15</string>
  <key>LSUIElement</key>
  <true/>
</dict>
</plist>
PLIST
}

build_macos_universal_app() {
  universal_dir="$MAC_DIR/ImagePadServer.app"
  amd64_dir="$MAC_DIR/ImagePadServer-amd64.app"
  arm64_dir="$MAC_DIR/ImagePadServer-arm64.app"
  archive="$MAC_DIR/${APP_NAME}-${VERSION}-macos-universal.zip"

  echo "building $universal_dir"
  rm -rf "$universal_dir"
  cp -R "$arm64_dir" "$universal_dir"
  lipo -create \
    "$amd64_dir/Contents/MacOS/ImagePadServer" \
    "$arm64_dir/Contents/MacOS/ImagePadServer" \
    -output "$universal_dir/Contents/MacOS/ImagePadServer"
  echo "packing $archive"
  rm -f "$archive"
  ditto -c -k --sequesterRsrc --keepParent "$universal_dir" "$archive"
}

write_airplay_runtime_bootstrap
build_one windows amd64 .exe
pack_windows_zip amd64
if [ "$(uname -s)" = "Darwin" ]; then
  build_macos_app amd64
  build_macos_app arm64
  build_macos_universal_app
else
  build_one darwin amd64 ""
  build_one darwin arm64 ""
fi
build_one linux amd64 ""
build_one linux arm64 ""

echo "done: $BUILD_DIR"
