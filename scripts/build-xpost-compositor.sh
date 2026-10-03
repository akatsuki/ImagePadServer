#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
manifest_path="$repo_root/gpu/xpost-compositord/Cargo.toml"
target_path="$repo_root/gpu/nico-compositord/target"
output_directory="$repo_root/build/xpost-compositord"
binary_name=xpost-compositord

case "$(uname -s)" in
  MINGW64_NT*|MINGW32_NT*|MSYS_NT*)
  binary_name=xpost-compositord.exe
  ;;
esac

CARGO_TARGET_DIR="$target_path" cargo build --locked --manifest-path "$manifest_path" --release
mkdir -p "$output_directory"
cp "$target_path/release/$binary_name" "$output_directory/$binary_name"
printf '%s\n' "$output_directory/$binary_name"
