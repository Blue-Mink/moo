#!/usr/bin/env bash
# moo-health.sh — read-only status bundle for a Moo instance (safe unattended).
#
# Usage:
#   bash moo-health.sh [api-base]
#   api-base default: http://127.0.0.1:38101/api  (panel gateway: http://<NAS_IP>:5666/app/moo/api)
#
# Only GETs; no credentials required except the admin-only endpoints, which
# are best-effort (403 when called without a trusted admin session).
set -u
BASE="${1:-${MOO_API_BASE:-http://127.0.0.1:38101/api}}"
BASE="${BASE%/}"

jq_or_cat() { # pretty-print JSON if jq exists, else raw (truncated)
  if command -v jq >/dev/null 2>&1; then jq . 2>/dev/null || cat; else head -c 2000; fi
}

echo "== Moo health bundle @ $(date '+%F %T')"
echo "== base: $BASE"

get() { # $1=path $2=label ; prints status + body
  local out status body
  out=$(curl -s -m 10 -w '\n%{http_code}' "$BASE$1")
  status="${out##*$'\n'}"
  body="${out%$'\n'*}"
  printf -- "-- [%s] %s → HTTP %s\n" "$2" "$1" "$status"
  if [ "$status" = "200" ]; then printf '%s\n' "$body" | jq_or_cat; fi
}

get /version        "version (public)"
get /status         "status (public)"
get /daemon/status  "appcenter daemon"
get /store-update   "self-update probe"
get /mirrors/health "github mirror health"
get /mirrors/docker/health "docker mirror health"
get /operations     "operations (current+history)"
get /tasks          "background tasks"
get /icons/version  "icon cache generation"
get /settings       "settings (admin; 403 w/o trusted session)"
get /sources        "sources (admin; 403 w/o trusted session)"
get /notify-log     "notify log (admin; 403 w/o trusted session)"

echo "== done"
