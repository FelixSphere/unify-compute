---
environment: production
action: release
outcome: success
run: 37093737717
commit: e5ce49023749034694f50352325b894eadcb075c
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:9dfc652abae49a0b622eda994c4152aa16b3a295b830e76daeae2685b03496b8
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:6981df3526a829868b3360d938b3240217f0e9d19790c303505b0cdcbf587f63
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:ec4bd964372acf9724365273204c556414a3090ee68035037b0c41b21e7d9902
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:7b9bb6596a8cba954dccd8ff8bd4922eaf23d43d54b0ea9fe8a9b7315bace4ea
hotfix: false
---

# 2026-10-03 — gpu-ready: wired compute-api on production, keyed routes 503

The operator reversed the hold ("合并，发布", merge and release). Production
takes the same digests as staging run 37093005905 (gate: `staging ran exactly
these digests`). What shipped is in
[2026-10-03-staging-gpu-ready](2026-10-03-staging-gpu-ready.md). Over
production's previous compute-api (`a8562dd`) that is 26 merges. Among them
are the wired job API (#40), resume (#42), artifacts (#38), the SkyPilot
backend (#22, #39) and the IMDS file credentials (#44). No price change.

## Before the release

- IMDS lockdown: `IMDS_LOCKDOWN_DONE all`, then `CHECK_IMDS_OK`.
- `scripts/staging-secrets.sh --env production` created
  `artifacts_token_secret`, which the script now lists for production. The
  running host does not read it; its `.env` has no artifacts lines.
- `terraform apply` envs/production: **18 added, 1 changed, 0 destroyed**:
  ADR-0005's artifacts bucket, logs bucket (neither force-destroyable) and
  writer role, plus hop limit 2 → 1 on the instance (in place).
  `CHECK_IMDS_OK` after. `gpu_workers_enabled = false`: no GPU resources, 0
  `gpu_workers` entries in state.

## The release

Before: compute-api `ec4bd964…`, site `7b9bb659…`. Backup
`/data/backups/20261003T033639Z-37093737717`, copied to
`s3://unify-compute-production-backups-871376691052/production/releases/20261003T033639Z-37093737717/`.
Smoke passed, and so did `served-by-aws.sh`.

**A data release:** this compute-api migrates at boot. Production's `compute`
database went from empty to migrations 0001_jobs, 0002_ledger, 0003_events
and 0004_resume.

## What was exercised

- `https://api.compute.unifyai.us/healthz`: build e5ce490.
- Public routes 200: `/compute/v1/skus` (identical to staging),
  `/compute/v1/templates`, `/compute/v1/openapi.json`.
- Without a key: 401 `missing_key`. With a key, `GET/POST /compute/v1/jobs`,
  `GET /jobs/{id}`, `/events`, `/artifacts` and `POST /jobs/{id}/cancel` all
  return **503 `console_unavailable`**, and nothing else. compute-api logs
  `CONSOLE_URL unset; keyed job routes answer 503 console_unavailable`, plus
  the artifacts (501) and SkyPilot (503 `upstream_unavailable`) warnings that
  keyed requests cannot reach behind it.
- Site on https://compute.unifyai.us: title, v4 icons byte-identical to
  the brand kit, HubSpot loader once, `/privacy` 200, HSTS once.
- `check-imds.sh`: `CHECK_IMDS_OK`.

## What this does not prove

- Anything a customer can do with a key. Every keyed route is 503 until the
  console bridge ships on app.unifyapi.ai and production's
  `bridge_enabled` / `CONSOLE_URL` change in a reviewed PR with a rebuild.
