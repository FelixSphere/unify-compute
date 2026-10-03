#!/usr/bin/env bash
# After the DNS cutover: prove the public domain is answered by this
# environment's load balancer, not by the old host.
#
#   DOMAIN=compute.unifyai.us ALB_DNS_NAME=<alb>.elb.amazonaws.com \
#     scripts/release/served-by-aws.sh
#
# Two independent signals, both required:
#   1. the domain resolves to (some of) the ALB's addresses. An alias record
#      returns the ALB's own IPs, so the sets intersect when DNS points here.
#      Skipped, with a note, when ALB_DNS_NAME is unset (e.g. a CDN in front).
#   2. the domain is answered by this stack's site container: its /healthz
#      is Caddy's literal "ok", and the response carries the
#      Strict-Transport-Security header the ALB adds. compute.unifyai.us is a
#      site name; the ALB routes it to the site, never to compute-api, so
#      compute-api's /healthz cannot be the signal here. OpenAI Sites had no
#      /healthz and sent no HSTS when checked on 2026-10-02.
set -euo pipefail

DOMAIN="${DOMAIN:?set DOMAIN}"
ALB_DNS_NAME="${ALB_DNS_NAME:-}"
fail=0

resolve() { dig +short A "$1" | grep -E '^[0-9.]+$' | sort -u; }

if [ -n "$ALB_DNS_NAME" ]; then
  domain_ips="$(resolve "$DOMAIN")"
  alb_ips="$(resolve "$ALB_DNS_NAME")"
  echo "$DOMAIN -> $(echo "$domain_ips" | tr '\n' ' ')"
  echo "$ALB_DNS_NAME -> $(echo "$alb_ips" | tr '\n' ' ')"
  if [ -n "$domain_ips" ] && [ -n "$(comm -12 <(echo "$domain_ips") <(echo "$alb_ips"))" ]; then
    echo "ok   $DOMAIN resolves to the ALB"
  else
    echo "::error::$DOMAIN does not resolve to $ALB_DNS_NAME"; fail=1
  fi
else
  echo "note: ALB_DNS_NAME unset; DNS comparison skipped, relying on the route check"
fi

hdrs="$(mktemp)"; trap 'rm -f "$hdrs"' EXIT
body="$(curl -sS --max-time 20 -D "$hdrs" "https://$DOMAIN/healthz" 2>/dev/null || true)"
if [ "$body" = ok ]; then
  echo "ok   https://$DOMAIN/healthz is the site container"
else
  echo "::error::https://$DOMAIN/healthz is not the site container: $(printf '%s' "$body" | head -c 200)"; fail=1
fi
if grep -qi '^strict-transport-security: *max-age=' "$hdrs"; then
  echo "ok   https://$DOMAIN carries the ALB's Strict-Transport-Security"
else
  echo "::error::https://$DOMAIN has no Strict-Transport-Security; not answered by the ALB"; fail=1
fi

exit "$fail"
