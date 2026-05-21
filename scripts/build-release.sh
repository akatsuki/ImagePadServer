#!/usr/bin/env sh
set -eu

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
DIST_DIR="$ROOT_DIR/dist"
APP_NAME="imagepadserver"
GO_VERSION="$(go env GOVERSION 2>/dev/null || true)"
if [ -z "$GO_VERSION" ]; then
  GO_VERSION="$(go version | awk '{print $3}')"
fi

mkdir -p "$DIST_DIR"

build_one() {
  goos="$1"
  goarch="$2"
  ext="$3"
  if [ "$goos/$goarch" = "darwin/arm64" ] && printf '%s\n' "$GO_VERSION" | grep -Eq '^go1\.(1[0-5])(\.|$)'; then
    echo "skipping darwin/arm64: Go 1.16 or newer is required for this target"
    return
  fi
  out="$DIST_DIR/${APP_NAME}-${goos}-${goarch}${ext}"
  echo "building $out"
  ldflags=""
  if [ "$goos" = "windows" ]; then
    ldflags="-H=windowsgui"
  fi
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags "$ldflags" -o "$out" "$ROOT_DIR/cmd/imagepadserver"
}

build_one windows amd64 .exe
build_one darwin amd64 ""
build_one darwin arm64 ""
build_one linux amd64 ""
build_one linux arm64 ""

echo "done: $DIST_DIR"
