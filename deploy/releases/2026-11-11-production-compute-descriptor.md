---
environment: production
action: release
outcome: success
run: 43060309921
commit: 47f54d8bb6ec09fc42e2b182c69b6cbfbb48922a
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:15cf14fb57d9a6a12e3483a07865463e33ba4944d49cc8c9225ef1510835c56f
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:b31260dab45bb555c0dd2323c4ec175f6aa4654bd22fb34ec5883db2e618071d
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:15cf14fb57d9a6a12e3483a07865463e33ba4944d49cc8c9225ef1510835c56f
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:a8281c7835a2c2b4646e838f1d6b4882342476eb8c3eda1143a481dc56ee3b9c
hotfix: false
---

# 2026-11-11: the header descriptor reads "Compute" (#155), on production

Same digests as staging (`2026-11-11-staging-compute-descriptor.md`, run
43059641149, where every check passed). The staging-first gate passed. The
operator asked for this change to go live.

## What shipped

See the staging record. Site `a8281c78…` → `b31260da…` (47f54d8, #155);
compute-api `15cf14fb…` unchanged. No migration (schema 0015), no change to
`skus.json`, `platform.json`, env vars or Terraform.

## Before / after

- Before (status run 43060194188): `15cf14fb…` / `a8281c78…`, matching
  the bonus-refund-requests record. IMDS lockdown present.
- Backup `/data/backups/20261111T163913Z-43060309921` (compute, 30 tables),
  copied to
  `s3://unify-compute-production-backups-871376691052/production/releases/20261111T163913Z-43060309921/`.
- compute-api, postgres and site had 0 restarts after `up -d` and after
  30 s. Smoke passed in full, including `compute.unifyai.us` → ALB,
  `compute.unifyai.us/healthz` is the site container, and HSTS.
  `/healthz` reports build `792b4ea…`.
- PREVIOUS_RELEASE is `15cf14fb…` / `a8281c78…`: `mode=rollback` is a
  site-only step, no dump restore.

## What was exercised

The same checks as staging, from outside:

1. **Site** `https://compute.unifyai.us/` and `https://site.compute.unifyai.us/`,
   plus `/zh` and `/ja`: `.product-name` is exactly `Compute`; no
   "Flatkey".
2. **App** `https://app.compute.unifyai.us/login` and `/signup` (signed-out
   top bar and brand block) and `/`, `/jobs`, `/billing`, `/keys`,
   `/members` (signed-in shell): descriptor `Compute`.
3. **No "Flatkey" served:** those pages and all 15 JS/CSS assets they
   reference, case-insensitive: zero matches.

All passed.

## What this does not prove

As on staging: the signed-in shell was checked as served HTML, not rendered
in a browser with a real session, and nobody looked at the rendered header.
