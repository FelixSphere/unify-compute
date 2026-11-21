---
environment: production
action: release
outcome: success
run: 40268860104
commit: 2c7669f647307ad36b011744802683141eb27cf9
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fd586e94c7fe65a844ccae5f8ee4f44f39ff15591bd5f5cdfa37df4c67e77ae4
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:26d2e66c320a6ca0b60373ea61dfd0e4fc18f30f48f6bd5ccc744e40dd18268f
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fd586e94c7fe65a844ccae5f8ee4f44f39ff15591bd5f5cdfa37df4c67e77ae4
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:bd9edfdbd0a82676db29adb0216a11796d758f4c8ae404bab92b30bb4bc74f12
hotfix: false
---

# 2026-10-26 — smooth-load, with the skeleton fix, in production

The same digests as staging run 40267687668, exercised there
([`2026-10-26-staging-smooth-load-fix.md`](2026-10-26-staging-smooth-load-fix.md)).
This answers the operator's request for smoother page loads.

## What shipped

Site `bd9edfdb…` (`5838ce5`) → `26d2e66c…` (`2c7669f`). That is #136
(early reads beside `/me`, skeletons, a 220 ms ease-in, sidebar-hover
prefetch) and #138 (a filled loading line drops its skeleton), plus
records. compute-api is unchanged (`fd586e94…`, `e7fe722`). No price,
migration, env var or Terraform change.

## Before / after

- Before (status run 40267593940): `fd586e94…` / `bd9edfdb…`, PREVIOUS
  `fd586e94…` / `c2899e5f…`, IMDS lockdown present.
- Run 04:23:52–04:25:53Z. Staging first: "staging ran exactly these
  digests" (run 40267687668). Provenance passed for both digests. Backup
  `/data/backups/20261026T042437Z-40268860104`, copied to
  `s3://unify-compute-production-backups-871376691052/production/releases/20261026T042437Z-40268860104/`
  (compute 29 tables). Routine prune removed
  `/data/backups/20261003T202628Z-37151500537`.
- Only `site` was recreated. compute-api, postgres and site had 0 restarts
  before, after `up -d` and after 30 s. Smoke passed, including
  `/healthz` build `e7fe722`, the app host's CSP, `compute.unifyai.us`
  resolving to the ALB, and HSTS.
- After: `fd586e94…` / `26d2e66c…`. PREVIOUS `fd586e94…` / `bd9edfdb…`, the
  same schema, so `mode=rollback` is a site-only step.

## What was exercised

From the dev Mac, signed out:

- **Byte comparison with staging (pass).** `app.compute.unifyai.us` serves
  `/`, `/usage`, `/billing`, `/jobs`, `/keys`, `/login` and `/signup`
  byte-identical to `app.staging.compute.unifyai.us`. So are every asset
  those pages reference: `app-p-RzbA1F.css` (all four `common.loading`
  rules carry `:not([data-loaded])`), `main-DItOsaOQ.js` and five fonts.
  So production serves what staging was exercised on.
- **Signed out, in headless Chrome (pass).** `/`, `/usage` and `/billing`
  redirect to `/login`. On `/`, `/usage`, `/login` and `/signup`, nothing
  under `main` computes `animation-name: uc-shimmer`, and
  `document.getAnimations()` has no running `uc-shimmer`.
- **Marketing site (pass).** `compute.unifyai.us` `/` and `/privacy` are
  raw-identical to staging's after its release. They are identical to the
  pre-release snapshot once per-build JS chunk hashes and the build UUID
  are normalised. `index.DGyjs4uY.css` is unchanged.

## What this does not prove

- Nothing signed in was run on production: no production account was
  used. The signed-in behaviour (no skeleton left after load, early reads,
  key create/revoke, cancelled Billing return, hover prefetch) was
  exercised on staging against byte-identical pages and assets, with
  production's compute-api build.
- Only Chrome. Timings are from Nanjing.
