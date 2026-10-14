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

check /start 200 "Content-Type: text/html" "Cache-Control: no-cache" "${common[@]}"
grep -q '<title>Get started — Unify Compute</title>' "$tmp/body" \
  || { echo "FAIL /start: title missing"; fail=1; }
check /start/ 308 "Location: /start"

# The template marketplace (ADR-0012), generated from compute-api/config at
# build time. It must name no price while skus.json prices are placeholders
# (AGENTS.md), and its publisher is always Unify Compute.
check /marketplace 200 "Content-Type: text/html" "Cache-Control: no-cache" "${common[@]}"
grep -q '<title>Marketplace — Unify Compute</title>' "$tmp/body" \
  || { echo "FAIL /marketplace: title missing"; fail=1; }
while read -r id; do
  grep -q "href=\"/marketplace/$id\"" "$tmp/body" \
    || { echo "FAIL /marketplace: no card for template $id"; fail=1; }
done < <(sed -n 's/^      "id": "\([a-z0-9-]*\)",$/\1/p' "$(dirname "$0")/../../compute-api/config/templates.json")
check /marketplace/ 308 "Location: /marketplace"
check /marketplace/batch-embed 200 "Content-Type: text/html" "Cache-Control: no-cache" "${common[@]}"
grep -q '<title>Batch embeddings with vLLM — Unify Compute Marketplace</title>' "$tmp/body" \
  || { echo "FAIL /marketplace/batch-embed: title missing"; fail=1; }
grep -q 'compute/v1/jobs' "$tmp/body" \
  || { echo "FAIL /marketplace/batch-embed: no run example"; fail=1; }
check /marketplace/no-such-template 404
# Each SKU price as skus.json writes it ("1.2000") and as copy would ("$1.20").
# A bare "$7" is not a price: the export's flight data is full of them.
prices=$(sed -n 's/.*"price_usd_per_hour": "\([0-9]*\)\.\([0-9][0-9]\)\([0-9]*\)".*/\1\\.\2\3|\\$\1\\.\2/p' \
  "$(dirname "$0")/../../compute-api/config/skus.json" | paste -sd'|' -)
for page in /marketplace /marketplace/batch-embed; do
  curl -s "$base$page" > "$tmp/market"
  if grep -qE "$prices|USD|per hour|/hour" "$tmp/market"; then
    echo "FAIL $page: names a price: $(grep -oE ".{0,40}($prices|USD|per hour|/hour).{0,40}" "$tmp/market" | head -1)"; fail=1
  fi
  grep -q 'Unify Compute</dd>\|published by <!-- -->Unify Compute\|published by Unify Compute' "$tmp/market" \
    || { echo "FAIL $page: publisher is not Unify Compute"; fail=1; }
done

# Log in / Sign up point at Compute's own account pages on production's app
# host (app/links.ts; ADR-0011), on staging too, because the image is shared.
# They must not point at the Unify API console, whose accounts Compute no
# longer uses.
links_ok=1
for page in / /start /privacy /marketplace; do
  curl -s "$base$page" > "$tmp/links"
  for href in 'https://app.compute.unifyai.us/signup' 'https://app.compute.unifyai.us/login' '/start'; do
    grep -q "href=\"$href\"" "$tmp/links" \
      || { echo "FAIL $page: no link to $href"; fail=1; links_ok=0; }
  done
  if grep -q 'app\.unifyapi\.ai' "$tmp/links"; then
    echo "FAIL $page: links to the Unify API console"; fail=1; links_ok=0
  fi
  # The footer's Terms link is FelixSphere's Terms and Conditions (app/links.ts
  # TERMS_URL; ADR-0011 D2). compute.unifyai.us/terms never existed.
  grep -qF '<a href="https://felixsphere.com/legal/terms">Terms</a>' "$tmp/links" \
    || { echo "FAIL $page: footer has no Terms link to https://felixsphere.com/legal/terms"; fail=1; links_ok=0; }
  if grep -q 'compute\.unifyai\.us/terms' "$tmp/links"; then
    echo "FAIL $page: links to compute.unifyai.us/terms, which does not exist"; fail=1; links_ok=0
  fi
  # UI-STANDARD 2026-10-03: FelixSphere LLC is the legal entity, named on the
  # legal page only; the Malaysian company line is gone everywhere.
  if grep -qE 'Petaling Jaya|Lebuh Bandar Utama' "$tmp/links"; then
    echo "FAIL $page: still carries the old Malaysian address"; fail=1; links_ok=0
  fi
  if [ "$page" = /privacy ]; then
    grep -qF 'Operated by FelixSphere LLC · 6 Karen Ct, CA 94010, United States' "$tmp/links" \
      || { echo "FAIL $page: footer has no \"Operated by FelixSphere LLC\" line"; fail=1; links_ok=0; }
  elif grep -qF 'Operated by FelixSphere LLC' "$tmp/links"; then
    echo "FAIL $page: footer names the legal entity; only legal pages do"; fail=1; links_ok=0
  fi
