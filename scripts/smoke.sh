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
check "POST /compute/v1/jobs unknown sku -> 400" 400 \
  "$(code -X POST -H 'Authorization: Bearer sk-smoke-test-key' -H 'Content-Type: application/json' -d '{"sku":"nope","spec":{"max_runtime_seconds":600}}' "$BASE_URL/compute/v1/jobs")"
body_has "unknown sku is named" '"code":"unknown_sku"'
check "POST /compute/v1/jobs valid -> 501 (unbuilt)" 501 \
  "$(code -X POST -H 'Authorization: Bearer sk-smoke-test-key' -H 'Content-Type: application/json' -d "$valid" "$BASE_URL/compute/v1/jobs")"

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
