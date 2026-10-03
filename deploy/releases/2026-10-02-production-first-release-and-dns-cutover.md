---
environment: production
action: release
outcome: success
run: 37058109546
commit: 354d9029499fe7713a06d15a6cbb0566cb7a2535
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:ec4bd964372acf9724365273204c556414a3090ee68035037b0c41b21e7d9902
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:cd7077d3462abe68b17d33b39437e34121f3a2d7333aee04b9844f854f9bd018
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:ec4bd964372acf9724365273204c556414a3090ee68035037b0c41b21e7d9902
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:cd7077d3462abe68b17d33b39437e34121f3a2d7333aee04b9844f854f9bd018
hotfix: false
---

# 2026-10-02 — go-live: first production release, and compute.unifyai.us moves to AWS

Same digests as staging (gate:
`staging ran exactly these digests: …/runs/37055453977`). Built from main
`a8562dd`. What shipped is listed in
[2026-10-02-staging-first-release](2026-10-02-staging-first-release.md).

## Before the release

- Production applied from main `913bddb`: 48 resources, VPC 10.71.0.0/16,
  ALB deletion protection on, DLM daily snapshots, `manage_site_dns = false`.
  The only records were `api.compute` and `site.compute`, plus ACM validation children.
- **First boot crash-looped compute-api**:
  `console bridge secret must be at least 32 bytes`. user-data wrote
  `CONSOLE_URL=https://app.unifyapi.ai` next to the deliberately empty
  bridge secret. Fixed by #34 (CONSOLE_URL empty while `bridge_enabled =
  false`). The never-released instance `i-062e9876731f1e3fe` was rebuilt by
  ADR-0009's procedure (unit stopped, `-replace`: 5 add / 5 destroy, data
  volume and EIP kept) as `i-07dd2f6d313a0dafd`. It booted the baseline pins
  (= these digests) with compute-api dark.
- **Order deviation:** ADR-0009 says a rebuild runs on staging before
  production. This one ran on production first, because production was the
  host that could not start. Staging was rebuilt with the same template right
  afterwards (`i-0780af3b0bfdc3383` became `i-06125c23285803d7a`). It came
  up with all five containers, and `make smoke` was clean.

## The release

`NOOP`: the rebuilt host already ran these digests. Health was verified, nothing
changed, and no dump was taken. Smoke passed against `https://api.compute.unifyai.us` and
`https://site.compute.unifyai.us`. The host has no `PREVIOUS_RELEASE`, so
`mode=rollback` refuses on production until a second release.

## DNS cutover (ADR-0009)

1. Before: `compute.unifyai.us` was resolved to the ALB directly (`curl
   --noproxy '*' --resolve`; the operator machine's HTTPS proxy ignores
   `--resolve`). It served title "Unify Compute — …", `/healthz` `ok`,
   `/privacy` 200, and HSTS. The ALB's certificate covers `compute.unifyai.us`.
2. Route53 change `C07235422LT740FCOOAEW`, submitted 2026-10-02T20:06:31Z and
   INSYNC at 20:06:58Z. One batch: DELETE the CNAME to
   `custom-domains.chatgpt.site.`, CREATE an A alias to
   `unify-compute-production-1999053028.us-east-1.elb.amazonaws.com`. The
   `_cf-custom-hostname` and `_openai-site-verification` TXT records were
   left in place.
3. `manage_site_dns = true`, with the record imported into
   `module.stack.aws_route53_record.app["compute.unifyai.us"]` (see the PR
   that carries this record). GitHub production variables:
   `DNS_CUTOVER=done`, `PUBLIC_ORIGIN=https://compute.unifyai.us`.

## What was exercised on https://compute.unifyai.us after the cutover

- Authoritative NS and 1.1.1.1 resolve to the ALB's two addresses.
  `scripts/release/served-by-aws.sh`: all ok.
- `http://` gives 301 to https. `Strict-Transport-Security: max-age=31536000`
  appears once. CSP is report-only.
- Title and manifest name "Unify Compute". Header: lockup, then
  `product-divider`, then "Compute".
- `/favicon.ico?v=3`, `/icon-v3.svg`, `/favicon-32-v3.png`: 200, byte-identical
  to `public/` at a8562dd.
- No "unifyAI" and no "UnifyAI Compute" on `/`, `/privacy` or the 404 page. No
  FelixSphere link anywhere. The only FelixSphere text is
  `contactus@felixsphere.com` on `/privacy`.
- `node deploy/site/browser-check.mjs https://compute.unifyai.us`: hydrated
  with the cookie notice shown. Footer and cookie-notice "Privacy" links
  navigated client-side. The HubSpot loader (`js.hs-scripts.com/46127314.js`)
  is present once before and after navigation. There were no console errors
  or failed requests.
- `/privacy` 200, `/privacy.txt` 200 `text/plain`.

## What this does not prove

- That the HubSpot chat opens on this domain. Only the loader was checked, in
  headless Chrome, and the widget was not observed.
- That every resolver has moved. The old CNAME had a 300 s TTL, and 8.8.8.8
  timed out from the checking machine.
- Anything about billing. compute-api is dark in production, and job submit
  is 501.