done
curl -s "$base/start" > "$tmp/links"
for href in 'mailto:contactus@felixsphere.com?subject=Unify%20Compute%20early%20access' 'https://api.compute.unifyai.us/compute/v1/openapi.json'; do
  grep -qF "href=\"$href\"" "$tmp/links" \
    || { echo "FAIL /start: no link to $href"; fail=1; links_ok=0; }
done
# Self-serve accounts are open; the guide must say so and must still be
# honest that job execution is enabled per account.
for copy in 'Create an account' 'uck_' '403 compute_not_enabled' '503 upstream_unavailable' 'read -rs UNIFY_KEY'; do
  grep -qF "$copy" "$tmp/links" \
    || { echo "FAIL /start: copy \"$copy\" missing"; fail=1; links_ok=0; }
done
for stale in 'id="early-access"' 'not open yet' 'Self-serve sign-up is coming'; do
  if grep -qF "$stale" "$tmp/links"; then
    echo "FAIL /start: still says \"$stale\""; fail=1; links_ok=0
  fi
done
[ "$links_ok" = 1 ] && echo "ok   sign-up, log-in and quick-start links"

check /does-not-exist 404 "Content-Type: text/html" "Cache-Control: no-cache" "${common[@]}"
grep -q 'This page could not be found' "$tmp/body" \
  || { echo "FAIL 404: not the export's 404 page"; fail=1; }

