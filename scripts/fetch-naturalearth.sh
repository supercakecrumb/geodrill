#!/usr/bin/env bash
# Fetch the Natural Earth 1:10m admin-0 countries dataset as GeoJSON — the
# public-domain vector basemap the city-map renderer draws from (cmd/citymaps
# via internal/citymap).
#
# Source: https://github.com/nvkelso/natural-earth-vector (geojson/)
# License: Natural Earth is PUBLIC DOMAIN — no attribution is required. We
# credit it anyway: "Made with Natural Earth. Free vector and raster map data
# @ naturalearthdata.com."
#
# The 1:10m resolution is REQUIRED, not a nicety: at 1:50m and 1:110m the
# microstates (Monaco, San Marino, Singapore, Malta, Liechtenstein) degrade
# into a couple of pixels or vanish entirely, which would leave those cities'
# maps blank. 1:10m keeps them present enough to render a recognizable frame.
#
# Downloads into the gitignored data/naturalearth/ directory:
#   data/naturalearth/ne_10m_admin_0_countries.geojson  (~24 MB)
#
# Idempotent: if the destination already exists this script does nothing
# unless --force is passed (which re-downloads).
#
# Usage: ./scripts/fetch-naturalearth.sh [--force]   (safe to run from any
# directory; paths are resolved relative to this script's location)
# Then: go run ./cmd/citymaps -only <city-key>

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out_dir="${repo_root}/data/naturalearth"
dest="${out_dir}/ne_10m_admin_0_countries.geojson"
url="https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson/ne_10m_admin_0_countries.geojson"
user_agent="geodrill-naturalearth-fetch/1.0 (+https://github.com/supercakecrumb/geodrill; contact: supercakecrumb@gmail.com)"

force=0
if [ "${1:-}" = "--force" ]; then
  force=1
fi

mkdir -p "$out_dir"

if [ -s "$dest" ] && [ "$force" -eq 0 ]; then
  echo "SKIP: ${dest} already exists (use --force to re-download)"
  exit 0
fi

echo "Downloading ${url} ..."
tmp="${dest}.tmp"
curl -fsS -A "$user_agent" --connect-timeout 10 --max-time 300 -o "$tmp" "$url"
mv "$tmp" "$dest"

if [ ! -s "$dest" ]; then
  echo "ERROR: expected ${dest} after download but it's missing/empty" >&2
  exit 1
fi

echo "OK   downloaded -> ${dest}"
echo ""
echo "=== fetch-naturalearth summary ==="
echo "size: $(wc -c < "$dest" | tr -d ' ') bytes"
echo "Made with Natural Earth (public domain) — naturalearthdata.com."
echo "Next: go run ./cmd/citymaps -only <city-key>"
