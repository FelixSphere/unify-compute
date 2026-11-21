---
environment: production
action: release
outcome: success
run: 39023191841
commit: e7fe722850ab36e9efdb12ccff3c7eebccf89035
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fd586e94c7fe65a844ccae5f8ee4f44f39ff15591bd5f5cdfa37df4c67e77ae4
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:c2899e5f4815bf0dd4574ac0cdc3e7d59541575c1c347c87b73080f8ba7a15e2
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:05ba021ce498d3b55597392f135f6cb70373722984ab8e81d9e466bfa64153ce
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:c2899e5f4815bf0dd4574ac0cdc3e7d59541575c1c347c87b73080f8ba7a15e2
hotfix: false
---

# 2026-10-17 — job-log-archive on production

The digests of
[`2026-10-17-staging-job-log-archive.md`](2026-10-17-staging-job-log-archive.md),
released after staging's run 39022313260 and its checks passed. The gate
found that run's manifest (`release-manifest-staging-fd586e94c7fe-c2899e5f4815`).

## What shipped

compute-api `05ba021c…` (`494f43d`) → `fd586e94…` (`e7fe722`): #121, keep
a job's log after its cluster is torn down (see the staging record). Site
unchanged (`c2899e5f…`). No price, migration, env var or Terraform change.

## Before / after

- Before (status run 39023114874): `05ba021c…` / `c2899e5f…`, PREVIOUS
  `05ba021c…` / `b3871cfc…`, IMDS lockdown present. Matches the
  gpu-catalogue-wrap record.
- Run 09:59:39–10:01:21Z. Backup
  `/data/backups/20261017T100031Z-39023191841`, copied to
  `s3://unify-compute-production-backups-871376691052/production/releases/20261017T100031Z-39023191841/`
  (compute 29 tables).
- Only `compute-api` was recreated. Smoke passed, `/healthz` build
  `e7fe722`. Started 10:00:34Z with "stripe top-ups enabled livemode=true"
  and the existing WARN that `ARTIFACTS_BUCKET` / `ARTIFACTS_TOKEN_SECRET`
  are unset. 0 restarts and 0 ERROR lines afterwards.
- PREVIOUS_RELEASE is now `05ba021c…` / `c2899e5f…`.

## What was exercised

- **Production has no jobs** (`select count(*) from jobs` → 0, read-only
  over SSM), so there is no finished job whose log could be read.
- Production's `.env` has no `ARTIFACTS_*` lines, so compute-api runs
  without a log archive. A terminal job's `/logs` would never reach S3
  and answers `200` empty, as before. The run step is not added to tasks
  either, and production has no SkyPilot.
- Signed out: `api.compute.unifyai.us/compute/v1/jobs/<id>/logs` → `401
  missing_key`; `app.compute.unifyai.us/dashboard/v1/jobs/<id>/logs` → `401
  session_required`.

## What this does not prove

- Nothing on production exercises #121, and nothing can until production
  has artifacts storage, SkyPilot and a job. It is the same digest staging
  runs.
- No signed-in session or key on production.
