#!/usr/bin/env sh
set -eu

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
MANIFEST="$ROOT_DIR/gpu/playlist-compositord/Cargo.toml"
SIDECAR_VERSION="$(sed -n 's/^version = "\([^"]*\)"/\1/p' "$ROOT_DIR/gpu/playlist-compositord/Cargo.toml" | head -n 1)"
TARGET_DIR="${GPU_SIDECAR_DIR:-$ROOT_DIR/dist/gpu/$SIDECAR_VERSION}"
TARGET="${GPU_SIDECAR_TARGET:-}"

command -v cargo >/dev/null 2>&1 || { echo "cargo is required to build playlist-compositord" >&2; exit 1; }
mkdir -p "$TARGET_DIR"
if [ -n "$TARGET" ]; then
  cargo build --manifest-path "$MANIFEST" --release --target "$TARGET"
  BIN="$ROOT_DIR/gpu/playlist-compositord/target/$TARGET/release/playlist-compositord"
else
  cargo build --manifest-path "$MANIFEST" --release
  BIN="$ROOT_DIR/gpu/playlist-compositord/target/release/playlist-compositord"
fi
[ -f "$BIN" ] || { echo "sidecar binary not found: $BIN" >&2; exit 1; }
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) EXT=.exe ;;
  *) EXT= ;;
esac
OUT="$TARGET_DIR/playlist-compositord${EXT}"
cp "$BIN" "$OUT"
"$OUT" --version | grep -F "playlist-compositord $SIDECAR_VERSION" >/dev/null
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum "$OUT" > "$OUT.sha256"
else
  shasum -a 256 "$OUT" > "$OUT.sha256"
fi
printf '%s\n' "built $OUT"
