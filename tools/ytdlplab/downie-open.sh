#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "usage: downie-open.sh <url> [destination]" >&2
  exit 2
fi

url="$1"
destination="${2:-}"
app="${DOWNIE_APP_NAME:-Downie 4}"

if [[ -n "$destination" ]]; then
  mkdir -p "$destination"
  encoded_url="$(python3 -c 'import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=""))' "$url")"
  encoded_destination="$(python3 -c 'import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=""))' "$destination")"
  open -a "$app" "downie://XUOpenURL?url=${encoded_url}&destination=${encoded_destination}"
else
  open -a "$app" "$url"
fi

echo "submitted to ${app}: ${url}"
