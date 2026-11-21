---
environment: production
action: release
outcome: success
run: 39033815759
commit: 5838ce5dcfd0877da5677463fb80da335d8c5e92
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fd586e94c7fe65a844ccae5f8ee4f44f39ff15591bd5f5cdfa37df4c67e77ae4
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:bd9edfdbd0a82676db29adb0216a11796d758f4c8ae404bab92b30bb4bc74f12
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fd586e94c7fe65a844ccae5f8ee4f44f39ff15591bd5f5cdfa37df4c67e77ae4
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:c2899e5f4815bf0dd4574ac0cdc3e7d59541575c1c347c87b73080f8ba7a15e2
hotfix: false
---

# 2026-10-17 — smooth-shell on production

These are the same digests staging ran in run 39032471498, and the gate
confirmed that ("staging ran exactly these digests"). The delta (#131,
dashboard only) and its exercise are in
`2026-10-17-staging-smooth-shell.md`. compute-api is unchanged at
`fd586e94…` (`e7fe722`).

## Before / after

- Before (status run 39033744713): `fd586e94…` / `c2899e5f…`, PREVIOUS
  `05ba021c…` / `c2899e5f…`, IMDS lockdown present. This matches the
  job-log-archive record and what staging ran before its release.
- Run 12:45:46–12:47:56Z. Backup
  `/data/backups/20261017T124636Z-39033815759`, copied to
  `s3://unify-compute-production-backups-871376691052/production/releases/20261017T124636Z-39033815759/`
  (compute 29 tables).
- Only `site` was recreated. After 30 s compute-api, postgres and site all
  had 0 restarts. Smoke passed: `/healthz` reports build `e7fe722`, the app
  CSP is enforced, `compute.unifyai.us` resolves to the ALB, and HSTS is
  present.
- The routine prune (newest 10 local backups) removed
  `/data/backups/20261003T185838Z-manual-prod-stripe-env/`, the pre-Stripe
  `.env` cited in `2026-10-03-production-accounts-live.md`. Env files are
  never copied to S3, so that copy is gone. Every release backup since holds
  its own `.env`.
- PREVIOUS_RELEASE is now `fd586e94…` / `c2899e5f…`, so `mode=rollback`
  is a site-only step back.

## What was exercised

These checks were signed out, from the dev Mac.

- **Served HTML**: all 16 app pages on `app.compute.unifyai.us` carry one
  entry script with `blocking="render"`; the 404 has none. All 17 have 5
  font preloads, and each font answers 200 `font/woff2`.
- **Bytes equal staging**: the 17 pages (404 included) and all 7 assets
  they reference (`main-52QbrsnS.js`, `app-yywHSKJc.css`, the 5 fonts)
  are byte-identical between `app.compute.unifyai.us` and
  `app.staging.compute.unifyai.us`. The CSS carries `@view-transition`.
- **Headless Chrome**: signed-out /usage goes to /login. After choosing
  简体中文, the first animation frame of /login was
  `登录 — Unify Compute` (`lang=zh-Hans`), and /signup was
  `注册 — Unify Compute`. At 375 px, /login and /signup have no
  horizontal scroll.
- **Marketing site** `compute.unifyai.us`: /, /privacy, /icon-v4.svg and a
  404 are byte-identical to `site.staging…`. The visible text of / is
  unchanged from before the release (hash `60f9cab0…`), and the title is
  unchanged. Only bundle names changed, plus the unused
  `.collapse{visibility:collapse}` rule described in the staging record.

## What this does not prove

- Nothing signed in was exercised on production: sidebar collapse, key
  create/revoke, logout and transitions were checked only on staging. The
  bytes being identical is the evidence that production behaves the same.
- No customer session was observed across the cutover. A tab open from
  before the release keeps its old JS until it navigates.
