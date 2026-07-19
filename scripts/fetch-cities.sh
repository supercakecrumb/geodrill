#!/usr/bin/env bash
# Fetch the GeoNames cities15000 dataset (every populated place with
# population >= 15,000 — ~26k rows worldwide, tab-separated, no header).
#
# Source: https://download.geonames.org/export/dump/cities15000.zip
# License: CC-BY 4.0 (https://www.geonames.org/about.html). Per the license,
# any product derived from this data must credit GeoNames — the derived
# seed file this repo actually commits (seeds/cities.yaml, produced by
# `go run ./cmd/citygen` from the raw download this script fetches) carries
# that attribution in its own header comment. The raw dump itself is NEVER
# committed (data/ is gitignored; the pre-commit secrets/data scan also
# blocks anything under data/** from being staged).
#
# Downloads into the gitignored data/geonames/ directory:
#   data/geonames/cities15000.zip  (the raw download, kept for --force reruns)
#   data/geonames/cities15000.txt  (unzipped TSV, read by cmd/citygen)
#
# Idempotent: if data/geonames/cities15000.txt already exists, this script
# does nothing unless --force is passed (which re-downloads and re-unzips).
#
# Usage: ./scripts/fetch-cities.sh [--force]   (safe to run from any
# directory; paths are resolved relative to this script's location)
# Regeneration: ./scripts/fetch-cities.sh && go run ./cmd/citygen

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out_dir="${repo_root}/data/geonames"
zip_path="${out_dir}/cities15000.zip"
txt_path="${out_dir}/cities15000.txt"
url="https://download.geonames.org/export/dump/cities15000.zip"
user_agent="geodrill-cities-fetch/1.0 (+https://github.com/supercakecrumb/geodrill; contact: supercakecrumb@gmail.com)"

force=0
if [ "${1:-}" = "--force" ]; then
  force=1
fi

mkdir -p "$out_dir"

if [ -s "$txt_path" ] && [ "$force" -eq 0 ]; then
  echo "SKIP: ${txt_path} already exists (use --force to re-download)"
  exit 0
fi

echo "Downloading ${url} ..."
tmp_zip="${zip_path}.tmp"
curl -fsS -A "$user_agent" --connect-timeout 10 --max-time 120 -o "$tmp_zip" "$url"
mv "$tmp_zip" "$zip_path"
echo "OK   downloaded -> ${zip_path}"

echo "Unzipping ${zip_path} ..."
unzip -o -q "$zip_path" -d "$out_dir"

if [ ! -s "$txt_path" ]; then
  echo "ERROR: expected ${txt_path} after unzip but it's missing/empty" >&2
  exit 1
fi

echo "OK   unzipped -> ${txt_path}"
echo ""
echo "=== fetch-cities summary ==="
echo "rows: $(wc -l < "$txt_path" | tr -d ' ')"
echo "Data © GeoNames (https://www.geonames.org/), licensed CC-BY 4.0."
echo "Next: go run ./cmd/citygen  (regenerates seeds/cities.yaml)"
