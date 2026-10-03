#!/usr/bin/env bash
# Probes one environment from OUTSIDE after a cutover, the way a customer
# reaches it. Exit status is the verdict.
#
#   API_ORIGIN=https://staging.compute.unifyai.us \
#   ORIGIN=https://site.staging.compute.unifyai.us scripts/release/smoke.sh
#
# Two origins because the ALB routes by Host: the API name reaches only
# compute-api, the site names reach only the site container (whose /healthz is
# Caddy's own "ok"). Probing both apps on one origin fails half the probes on
# either name. API_ORIGIN defaults to ORIGIN for a host that serves both.
#
# Optional:
#   APP_ORIGIN=https://app.staging.compute.unifyai.us
#                           the customer app (ADR-0011): its pages come from
#                           the site container under an enforced CSP, and
#                           /dashboard/v1/* must reach compute-api, not the
#                           site. Unset skips it, so a stack where the app
#                           host is not applied yet still releases.
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

ORIGIN="${ORIGIN:?set ORIGIN (the site), e.g. https://site.staging.compute.unifyai.us}"
ORIGIN="${ORIGIN%/}"
API_ORIGIN="${API_ORIGIN:-$ORIGIN}"
API_ORIGIN="${API_ORIGIN%/}"
EXPECT_BUILD="${EXPECT_BUILD:-}"
APP_ORIGIN="${APP_ORIGIN:-}"
APP_ORIGIN="${APP_ORIGIN%/}"
WAIT_SECONDS="${WAIT_SECONDS:-150}"
ICON_PATH="${ICON_PATH:-/icon-v4.svg}"
# The icon must be cacheable, for at least the hour deploy/site/Caddyfile
# gives hand-versioned files (-v3) and the fixed-name icons: an hour bounds
# how stale a missed version bump can be. This floor said a day until the
# first staging release (2026-10-02, run 37054858851) failed on it against a
# container doing exactly what its Caddyfile intends.
ICON_MIN_MAX_AGE="${ICON_MIN_MAX_AGE:-3600}"

fail=0
body="$(mktemp)"; headers="$(mktemp)"
trap 'rm -f "$body" "$headers"' EXIT

ok()  { printf 'ok   %s\n' "$*"; }
bad() { printf 'FAIL %s\n' "$*"; echo "::error::smoke $API_ORIGIN $ORIGIN: $*"; fail=1; }

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

echo "--- waiting up to ${WAIT_SECONDS}s for $API_ORIGIN/healthz and $ORIGIN/healthz"
deadline=$(( $(date +%s) + WAIT_SECONDS ))
until [ "$(get "$API_ORIGIN/healthz")" = 200 ] && [ "$(get "$ORIGIN/healthz")" = 200 ]; do
  [ "$(date +%s)" -lt "$deadline" ] || break
  sleep 5
done

echo "--- compute-api at $API_ORIGIN"
expect_code "GET /healthz" 200 "$(get "$API_ORIGIN/healthz")"
expect_body "healthz names compute-api" '"service":"compute-api"'
if [ -n "$EXPECT_BUILD" ]; then
  expect_body "healthz reports build $EXPECT_BUILD" "\"build\":\"$EXPECT_BUILD\""
fi

expect_code "GET /compute/v1/skus" 200 "$(get "$API_ORIGIN/compute/v1/skus")"
ctype="$(header content-type)"
case "$ctype" in
  application/json*) ok "skus is JSON ($ctype)" ;;
  *) bad "skus content-type is '$ctype', want application/json" ;;
esac
if jq -e '.data | length > 0' "$body" >/dev/null 2>&1; then ok "skus lists at least one SKU"; else bad "skus has no .data entries"; fi
expect_body "skus carry a price" '"price_usd_per_hour"'

expect_code "GET /compute/v1/jobs without a key" 401 "$(get "$API_ORIGIN/compute/v1/jobs")"
# The code proves the 401 came from compute-api's own auth, not from a proxy
# or a default page that happens to say 401.
expect_body "401 is compute-api's missing_key" '"code":"missing_key"'

echo "--- site at $ORIGIN"
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

if [ -n "$APP_ORIGIN" ]; then
  echo "--- app at $APP_ORIGIN"
  expect_code "GET /" 200 "$(get "$APP_ORIGIN/")"
  expect_body "/ is the Unify Compute app" '<title>Unify Compute'
  csp="$(header content-security-policy)"
  case "$csp" in
    *"default-src 'none'"*) ok "app CSP is enforced" ;;
    *) bad "app Content-Security-Policy is '$csp', want the enforced app policy" ;;
  esac
  case "$csp" in
    *unsafe-inline*|*hubspot*|*hs-scripts*) bad "app CSP allows inline script or HubSpot: $csp" ;;
  esac
  if grep -q 'hs-script-loader' "$body"; then bad "app host serves the HubSpot loader"; fi
  # Routed by the ALB's host + path rule. Whatever compute-api answers for a
  # dashboard path (404 before the dashboard API exists, 401 after), it is not
  # the site's HTML: a text/html answer means the rule is missing and the site
  # took the request.
  code="$(get "$APP_ORIGIN/dashboard/v1/session")"
  ctype="$(header content-type)"
  case "$code:$ctype" in
    *:text/html*) bad "/dashboard/v1/session on the app host came from the site ($code $ctype), not compute-api" ;;
    401:*|404:*) ok "/dashboard/v1/session reaches compute-api ($code $ctype)" ;;
    *) bad "/dashboard/v1/session -> $code $ctype, want compute-api's 401 or 404" ;;
  esac
  # The job API stays on the API host.
  code="$(get "$APP_ORIGIN/compute/v1/skus")"
  if [ "$code" = 200 ] && grep -qF '"price_usd_per_hour"' "$body"; then
    bad "/compute/v1/skus answers on the app host; the job API belongs to $API_ORIGIN only"
  else
    ok "/compute/v1/skus not served on the app host ($code)"
  fi
fi

exit "$fail"
