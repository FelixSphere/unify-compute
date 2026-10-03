---
environment: production
action: release
outcome: success
run: 37099963200
commit: 5f964c1b4469b6846766da00381edf9433d754e2
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fdd7a67c1c72a94ae483df8eb4e1eab4ba3785288fb891c54506ff5ecac3fb34
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:6981df3526a829868b3360d938b3240217f0e9d19790c303505b0cdcbf587f63
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:9dfc652abae49a0b622eda994c4152aa16b3a295b830e76daeae2685b03496b8
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:6981df3526a829868b3360d938b3240217f0e9d19790c303505b0cdcbf587f63
hotfix: false
---

# 2026-10-03 — start-small: hard GPU spend caps (#50)

Same digests as staging run 37099812868 (gate: `staging ran exactly these
digests`). What shipped is in
[2026-10-03-staging-start-small](2026-10-03-staging-start-small.md).
`envs/production` plan beforehand: no changes.

## Before

compute-api `9dfc652a…`, site `6981df35…`. Production's console bridge was
already on: #51, done by unifyai-cicd with operator approval, release run
37095677205. So keyed routes already answered 401 for an unknown key.

## The release

Backup `/data/backups/20261003T052946Z-37099963200`, copied to
`s3://unify-compute-production-backups-871376691052/production/releases/20261003T052946Z-37099963200/`.
Smoke and `served-by-aws.sh` passed.

## What was exercised

- `https://api.compute.unifyai.us/healthz`: build 5f964c1. The boot log shows
  `platform_max_active_jobs 1`, `platform_max_gpus 1`,
  `max_runtime_seconds 14400`, and `console bridge configured` for
  `https://app.unifyapi.ai`.
- A made-up key on `GET` and `POST /compute/v1/jobs`: **401 `invalid_key`**.
  No key: 401 `missing_key`. `/skus`, `/templates` and `/openapi.json`: 200.
- Site on https://compute.unifyai.us: title, v4 icons byte-identical to the
  brand kit, HubSpot once, `/privacy` 200, HSTS once.
- `check-imds.sh`: `CHECK_IMDS_OK`.
- GPUs off: no skypilot container, 0 `gpu_workers` resources in production state, no
  `Project=unify-compute-workers` instance running. The host logs
  `SKYPILOT_API_SERVER_URL unset`, so a keyed submit with a real key would get
  503 `upstream_unavailable`.

## What this does not prove

- The real-key path on production. That needs an internal test user on
  app.unifyapi.ai, which only the operator can create (see the #51 record).
