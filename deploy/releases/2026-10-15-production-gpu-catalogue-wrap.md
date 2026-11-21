---
environment: production
action: release
outcome: success
run: 38725660008
commit: 0e2b69439e9f8a6d8173dd94b16a1ae3ed6ccc33
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:05ba021ce498d3b55597392f135f6cb70373722984ab8e81d9e466bfa64153ce
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:c2899e5f4815bf0dd4574ac0cdc3e7d59541575c1c347c87b73080f8ba7a15e2
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:05ba021ce498d3b55597392f135f6cb70373722984ab8e81d9e466bfa64153ce
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:b3871cfcc7907a453d2051af8e5f44e39137ec12a876e592d5a6961cf50d83bd
hotfix: false
---

# 2026-10-15 — gpu-catalogue-wrap on production

The digests of
[`2026-10-15-staging-gpu-catalogue-wrap.md`](2026-10-15-staging-gpu-catalogue-wrap.md),
released after staging's run 38723813378 and its checks passed. The gate
found that run's manifest. This is #115, which the operator asked to have
merged and released ("先把 PR 115 合并发布"), together with its 375 px fix #118.

## What shipped

Site `b3871cfc…` (`db3c771`) → `c2899e5f…` (`0e2b694`): #115 the GPU
catalogue (dashboard Marketplace table, run-form GPU select, public
`/marketplace#gpus` without prices, ten languages) and #118 (the "GPUs"
subtitle wraps and drops under its heading on phones), plus the docs-only
#114 and #117. compute-api unchanged (`05ba021c…`, `494f43d`). No price,
migration or Terraform change.

## Before / after

- Before (status run 38725357478): `05ba021c…` / `b3871cfc…`, PREVIOUS
  m3 (`de7ce94d…` / `b9da4d90…`). Staging was at this pair when it took
  the same step.
- Run 11:12:15–11:14:13Z. Backup
  `/data/backups/20261015T111258Z-38725660008`, copied to
  `s3://unify-compute-production-backups-871376691052/production/releases/20261015T111258Z-38725660008/`
  (compute 29 tables).
- Only `site` was recreated; compute-api and postgres kept their
  containers, 0 restarts after 30 s. Smoke passed, `/healthz` build
  `494f43d`, app CSP enforced, `compute.unifyai.us` resolves to the ALB
  and is served by the site container with the ALB's HSTS.
- PREVIOUS_RELEASE is now banner-rtl-fix (`05ba021c…` / `b3871cfc…`), so
  `mode=rollback` is a site-only step back with no schema concern (both
  read 0001–0014).

## What was exercised

From outside, signed out:

- **The app host is byte-identical to staging's.** All 16 pages (`/`,
  `/login`, `/signup`, `/verify`, `/forgot-password`, `/reset-password`,
  `/accept-invitation`, `/jobs`, `/job`, `/keys`, `/billing`, `/usage`,
  `/members`, `/listings`, `/account`, `/marketplace`) and both bundles
  they reference (`app-DrACL9UR.css`, `main-DGpeQFnE.js`) hash the same
  as on `app.staging.compute.unifyai.us`, where the signed-in checks ran.
  Against production before the release, every page differs only in
  those two bundle names, except `/marketplace`, which gained exactly the
  GPUs section with `class="muted section-note"`. The CSS carries
  `.section-head .section-note{white-space:normal;text-align:end;flex:320px}`
  and the 580 px `text-align:start` override.
- `app.compute.unifyai.us/marketplace` signed out lands on `/login` at 1280
  and 375: "Log in — Unify Compute", 64 px bar, one primary button,
  `scrollWidth` = `innerWidth` (375 at 375), and no console errors apart
  from the expected signed-out 401 on `/dashboard/v1/me`.
- Public `compute.unifyai.us/marketplace#gpus`, 1280 and 375: four rows
  (A10G `a10g-24gb-x1` Available, A100 80GB, A10G spot and L4 Coming
  soon), headers GPU / Memory / vCPU · RAM / Tier / Billing / Status. No
  price anywhere: the visible text has no USD, `$`, per-hour or
  two-decimal figure, and the HTML has no `price_usd` or SKU rate. The
  anchor scrolls to "Choose the GPU for the job", no page-level
  horizontal scroll, and at 375 the table scrolls in its box (582 in 327).
- Marketing site: production's pages hash the same as
  `site.staging.compute.unifyai.us`. Against before, the visible text of
  `/`, `/start`, `/privacy` and the 404 page is identical; only
  `/marketplace` changed, by the GPUs section.

## What this does not prove

- **No signed-in session on production.** The signed-in Marketplace,
  the run form, 简体中文 and the 375 px measurement ran on staging only
  (account 22). Production is covered by the identical bytes above and
  the same compute-api digest serving `GET /dashboard/v1/skus`.
- `/members` scrolls sideways at 375 px on production too. It predates
  this release; see the staging record.
- No job was submitted; production has no SkyPilot.
