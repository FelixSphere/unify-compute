---
environment: staging
action: rollback
outcome: success
run: 40265908953
commit: 1d5c16ecfbbeab45c81c307e8aef396659d6bea2
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fd586e94c7fe65a844ccae5f8ee4f44f39ff15591bd5f5cdfa37df4c67e77ae4
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:bd9edfdbd0a82676db29adb0216a11796d758f4c8ae404bab92b30bb4bc74f12
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fd586e94c7fe65a844ccae5f8ee4f44f39ff15591bd5f5cdfa37df4c67e77ae4
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:c4d87233b4adbda42b17d55214f4b2d3372caf6ca4f9f0512935af4223775eb4
hotfix: false
---

# 2026-10-26 — staging rolled back from smooth-load, to production's pair

smooth-load (site `c4d87233…`, `1d5c16e`) failed its staging exercise. Its
loading sub-lines stay invisible under a shimmer after load; see
[`2026-10-26-staging-smooth-load.md`](2026-10-26-staging-smooth-load.md).
Production was never released. Staging went back so that it is not left
on a known-broken build, and so that the fix's release tests the step
production will take (`bd9edfdb → fix`).

## Before / after

- Before: `fd586e94…` / `c4d87233…`, PREVIOUS `fd586e94…` / `bd9edfdb…`.
- Run 03:48:57–03:50:43Z. Backup `/data/backups/20261026T034924Z-40265908953`,
  copied to
  `s3://unify-compute-staging-backups-871376691052/staging/releases/20261026T034924Z-40265908953/`
  (compute 29 tables, console 59). Routine prune removed
  `/data/backups/20261014T110107Z-38553488166`.
- Only `site` was recreated. All six containers had 0 restarts after 30 s.
  Smoke passed.
- After: `fd586e94…` / `bd9edfdb…`, the pair production runs (status run
  40265053048). PREVIOUS is now `fd586e94…` / `c4d87233…`, so a second
  `mode=rollback` would re-apply the broken build. Do not use it.
  No database change: site only.

## What was exercised

From outside: `/`, `/usage`, `/billing` and `/keys` on
`app.staging.compute.unifyai.us` are byte-identical to
`app.compute.unifyai.us`. Staging's `/` is identical to what it served
before the release. The stylesheet is back to `app-yywHSKJc.css`, with no
`uc-shimmer`.

## What this does not prove

Nothing signed in was run against the rolled-back build. It is the build
both hosts ran from 2026-10-17 until today.
