#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
target=""
output_dir=""
for_go_embed=0
profile="release"

while (($#)); do
  case "$1" in
    --profile)
      profile="${2:?--profile requires a Cargo profile}"
      shift 2
      ;;
    --target)
      target="${2:?--target requires a Rust target triple}"
      shift 2
      ;;
    --output-directory)
      output_dir="${2:?--output-directory requires a path}"
      shift 2
      ;;
    --for-go-embed)
      for_go_embed=1
      shift
      ;;
    -h|--help)
      printf '%s\n' 'Usage: build-nico-timeline-compositor.sh [--profile release|release-thin|release-thin-one] [--target TRIPLE] [--output-directory PATH] [--for-go-embed]'
      exit 0
      ;;
    *)
      printf 'Unknown argument: %s\n' "$1" >&2
      exit 2
      ;;
  esac
done

case "$profile" in
  release|release-thin|release-thin-one) ;;
  *)
    printf 'Unsupported Cargo profile: %s\n' "$profile" >&2
    exit 2
    ;;
esac

if [[ -z "$target" ]]; then
  target="$(rustc -vV | sed -n 's/^host: //p')"
fi
if [[ -z "$target" ]]; then
  printf '%s\n' 'rustc did not report its host target' >&2
  exit 2
fi
if ((for_go_embed)); then
  output_dir="$repo_root/internal/nicorender/timeline_payload"
elif [[ -z "$output_dir" ]]; then
  output_dir="$repo_root/build/nico-timeline/artifacts/$target"
elif [[ "$output_dir" != /* ]]; then
  output_dir="$repo_root/$output_dir"
fi

target_dir="$repo_root/build/nico-timeline/cargo"
build_log=""
source_inputs_file=""
overrides_file=""
cleanup_provenance_files() {
  for temporary_file in "$build_log" "$source_inputs_file" "$overrides_file"; do
    if [[ -n "$temporary_file" ]]; then
      rm -f -- "$temporary_file"
    fi
  done
}
trap cleanup_provenance_files EXIT
build_log="$(mktemp "${TMPDIR:-/tmp}/nico-compositor-cargo.XXXXXXXX")"
source_inputs_file="$(mktemp "${TMPDIR:-/tmp}/nico-compositor-sources.XXXXXXXX")"
overrides_file="$(mktemp "${TMPDIR:-/tmp}/nico-compositor-env.XXXXXXXX")"

python3 "$repo_root/scripts/nico_timeline_manifest.py" source-inputs \
  --source-root "$repo_root" --output "$source_inputs_file" >/dev/null
python3 - "$repo_root" >"$overrides_file" <<'PY'
import json
import os
import sys

sys.path.insert(0, os.path.join(sys.argv[1], "scripts"))
import nico_timeline_manifest

json.dump(nico_timeline_manifest.collect_build_environment_overrides(os.environ), sys.stdout)
sys.stdout.write("\n")
PY

# Cargo/Rust override variables are hashed above and removed from this script's
# child environment. The parent shell is unaffected because this is a process.
while IFS= read -r name; do
  # Cargo and Rust build override variables use uppercase names; keep this
  # comparison compatible with macOS's system Bash 3.2.
  case "$name" in
    RUSTFLAGS|CARGO_ENCODED_RUSTFLAGS|RUSTC|RUSTC_WRAPPER|RUSTC_WORKSPACE_WRAPPER|CARGO_TARGET_DIR|CARGO_BUILD_TARGET|CARGO_PROFILE_*|CARGO_TARGET_*_RUSTFLAGS)
      unset "$name"
      ;;
  esac
done < <(compgen -e)

export CARGO_TARGET_DIR="$target_dir"
export CARGO_BUILD_TARGET="$target"
cargo build --locked --profile "$profile" \
  --manifest-path "$repo_root/gpu/nico-compositord/Cargo.toml" \
  --target-dir "$target_dir" \
  --target "$target" -vv >"$build_log" 2>&1 || {
    printf 'cargo build failed for %s/%s\n' "$target" "$profile" >&2
    exit 1
  }

binary_name="nico-compositord"
if [[ "$target" == *windows* ]]; then
  binary_name+=".exe"
fi
built_binary="$target_dir/$target/$profile/$binary_name"
if [[ ! -f "$built_binary" ]]; then
  printf 'built compositor is missing: %s\n' "$built_binary" >&2
  exit 1
fi
mkdir -p "$output_dir"
payload="$output_dir/nico-compositord.bin"
cp -- "$built_binary" "$payload.tmp"
mv -f -- "$payload.tmp" "$payload"
python3 "$repo_root/scripts/nico_timeline_manifest.py" \
  --binary "$payload" --target "$target" --output "$output_dir/manifest.json" \
  --cargo-manifest "$repo_root/gpu/nico-compositord/Cargo.toml" \
  --notices-output-directory "$output_dir"
python3 "$repo_root/scripts/nico_timeline_manifest.py" build-provenance \
  --binary "$payload" --target "$target" --mode "$profile" \
  --source-root "$repo_root" --source-inputs "$source_inputs_file" \
  --build-log "$build_log" --overrides-json "$overrides_file" \
  --output "$output_dir/build-manifest.json"
