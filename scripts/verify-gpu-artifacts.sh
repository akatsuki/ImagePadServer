#!/usr/bin/env sh
set -eu

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
MANIFEST="$ROOT_DIR/gpu/playlist-compositord/Cargo.toml"
SIDECAR_VERSION="$(sed -n 's/^version = "\([^"]*\)"/\1/p' "$MANIFEST" | head -n 1)"
ARTIFACT_DIR="${GPU_SIDECAR_DIR:-$ROOT_DIR/dist/gpu/$SIDECAR_VERSION}"

if [ ! -d "$ARTIFACT_DIR" ]; then
  echo "GPU artifact directory is missing: $ARTIFACT_DIR" >&2
  exit 1
fi
FOUND=0
for BIN in "$ARTIFACT_DIR"/playlist-compositord.exe "$ARTIFACT_DIR"/playlist-compositord; do
  [ -f "$BIN" ] || continue
  FOUND=1
  ACTUAL="$($BIN --version 2>/dev/null || true)"
  EXPECTED="playlist-compositord $SIDECAR_VERSION"
  [ "$ACTUAL" = "$EXPECTED" ] || { echo "sidecar version mismatch: $BIN ($ACTUAL != $EXPECTED)" >&2; exit 1; }
  SUM_FILE="$BIN.sha256"
  [ -f "$SUM_FILE" ] || { echo "checksum manifest missing: $SUM_FILE" >&2; exit 1; }
  if command -v sha256sum >/dev/null 2>&1; then sha256sum -c "$SUM_FILE"; else shasum -a 256 -c "$SUM_FILE"; fi
  break
done
[ "$FOUND" -eq 1 ] || { echo "no playlist-compositord artifact found in $ARTIFACT_DIR" >&2; exit 1; }
printf '%s\n' "verified GPU sidecar artifacts in $ARTIFACT_DIR"
