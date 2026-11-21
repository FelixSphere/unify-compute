---
environment: staging
action: rollback
outcome: success
run: 38723497442
commit: 0e2b69439e9f8a6d8173dd94b16a1ae3ed6ccc33
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:05ba021ce498d3b55597392f135f6cb70373722984ab8e81d9e466bfa64153ce
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:b3871cfcc7907a453d2051af8e5f44e39137ec12a876e592d5a6961cf50d83bd
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:05ba021ce498d3b55597392f135f6cb70373722984ab8e81d9e466bfa64153ce
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:b14bbf75adee10697de1b1962bae957a4c9ad80b9e87a44f6f07dc3a1b6030dc
hotfix: false
---

# 2026-10-15 — staging rolled back to production's pair, for parity

Not a fault. Staging ran site `b14bbf75…` (`846c2e0`, the gpu-catalogue
build held in
[`2026-10-15-staging-gpu-catalogue.md`](2026-10-15-staging-gpu-catalogue.md)),
one site build ahead of production (`b3871cfc…`, `db3c771`). A staging
release from there would have tested `b14bbf75 → new`, a step production
never takes. So staging went back first, and the release in
[`2026-10-15-staging-gpu-catalogue-wrap.md`](2026-10-15-staging-gpu-catalogue-wrap.md)
then took production's exact step, `b3871cfc → c2899e5f`.

## Before / after

- Before (status run 38723232062, 10:51Z): `05ba021c…` / `b14bbf75…`,
  PREVIOUS `05ba021c…` / `b3871cfc…`.
- Run 10:53:51–10:55:30Z. Backup `/data/backups/20261015T105415Z-38723497442`,
  copied to
  `s3://unify-compute-staging-backups-871376691052/staging/releases/20261015T105415Z-38723497442/`
  (compute 29 tables, console 59 tables).
- Only `site` was recreated. compute-api, postgres, redis, console and
  skypilot kept their containers, 0 restarts after 30 s.
- After: `05ba021c…` / `b3871cfc…`, which is what production ran
  (production status run 38725357478). PREVIOUS became `b14bbf75…`.
  No database change: site only, and compute-api unchanged.

## What was exercised

The run's smoke: compute-api `/healthz`, `/compute/v1/skus` priced,
`/compute/v1/jobs` 401 `missing_key`; site `/`, `/privacy`,
`/icon-v4.svg` with `max-age=3600`; app host with enforced CSP and
`/dashboard/v1/session` answered by compute-api. All passed.

## What this does not prove

Nothing beyond smoke was run against the rolled-back build. It was on
staging for about a minute before the next release replaced it.
