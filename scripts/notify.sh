#!/usr/bin/env bash
# Send a short progress message to Aurora's Telegram chat.
#
# Usage: ./scripts/notify.sh "text of the message"
#
# Reads TELEGRAM_TOKEN from the repo .env at send time; the token is never
# echoed or logged. A failed send never blocks anything (always exits 0).
set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
env_file="$repo_root/.env"

if [ ! -f "$env_file" ]; then
  echo "notify: no .env — skipping" >&2
  exit 0
fi

set -a
# shellcheck disable=SC1090
source "$env_file"
set +a

# Both the token and the recipient chat id live only in the gitignored .env,
# never in tracked source (this repo is public).
if [ -z "${TELEGRAM_TOKEN:-}" ]; then
  echo "notify: TELEGRAM_TOKEN not set — skipping" >&2
  exit 0
fi

chat_id="${TELEGRAM_CHAT_ID:-}"
if [ -z "$chat_id" ]; then
  echo "notify: TELEGRAM_CHAT_ID not set in .env — skipping" >&2
  exit 0
fi

curl -s -m 10 -X POST "https://api.telegram.org/bot${TELEGRAM_TOKEN}/sendMessage" \
  --data-urlencode "chat_id=${chat_id}" \
  --data-urlencode "text=${1:-(empty notify)}" \
  -o /dev/null || echo "notify: send failed (non-blocking)" >&2

exit 0
