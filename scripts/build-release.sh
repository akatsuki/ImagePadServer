#!/usr/bin/env sh
set -eu

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
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
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags "$ldflags" -o "$out" "$ROOT_DIR/cmd/imagepadserver"
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

build_one windows amd64 .exe
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
