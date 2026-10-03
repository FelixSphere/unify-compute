---
environment: staging
action: release
outcome: success
run: 37055453977
commit: 2c63b3ac63cbdd0aa947de23f762420c12ef5471
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:ec4bd964372acf9724365273204c556414a3090ee68035037b0c41b21e7d9902
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:cd7077d3462abe68b17d33b39437e34121f3a2d7333aee04b9844f854f9bd018
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:ec4bd964372acf9724365273204c556414a3090ee68035037b0c41b21e7d9902
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:cd7077d3462abe68b17d33b39437e34121f3a2d7333aee04b9844f854f9bd018
hotfix: false
---

# 2026-10-02 — go-live: first staging release

A re-run of the same digests after #32 corrected the smoke icon floor. The
host already ran them (cut over by run 37054858851, see
[2026-10-02-staging-first-start-smoke-failed](2026-10-02-staging-first-start-smoke-failed.md)),
so the cutover was `NOOP`: health verified, nothing changed, no backup. Smoke
passed in full, and the run uploaded
`release-manifest-staging-ec4bd964372a-cd7077d3462a`, which is what makes these
digests eligible for production.

## What shipped

Both images were built from main `a8562dd`. This is the first release of
either on any host, so "what shipped" is everything up to that commit.

- compute-api: the skeleton described in AGENTS.md. Health, SKU catalogue,
  upstreams, and the job API, where submit returns `501`. **Pricing:**
  `compute-api/config/skus.json` as of `124e20c` is public for the first time
  at `/compute/v1/skus`. Nothing can be billed: no job runs, and the console
  bridge is not implemented on the console side.
- site: main at `a8562dd`, including #26 (vinext 1.0.1, client-side link
  navigation and the `.txt` navigation payloads) and the native-platform
  export build (#28).

## What was exercised (from outside, after the run)

- `scripts/release/smoke.sh` in the run: compute-api at
  `https://staging.compute.unifyai.us` (healthz reports build a8562dd, skus
  priced, jobs without a key give 401 `missing_key`), and the site at
  `https://site.staging.compute.unifyai.us` (title, /privacy, icon).
- `make smoke` with the console: all ok, including console `/api/status`
  `success:true`, a valid submit returning 501, and an unknown SKU returning 400. The
  console-host isolation check was corrected in the same PR as this record.
  The console SPA answers 200 with its own index.html for any path, which
  is not compute-api.
- `node deploy/site/browser-check.mjs https://site.staging.compute.unifyai.us`
  (headless Chrome): hydrated, cookie notice shown. Footer "Privacy" and
  cookie-notice "Privacy Policy" both navigated client-side with no reload, and back
  navigation worked. The HubSpot loader is present once before and after
  navigating, and there were no console errors or failed requests.
- `/privacy.txt` and `/index.txt`: 200 `text/plain; charset=utf-8`,
  `Cache-Control: no-cache`.
- `/favicon.ico?v=3`, `/icon-v3.svg`, `/favicon-32-v3.png`: 200.
  `/manifest-v3.webmanifest` is `application/manifest+json`, name "Unify Compute".
- Headers: `Strict-Transport-Security` once, CSP report-only present,
  http→https 301 on all three names.

## What this does not prove

- That a job can be submitted, run or billed. Submit is 501 by design.
- That the HubSpot chat opens. The chatflow targets compute.unifyai.us, and
  the loader is all staging can show.
- Anything about backups. A first start takes no dump, and this run was NOOP.
  The first release that replaces images on staging is the first real test
  of the dump and its S3 copy.
