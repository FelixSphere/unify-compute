---
environment: staging
action: release
outcome: success
run: 43059641149
commit: 47f54d8bb6ec09fc42e2b182c69b6cbfbb48922a
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:15cf14fb57d9a6a12e3483a07865463e33ba4944d49cc8c9225ef1510835c56f
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:b31260dab45bb555c0dd2323c4ec175f6aa4654bd22fb34ec5883db2e618071d
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:15cf14fb57d9a6a12e3483a07865463e33ba4944d49cc8c9225ef1510835c56f
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:a8281c7835a2c2b4646e838f1d6b4882342476eb8c3eda1143a481dc56ee3b9c
hotfix: false
---

# 2026-11-11: the header descriptor reads "Compute" (#155), on staging

## What shipped

Site only. `a8281c78…` (792b4ea) → `b31260da…` (47f54d8, publish run
43059317487, after CI run 43058462200 passed on 47f54d8). Between the two
builds, on main:

- `397d01f` Merge #152: Terraform root `deploy/aws/shared/cicd-agent`, a
  Makefile `TF_STACKS` entry, and a cicd.md note. Not in either image and
  not applied by this release.
- `1360754` Merge #153: the bonus-refund-requests records. Docs only.
- `47f54d8` Merge #155: the descriptor on the marketing site
  (`app/header-nav.tsx`) and on the app pages (`dashboard/vite.config.ts`:
  signed-in shell, signed-out top bar, standalone brand block) reads
  "Compute" instead of "Compute & Flatkey".

compute-api is unchanged: `15cf14fb…` (792b4ea), and nothing under
`compute-api/` changed since. No change to `skus.json`, `platform.json`,
schema (still 0015), env vars or Terraform.

## Before / after

- Before (status run 43059364324): `15cf14fb…` / `a8281c78…`, matching
  the bonus-refund-requests record. IMDS lockdown present.
- Backup `/data/backups/20261111T163532Z-43059641149` (compute 30 tables,
  console 59 tables), copied to
  `s3://unify-compute-staging-backups-871376691052/staging/releases/20261111T163532Z-43059641149/`.
- compute-api, console, postgres, redis and site had 0 restarts after
  `up -d` and after 30 s. Smoke passed in full; `/healthz` reports build
  `792b4ea…`.
- PREVIOUS_RELEASE is `15cf14fb…` / `a8281c78…`: `mode=rollback` is a
  site-only step, no dump restore.

## What was exercised

From outside, with curl, after the cutover:

1. **Site** `https://site.staging.compute.unifyai.us/`, `/zh`, `/ja` and the
   404 page: the `.product-name` span is exactly `Compute`; no
   "Flatkey" in any of them (case-insensitive).
2. **App** `https://app.staging.compute.unifyai.us/login` and `/signup`
   carry the signed-out top bar (`#topbar-brand`) and the standalone brand
   block, both `<span class="descriptor">Compute</span>`. `/`, `/jobs`,
   `/billing`, `/keys` and `/members` carry the signed-in shell
   (`common.overview-link`), descriptor `Compute`.
3. **No "Flatkey" served:** every page above plus all 15 JS/CSS assets they
   reference scanned case-insensitively: zero matches. `git grep -i
   flatkey` over `app components public dashboard lib deploy/site` at
   47f54d8 is also empty.

All passed.

## What this does not prove

- The signed-in shell was read as the static HTML the app host serves, not
  rendered in a browser with a real session (no staging session was used).
  The descriptor is in that static partial, so a session cannot change it,
  but nobody looked at the rendered pixels.
- Translated strings loaded at runtime were covered only as far as they sit
  in the scanned assets.
