#!/usr/bin/env bash
# Probes a compute-api (and optionally its console) and prints what answered.
# Exit status is the verdict; `docker ps` saying "running" is not.
#
#   BASE_URL=https://staging.compute.unifyai.us \
#   CONSOLE_URL=https://console.staging.compute.unifyai.us scripts/smoke.sh
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:8080}"
CONSOLE_URL="${CONSOLE_URL:-}"
fail=0
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

check() {
  local name="$1" want="$2" got="$3"
  if [ "$got" = "$want" ]; then
    printf 'ok   %-46s %s\n' "$name" "$got"
  else
    printf 'FAIL %-46s got %s, want %s\n' "$name" "$got" "$want"
    fail=1
  fi
}

body_has() {
  local name="$1" needle="$2"
  if grep -q -- "$needle" "$tmp"; then
    printf 'ok   %-46s\n' "$name"
  else
    printf 'FAIL %-46s %s\n' "$name" "$(head -c 200 "$tmp")"
    fail=1
  fi
}

code() { curl -sS -o "$tmp" -w '%{http_code}' "$@" || echo 000; }

check "GET /healthz" 200 "$(code "$BASE_URL/healthz")"
body_has "healthz names the service" '"service":"compute-api"'

check "GET /compute/v1/skus" 200 "$(code "$BASE_URL/compute/v1/skus")"
body_has "skus carry a price" '"price_usd_per_hour"'
body_has "skus carry a tier" '"tier":"'

check "GET /compute/v1/upstreams" 200 "$(code "$BASE_URL/compute/v1/upstreams")"
body_has "staging has aws enabled" '"cloud":"aws","enabled":true'

valid='{"sku":"a10g-24gb-x1","spec":{"max_runtime_seconds":600}}'
check "POST /compute/v1/jobs without key -> 401" 401 \
  "$(code -X POST -H 'Content-Type: application/json' -d "$valid" "$BASE_URL/compute/v1/jobs")"
check "GET /compute/v1/jobs without key -> 401" 401 "$(code "$BASE_URL/compute/v1/jobs")"

# A made-up key. A build with a job store verifies it through the console
# bridge and refuses it before validation: 401 invalid_key where the bridge
# answers, 503 console_unavailable where it is dark or CONSOLE_URL is unset
# (production today). The dark build (no DATABASE_URL) checks only the key's
# shape, so the request reaches validation: 400 unknown_sku, then 501.
# Accepting both keeps the smoke meaningful across a rollback.
made_up() {
  local name="$1" body="$2" dark_status="$3" dark_code="$4" got
  got="$(code -X POST -H 'Authorization: Bearer sk-smoke-test-key' -H 'Content-Type: application/json' -d "$body" "$BASE_URL/compute/v1/jobs")"
  case "$got" in
    401) check "$name (bridge live) -> 401" 401 "$got"; body_has "made-up key is refused" '"code":"invalid_key"' ;;
    503) check "$name (bridge dark) -> 503" 503 "$got"; body_has "key cannot be verified" '"code":"console_unavailable"' ;;
    *)   check "$name (dark build) -> $dark_status" "$dark_status" "$got"; body_has "dark build answers $dark_code" "\"code\":\"$dark_code\"" ;;
  esac
}
made_up "POST /compute/v1/jobs unknown sku, made-up key" '{"sku":"nope","spec":{"max_runtime_seconds":600}}' 400 unknown_sku
made_up "POST /compute/v1/jobs valid, made-up key" "$valid" 501 not_implemented

# The compute host must not proxy the console and vice versa.
check "compute host does not serve console /api/status" 404 "$(code "$BASE_URL/api/status")"

if [ -n "$CONSOLE_URL" ]; then
  check "console GET /api/status" 200 "$(code "$CONSOLE_URL/api/status")"
  body_has "console reports success" '"success":true'
  # Not a status check: the console is an SPA and answers 200 with its own
  # index.html for any path it does not know (seen on staging 2026-10-02).
  # What must not happen is compute-api answering there.
  code "$CONSOLE_URL/compute/v1/skus" >/dev/null
  if grep -q -e '"price_usd_per_hour"' -e '"service":"compute-api"' "$tmp"; then
    printf 'FAIL %-46s %s\n' "console host does not serve compute-api" "$(head -c 200 "$tmp")"
    fail=1
  else
    printf 'ok   %-46s\n' "console host does not serve compute-api"
  fi
fi

exit "$fail"