# The ten languages (UI-STANDARD "Languages"): every public page is a real
# file under its prefix, with <html lang> (and dir="rtl" for Arabic), its own
# canonical and all eleven hreflang links. ~/unifyai-brand-kit/check-i18n.sh
# audits the same thing against a live host.
for lang in zh:zh-Hans zh-hant:zh-Hant ja:ja ko:ko es:es fr:fr de:de pt-br:pt-BR ar:ar; do
  prefix=${lang%%:*}; code=${lang#*:}
  for page in "" /start /marketplace /marketplace/batch-embed /privacy; do
    check "/$prefix$page" 200 "Content-Type: text/html" "Cache-Control: no-cache" "${common[@]}"
    grep -q "<html lang=\"$code\"" "$tmp/body" || { echo "FAIL /$prefix$page: <html lang> is not $code"; fail=1; }
    grep -q "<link rel=\"canonical\" href=\"https://compute.unifyai.us/$prefix$page\"" "$tmp/body" \
      || { echo "FAIL /$prefix$page: canonical is not its own URL"; fail=1; }
    [ "$(grep -o 'rel="alternate" hrefLang="' "$tmp/body" | wc -l)" -eq 11 ] \
      || { echo "FAIL /$prefix$page: not 10 hreflang links plus x-default"; fail=1; }
  done
  check "/$prefix/" 308 "Location: /$prefix"
done
check /ar 200
grep -q '<html lang="ar" dir="rtl"' "$tmp/body" || { echo "FAIL /ar: not dir=\"rtl\""; fail=1; }
check /zh/no-such-page 404 "Content-Type: text/html"
check /sitemap.xml 200
[ "$(grep -c '<loc>' "$tmp/body")" -eq "$(( (5 + $(grep -c '^      "id": ' "$(dirname "$0")/../../compute-api/config/templates.json") - 1) * 10 ))" ] \
  || { echo "FAIL /sitemap.xml: does not list every page in every language"; fail=1; }
check /robots.txt 200

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
for page in /privacy /start /marketplace /marketplace/batch-embed / /zh /ar/start; do
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
# The pages A7b and A7c build (dashboard/), at the paths the mailer links to
# (compute-api/internal/mailer/messages.go), and the title each must carry.
# /jobs/<id> is one static page that Caddy serves for every job id.
app_pages=(
  "/|Unify Compute"
  "/keys|API keys — Unify Compute"
  "/jobs|Jobs — Unify Compute"
  "/jobs/job_01J8Q6ZK3R7Y9W2X4V6T8S0A1B|Job — Unify Compute"
  "/marketplace|Marketplace — Unify Compute"
  "/listings|My listings — Unify Compute"
  "/usage|Usage — Unify Compute"
  "/billing|Billing — Unify Compute"
  "/account|Account — Unify Compute"
  "/members|Members — Unify Compute"
  "/signup|Sign up — Unify Compute"
  "/login|Log in — Unify Compute"
  "/verify|Check your email — Unify Compute"
  "/forgot-password|Reset your password — Unify Compute"
  "/reset-password|Reset your password — Unify Compute"
  "/accept-invitation|Accept invitation — Unify Compute"
)
for app_host in app.compute.unifyai.us app.staging.compute.unifyai.us; do
  export CURL_ARGS="-H Host:$app_host"
  for entry in "${app_pages[@]}"; do
    path=${entry%%|*}; title=${entry#*|}
    check "$path" 200 "Content-Type: text/html" "Cache-Control: no-cache" "${app_common[@]}"
    app_headers_clean "$app_host $path"
    grep -qF "<title>$title</title>" "$tmp/body" \
      || { echo "FAIL $app_host $path: title is not '$title'"; fail=1; }
    if grep -q 'hs-script-loader' "$tmp/body"; then
      echo "FAIL $app_host $path: HubSpot loader on the app host"; fail=1
    fi
    # The enforced CSP would block these anyway; failing here says why the
    # page is broken instead of leaving a blank form in the browser.
    if grep -qE '<script>|<script type="module">|<style| style="| on[a-z]+="' "$tmp/body"; then
      echo "FAIL $app_host $path: inline script, style or event handler"; fail=1
    fi
    if grep -qE '<(script|link|img|iframe)[^>]+(src|href)="(https?:)?//' "$tmp/body"; then
      echo "FAIL $app_host $path: loads something from another origin"; fail=1
    fi
    grep -qE '<script type="module" crossorigin src="/assets/main-[A-Za-z0-9_-]+\.js">' "$tmp/body" \
      || { echo "FAIL $app_host $path: page script missing"; fail=1; }
  done

  # Every form compute-api checks the honeypot on (`website`: signup, resend,
  # login, forgot; ADR-0011 D5) carries it in place of Turnstile, hidden from
  # people and from screen readers.
  for path in /login /verify /forgot-password /signup; do
    check "$path" 200
    grep -qE '<div class="hp" aria-hidden="true">' "$tmp/body" \
      || { echo "FAIL $app_host $path: honeypot wrapper missing or not aria-hidden"; fail=1; }
    grep -qE '<input id="website" name="website" type="text" tabindex="-1" autocomplete="off"' "$tmp/body" \
      || { echo "FAIL $app_host $path: honeypot field missing or reachable by Tab"; fail=1; }
    grep -q 'challenges.cloudflare.com' "$tmp/body" \
      && { echo "FAIL $app_host $path: Turnstile is off (ADR-0011 D5)"; fail=1; }
  done
  # Terms and Conditions are FelixSphere's (app/links.ts TERMS_URL), both in
  # the sign-up consent line and under the card on every auth page.
  grep -qF '<p class="legal">By signing up you agree to the <a href="https://felixsphere.com/legal/terms">Terms and Conditions</a> and <a href="https://compute.unifyai.us/privacy">Privacy Policy</a>.</p>' "$tmp/body" \
    || { echo "FAIL $app_host /signup: consent line does not link Terms and Conditions to https://felixsphere.com/legal/terms"; fail=1; }
  for path in /signup /login; do
    check "$path" 200
    grep -qF '<a href="https://compute.unifyai.us/privacy">Privacy</a> · <a href="https://felixsphere.com/legal/terms">Terms</a></p>' "$tmp/body" \
      || { echo "FAIL $app_host $path: footer Terms link is not https://felixsphere.com/legal/terms"; fail=1; }
  done
  for entry in "${app_pages[@]}"; do
    path=${entry%%|*}
    if curl -s -H "Host:$app_host" "$base$path" | grep -q 'compute\.unifyai\.us/terms'; then
      echo "FAIL $app_host $path: links to compute.unifyai.us/terms, which does not exist"; fail=1
    fi
  done
  check /signup 200
  [ "$(grep -o 'id="password_confirm"' "$tmp/body" | wc -l)" -eq 1 ] \
    || { echo "FAIL $app_host /signup: no Confirm password field"; fail=1; }
  [ "$(grep -o 'class="toggle"' "$tmp/body" | wc -l)" -eq 2 ] \
    || { echo "FAIL $app_host /signup: not one show/hide toggle per password field"; fail=1; }
  js=$(grep -oE '/assets/main-[A-Za-z0-9_-]+\.js' "$tmp/body" | head -1)
  css=$(grep -oE '/assets/app-[A-Za-z0-9_-]+\.css' "$tmp/body" | head -1)
  check "${js:-/assets/missing.js}" 200 "Content-Type: text/javascript" "Cache-Control: public, max-age=31536000, immutable" "${app_common[@]}"
  check "${css:-/assets/missing.css}" 200 "Content-Type: text/css" "Cache-Control: public, max-age=31536000, immutable"
  # The fonts are self-hosted from the same build (UI-STANDARD "Fonts").
  font=$(grep -oE 'assets/instrument-sans-latin-400-normal-[A-Za-z0-9_-]+\.woff2' "$tmp/body" | head -1)
  if [ -z "$font" ]; then
    font=$(grep -oE '/assets/instrument-sans-latin-400-normal-[A-Za-z0-9_-]+\.woff2' <(curl -s -H "Host:$app_host" "$base$css") | head -1)
  fi
  check "/${font#/}" 200 "Content-Type: font/woff2"

  check /brand/unifyai-lockup-horizontal-black.svg 200 "Content-Type: image/svg+xml"
  check /favicon.ico 200 "Content-Type: image/x-icon"
  check /icon-v4.svg 200 "Content-Type: image/svg+xml"
  # The marketing pages are not on this host.
  check /privacy 404 "Content-Type: text/html" "${app_common[@]}"
  app_headers_clean "$app_host /privacy"
  check /does-not-exist 404 "Content-Type: text/html" "Cache-Control: no-cache" "${app_common[@]}"
  app_headers_clean "$app_host 404"
  grep -q 'Page not found' "$tmp/body" \
    || { echo "FAIL $app_host 404: not the app's 404 page"; fail=1; }
  check /login/ 308 "Location: /login"
  check /jobs/ 308 "Location: /jobs"
  # Only one path segment after /jobs is a job page.
  check /jobs/job_1/logs 404 "Content-Type: text/html" "${app_common[@]}"
  # The signed-in pages carry the app shell (UI-STANDARD "App shell"): the
  # sidebar with this page marked current, the phone menu button and the
  # account button.
  for entry in "/|/" "/keys|/keys" "/jobs/job_1|/jobs" "/account|/account"; do
    path=${entry%%|*}; current=${entry#*|}
    check "$path" 200
    grep -q "<a href=\"$current\" aria-current=\"page\">" "$tmp/body" \
      || { echo "FAIL $app_host $path: sidebar does not mark $current as the current page"; fail=1; }
    grep -qE '<button class="menu-button" type="button" id="menu-button" aria-label="Menu" aria-expanded="false" aria-controls="app-nav">' "$tmp/body" \
      || { echo "FAIL $app_host $path: no phone menu button"; fail=1; }
    grep -q 'id="account-button"' "$tmp/body" \
      || { echo "FAIL $app_host $path: no account button"; fail=1; }
  done
  # Build output that is not a page.
  check /.vite/manifest.json 404
done
unset CURL_ARGS

exit "$fail"
