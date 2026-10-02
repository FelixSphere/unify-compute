#!/usr/bin/env bash
# Asserts what the site container must do, against a running instance of it.
#   deploy/site/smoke.sh http://127.0.0.1:8080 <SITE_BUILD_ID the image was built with>
# Used by CI on every push and by publish-image.yml before anything is pushed.
set -euo pipefail

base=${1:?usage: smoke.sh BASE_URL SITE_BUILD_ID}
build_id=${2:?usage: smoke.sh BASE_URL SITE_BUILD_ID}
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
check /favicon-32-v3.png 200 "Content-Type: image/png" "Cache-Control: public, max-age=3600"
check /icon-v3.svg 200 "Content-Type: image/svg+xml" "Cache-Control: public, max-age=3600"
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

CURL_ARGS="-H RSC:1" check "/privacy?_rsc=x" 200 \
  "Content-Type: text/x-component" "X-Vinext-RSC-Compatibility-Id: $build_id" "Vary: RSC"
CURL_ARGS="-H RSC:1" check "/?_rsc=x" 200 "Content-Type: text/x-component"

exit "$fail"
