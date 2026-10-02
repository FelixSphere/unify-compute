#!/usr/bin/env bash
# Probes one environment from OUTSIDE after a cutover, the way a customer
# reaches it. Exit status is the verdict.
#
#   ORIGIN=https://staging.compute.unifyai.us scripts/release/smoke.sh
#
# Optional:
#   EXPECT_BUILD=<commit>   /healthz must report this build. This is what
#                           proves the NEW compute-api is serving publicly,
#                           not merely that something answers.
#   WAIT_SECONDS=150        how long to wait for the first answer: the ALB
#                           marks a target healthy again only after its own
#                           health-check threshold, after the host is fine.
#
# Why these probes: a 200 from `/` proves little, because a static page keeps
# answering after everything behind it is gone, and `/healthz` is liveness
# only. An authenticated route answering 401 with compute-api's own error
# code is the service routing, running and enforcing auth (unifydata's
# lesson). Read-only throughout: nothing here creates a job.
set -euo pipefail

ORIGIN="${ORIGIN:?set ORIGIN, e.g. https://staging.compute.unifyai.us}"
ORIGIN="${ORIGIN%/}"
EXPECT_BUILD="${EXPECT_BUILD:-}"
WAIT_SECONDS="${WAIT_SECONDS:-150}"
ICON_PATH="${ICON_PATH:-/icon-v3.svg}"
# The icon's name is versioned (app/layout.tsx), so it may be cached for a
# long time; at least a day is the floor we hold the site container to.
ICON_MIN_MAX_AGE="${ICON_MIN_MAX_AGE:-86400}"

fail=0
body="$(mktemp)"; headers="$(mktemp)"
trap 'rm -f "$body" "$headers"' EXIT

ok()  { printf 'ok   %s\n' "$*"; }
bad() { printf 'FAIL %s\n' "$*"; echo "::error::smoke $ORIGIN: $*"; fail=1; }

# Fetch into $body / $headers; print the status code.
get() {
  curl -sS --max-time 20 -o "$body" -D "$headers" -w '%{http_code}' "$@" 2>/dev/null || echo 000
}

expect_code() {
  local name="$1" want="$2" got="$3"
  if [ "$got" = "$want" ]; then ok "$name -> $got"; else bad "$name -> $got, want $want ($(head -c 200 "$body" | tr '\n' ' '))"; fi
}

expect_body() {
  local name="$1" needle="$2"
  if grep -qF -- "$needle" "$body"; then ok "$name"; else bad "$name: body lacks $needle ($(head -c 200 "$body" | tr '\n' ' '))"; fi
}

header() { grep -i "^$1:" "$headers" | tail -n 1 | cut -d: -f2- | tr -d '\r' | sed 's/^ *//'; }

echo "--- waiting up to ${WAIT_SECONDS}s for $ORIGIN/healthz"
deadline=$(( $(date +%s) + WAIT_SECONDS ))
until [ "$(get "$ORIGIN/healthz")" = 200 ]; do
  [ "$(date +%s)" -lt "$deadline" ] || break
  sleep 5
done

echo "--- compute-api"
expect_code "GET /healthz" 200 "$(get "$ORIGIN/healthz")"
expect_body "healthz names compute-api" '"service":"compute-api"'
if [ -n "$EXPECT_BUILD" ]; then
  expect_body "healthz reports build $EXPECT_BUILD" "\"build\":\"$EXPECT_BUILD\""
fi

expect_code "GET /compute/v1/skus" 200 "$(get "$ORIGIN/compute/v1/skus")"
ctype="$(header content-type)"
case "$ctype" in
  application/json*) ok "skus is JSON ($ctype)" ;;
  *) bad "skus content-type is '$ctype', want application/json" ;;
esac
if jq -e '.data | length > 0' "$body" >/dev/null 2>&1; then ok "skus lists at least one SKU"; else bad "skus has no .data entries"; fi
expect_body "skus carry a price" '"price_usd_per_hour"'

expect_code "GET /compute/v1/jobs without a key" 401 "$(get "$ORIGIN/compute/v1/jobs")"
# The code proves the 401 came from compute-api's own auth, not from a proxy
# or a default page that happens to say 401.
expect_body "401 is compute-api's missing_key" '"code":"missing_key"'

echo "--- site"
expect_code "GET /" 200 "$(get "$ORIGIN/")"
expect_body "/ is the Unify Compute site" '<title>Unify Compute'

expect_code "GET /privacy" 200 "$(get -L --max-redirs 2 "$ORIGIN/privacy")"

expect_code "GET $ICON_PATH" 200 "$(get "$ORIGIN$ICON_PATH")"
cc="$(header cache-control)"
age="$(printf '%s' "$cc" | sed -n 's/.*max-age=\([0-9][0-9]*\).*/\1/p')"
if [ -n "$age" ] && [ "$age" -ge "$ICON_MIN_MAX_AGE" ]; then
  ok "icon Cache-Control: $cc"
else
  bad "icon Cache-Control is '$cc', want max-age >= $ICON_MIN_MAX_AGE"
fi

exit "$fail"
