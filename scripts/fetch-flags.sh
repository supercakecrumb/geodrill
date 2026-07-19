#!/usr/bin/env bash
# Fetch flag PNGs for every country/subdivision listed in seeds/countries.yaml.
#
# Source: flagcdn.com (operated by Flagpedia.net). Flag images are public
# domain (exempt from copyright) and free for non-commercial and commercial
# use, no attribution required — see the "License" section of
# https://flagpedia.net/about and vibe/design-flags-quiz.md.
#
# Downloads a w320 PNG per code into the gitignored media root
# data/flags/<lowercased-iso2>.png (e.g. data/flags/fr.png,
# data/flags/gb-sct.png — flagcdn.com uses lowercase, hyphenated codes for
# both countries and UK constituent-country subdivisions).
#
# Idempotent: a code whose PNG already exists on disk is skipped, no request
# made. Politeness: 1 request/second between network attempts. A failed
# download is logged to stderr and the script continues with the next code —
# it never aborts the whole run over one missing/renamed flag (the flags
# topic falls back to emoji-only text mode for items with no image, per
# vibe/design-flags-quiz.md §6). A summary prints at the end.
#
# Usage: ./scripts/fetch-flags.sh   (safe to run from any directory; paths
# are resolved relative to this script's location, not the caller's cwd)

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
countries_file="${repo_root}/seeds/countries.yaml"
out_dir="${repo_root}/data/flags"
base_url="https://flagcdn.com/w320"
user_agent="geodrill-flags-fetch/1.0 (+https://github.com/supercakecrumb/geodrill; contact: supercakecrumb@gmail.com)"
sleep_seconds=1

if [ ! -f "$countries_file" ]; then
  echo "ERROR: countries file not found: ${countries_file}" >&2
  exit 1
fi

mkdir -p "$out_dir"

total=0
downloaded=0
skipped=0
failed=0
failed_codes=""

echo "Fetching flags into ${out_dir} (source: ${base_url}/<code>.png) ..."

# Process-substitution (not a pipe) so counters below survive outside a subshell.
while IFS= read -r code; do
  [ -z "$code" ] && continue
  total=$((total + 1))

  lower="$(printf '%s' "$code" | tr '[:upper:]' '[:lower:]')"
  dest="${out_dir}/${lower}.png"

  if [ -s "$dest" ]; then
    skipped=$((skipped + 1))
    continue
  fi

  url="${base_url}/${lower}.png"
  tmp="${dest}.tmp"

  if curl -fsS -A "$user_agent" --connect-timeout 10 --max-time 30 -o "$tmp" "$url"; then
    mv "$tmp" "$dest"
    downloaded=$((downloaded + 1))
    echo "OK   ${code} -> ${lower}.png"
  else
    rm -f "$tmp"
    failed=$((failed + 1))
    failed_codes="${failed_codes}${code} "
    echo "FAIL ${code} (${lower}): download failed from ${url}" >&2
  fi

  sleep "$sleep_seconds"
done < <(sed -n 's/^ *- iso_a2: "\([^"]*\)".*/\1/p' "$countries_file")

echo ""
echo "=== fetch-flags summary ==="
echo "total codes:               ${total}"
echo "downloaded:                ${downloaded}"
echo "skipped (already present): ${skipped}"
echo "failed:                    ${failed}"
if [ "$failed" -gt 0 ]; then
  echo "failed codes: ${failed_codes}"
  echo "NOTE: missing images are handled gracefully by the flags topic (emoji-only text fallback, vibe/design-flags-quiz.md §6)."
fi
