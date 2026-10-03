#!/usr/bin/env bash
# Asserts what the site container must do, against a running instance of it.
#   deploy/site/smoke.sh http://127.0.0.1:8080
# Used by CI on every push and by publish-image.yml before anything is pushed.
set -euo pipefail

base=${1:?usage: smoke.sh BASE_URL}
fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# check PATH STATUS [Header: substring]...   (extra curl args via CURL_ARGS)
check() {
  local path=$1 want=$2; shift 2
  local got
  # shellcheck disable=SC2086
  got=$(curl -s -o "$tmp/body" -D "$tmp/headers" -w '%{http_code}' ${CURL_ARGS:-} "$base$path" || true)
  if [ "$got" != "$want" ]; then
    echo "FAIL $path: status $got, want $want"; fail=1; return
  fi
  local expect name value line
  for expect in "$@"; do
    name=${expect%%:*}; value=${expect#*: }
    line=$(grep -i "^$name:" "$tmp/headers" | tr -d '\r' || true)
    if [ -z "$line" ] || [[ "${line#*: }" != *"$value"* ]]; then
      echo "FAIL $path: header $name is '${line#*: }', want it to contain '$value'"; fail=1
    fi
  done
  echo "ok   $path $got"
}

common=(
  "X-Content-Type-Options: nosniff"
  "Referrer-Policy: strict-origin-when-cross-origin"
  "Content-Security-Policy-Report-Only: https://*.hs-scripts.com"
)

check /healthz 200 "Cache-Control: no-store"
# The ALB sets Strict-Transport-Security (ADR-0009). If the container sent it
# as well, browsers would receive the header twice.
if grep -qi '^strict-transport-security:' "$tmp/headers"; then
  echo "FAIL /healthz: container sends Strict-Transport-Security; the ALB owns it"; fail=1
fi
check / 200 "Content-Type: text/html" "Cache-Control: no-cache" "${common[@]}"
grep -q '<title>Unify Compute — On-demand inference &amp; GPU compute</title>' "$tmp/body" \
  || { echo "FAIL /: title missing"; fail=1; }
[ "$(grep -o 'id="hs-script-loader"' "$tmp/body" | wc -l)" -eq 1 ] \
  || { echo "FAIL /: HubSpot loader is not present exactly once"; fail=1; }
css=$(grep -oE '/_next/static/css/[^"?]+\.css' "$tmp/body" | head -1)

check /privacy 200 "Content-Type: text/html" "Cache-Control: no-cache" "${common[@]}"
grep -q '<title>Privacy Policy — Unify Compute</title>' "$tmp/body" \
  || { echo "FAIL /privacy: title missing"; fail=1; }
check /privacy/ 308 "Location: /privacy"

check /does-not-exist 404 "Content-Type: text/html" "Cache-Control: no-cache" "${common[@]}"
grep -q 'This page could not be found' "$tmp/body" \
  || { echo "FAIL 404: not the export's 404 page"; fail=1; }

[ -n "$css" ] || { echo "FAIL: no hashed stylesheet referenced from /"; fail=1; css=/missing.css; }
check "$css" 200 "Content-Type: text/css" "Cache-Control: public, max-age=31536000, immutable"

check /favicon.ico 200 "Content-Type: image/x-icon" "Cache-Control: public, max-age=3600"
check /favicon-32-v4.png 200 "Content-Type: image/png" "Cache-Control: public, max-age=3600"
check /icon-v4.svg 200 "Content-Type: image/svg+xml" "Cache-Control: public, max-age=3600"
check /apple-touch-icon.png 200 "Content-Type: image/png"
check /manifest-v3.webmanifest 200 "Content-Type: application/manifest+json" "Cache-Control: public, max-age=3600"
check /og-image-v3.png 200 "Content-Type: image/png" "Cache-Control: public, max-age=3600"
# Email clients and image proxies send both spellings; neither may redirect.
check /email/unifyai-lockup-black@2x.png 200 "Content-Type: image/png"
check /email/unifyai-lockup-black%402x.png 200 "Content-Type: image/png"
check /email/unifyai-mark-black@2x.png 200 "Content-Type: image/png"

# Build artifacts that are not part of the site.
check /.vite/manifest.json 404
check /vinext-client-entry-manifest.json 404
# No directory listing.
check /brand 404

# Client-side navigation: the router fetches the page's flight payload by file
# name and still sends `RSC: 1`. A rewrite keyed on that header turned these
# into 404s and every link click into a full page load. The payload must come
# from the same build as the HTML, or the router discards it.
privacy_version=$(curl -s "$base/privacy" | grep -oE 'deploymentVersion\\?":\\?"[0-9a-f-]+' | grep -oE '[0-9a-f-]{36}' | head -1)
for page in /privacy /; do
  payload=$page.txt; [ "$page" = / ] && payload=/index.txt
  CURL_ARGS="-H RSC:1" check "$payload" 200 \
    "Content-Type: text/plain" "Cache-Control: no-cache" "${common[@]}"
  head -c 2 "$tmp/body" | grep -q '^0:' \
    || { echo "FAIL $payload: not a flight payload"; fail=1; }
  if [ -z "$privacy_version" ] || ! grep -q "\"deploymentVersion\":\"$privacy_version\"" "$tmp/body"; then
    echo "FAIL $payload: deploymentVersion does not match the HTML's ('$privacy_version')"; fail=1
  fi
done

# The app host (ADR-0011 section 5): the same container answers it from its own
# Caddy block, chosen by Host. Both environments' names, since one image
# serves both. On AWS the ALB sends /dashboard/v1/* on this host to
# compute-api, so it is not probed here; scripts/release/smoke.sh does.
app_common=(
  "Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'"
  "Referrer-Policy: no-referrer"
  "X-Content-Type-Options: nosniff"
  "X-Frame-Options: DENY"
  "Cross-Origin-Opener-Policy: same-origin"
)
# The marketing site's report-only HubSpot policy and loader must not reach
# account pages, and the enforced policy must not have been loosened.
app_headers_clean() {
  local label=$1
  if grep -qi '^content-security-policy-report-only:' "$tmp/headers"; then
    echo "FAIL $label: sends the marketing site's report-only CSP"; fail=1
  fi
  if grep -i '^content-security-policy:' "$tmp/headers" | grep -qiE "unsafe-inline|unsafe-eval|hubspot|hs-scripts|cloudflare"; then
    echo "FAIL $label: CSP allows inline script or a third party: $(grep -i '^content-security-policy:' "$tmp/headers")"; fail=1
  fi
  if grep -qi '^set-cookie:' "$tmp/headers"; then
    echo "FAIL $label: the site container sets a cookie; only compute-api may"; fail=1
  fi
}
for app_host in app.compute.unifyai.us app.staging.compute.unifyai.us; do
  export CURL_ARGS="-H Host:$app_host"
  check / 200 "Content-Type: text/html" "Cache-Control: no-cache" "${app_common[@]}"
  app_headers_clean "$app_host /"
  grep -q '<title>Unify Compute</title>' "$tmp/body" \
    || { echo "FAIL $app_host /: not the app placeholder"; fail=1; }
  if grep -q 'hs-script-loader' "$tmp/body"; then
    echo "FAIL $app_host /: HubSpot loader on the app host"; fail=1
  fi
  check /app.css 200 "Content-Type: text/css" "${app_common[@]}"
  check /unifyai-tokens.css 200 "Content-Type: text/css"
  check /brand/unifyai-lockup-horizontal-black.svg 200 "Content-Type: image/svg+xml"
  check /favicon.ico 200 "Content-Type: image/x-icon"
  # The marketing pages are not on this host.
  check /privacy 404 "Content-Type: text/html" "${app_common[@]}"
  app_headers_clean "$app_host /privacy"
  check /does-not-exist 404 "Content-Type: text/html" "Cache-Control: no-cache" "${app_common[@]}"
  app_headers_clean "$app_host 404"
  grep -q 'Page not found' "$tmp/body" \
    || { echo "FAIL $app_host 404: not the app's 404 page"; fail=1; }
  check /index/ 308 "Location: /index"
done
unset CURL_ARGS

exit "$fail"
