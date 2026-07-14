#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
image="imagepadserver-playlist-compositord-linux-contract"

docker build --pull -f "$repo_root/gpu/playlist-compositord/Dockerfile.linux" \
  -t "$image" "$repo_root/gpu/playlist-compositord"

# The container validates the Linux build and Rust contract tests. It is not a
# GPU acceptance lane: no software adapter is promoted as a supported result.
docker run --rm "$image" --version
