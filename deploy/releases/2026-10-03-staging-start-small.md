---
environment: staging
action: release
outcome: success
run: 37099812868
commit: 5f964c1b4469b6846766da00381edf9433d754e2
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fdd7a67c1c72a94ae483df8eb4e1eab4ba3785288fb891c54506ff5ecac3fb34
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:6981df3526a829868b3360d938b3240217f0e9d19790c303505b0cdcbf587f63
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:9dfc652abae49a0b622eda994c4152aa16b3a295b830e76daeae2685b03496b8
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:6981df3526a829868b3360d938b3240217f0e9d19790c303505b0cdcbf587f63
hotfix: false
---

# 2026-10-03 — start-small: hard GPU spend caps (#50)

Operator-approved ("合并并 apply"). No customers yet, so no surprise bills.

## What shipped

- compute-api `fdd7a67c…`, built from main `5f964c1` (publish run 37098651034).
  Over `e5ce490`: #50, which adds `config/platform.json` (platform-wide 1
  active job, 1 GPU, `max_runtime_seconds` 14400) and platform admission
  under one advisory lock. Also #51, docs and scripts only for this image.
  `skus.json` and `quotas.json` are unchanged. The image refuses to boot
  without `platform.json`.
- site unchanged (`6981df35…`).

## Applied before the release

- `envs/staging`: **0 added, 2 changed, 0 destroyed**. The launch policy
  gains `DenySpot` and `DenyOtherWaysToLaunch`. The reaper's `MAX_AGE_HOURS`
  goes 26 → 5, and its package hash changed because `reaper.py` gained a
  docstring. A reaper invoke afterwards returned `max_age_hours: 5.0` and no
  workers.
- `shared/gpu-budget`, with `alert_email=yuchengwang@felixsphere.com`
  (operator's choice) and the default `monthly_limit_usd` 50: **5 added**.
  - Budget `unify-compute-gpu-workers`: USD 50, filtered on
    `user:Project$unify-compute-workers`. Notifications at 50, 80 and 100%
    actual, and 100% forecast. The `Project` tag reads Active.
  - Action `b4302f8f-c1df-40b6-8157-d71a203ae18a`: `APPLY_IAM_POLICY`,
    approval **AUTOMATIC** (no approval step), threshold 100% ACTUAL, status
    STANDBY. It attaches `unify-compute-gpu-budget-stop-launches` (Deny
    `ec2:RunInstances`, `ec2:StartInstances`) to
    `unify-compute-staging-skypilot`, run by
    `unify-compute-gpu-budget-action`. That role is trusted only by
    budgets.amazonaws.com from this account for this budget, and may only
    attach and detach that one policy on that one role.
  - AWS sends a subscription confirmation to the alert address. The
    operator has to confirm it, or the alerts go nowhere. The action
    itself does not depend on that email.
- Budget stop dry run (docs/staging.md), from the skypilot container, as the
  launch role:
  1. Tagged `run-instances --dry-run` for a g5.xlarge in a workers subnet with the
     workers SG and worker profile: `DryRunOperation`.
  2. Attached the stop policy by hand: `UnauthorizedOperation`.
  3. Detached. The role has no attached policies again.
- `envs/production` plan: no changes.

## The release

Backup `/data/backups/20261003T052650Z-37099812868`, copied to
`s3://unify-compute-staging-backups-871376691052/staging/releases/20261003T052650Z-37099812868/`.
Only compute-api was recreated. All six containers were stable with 0
restarts. Smoke passed.

## What was exercised (from outside)

- `/healthz`: build 5f964c1.
- `max_runtime_seconds: 14401`: **400 `runtime_over_ceiling`**, "exceeds
  the platform ceiling of 14400".
- Owner 1 (`compute-e2e`) valid `batch-infer-vllm` submit: 201,
  `provisioning`, hold $1.20 (`job_01M403QSG49J8E6B6ZMPGQW53R`).
- Owner 2 (`compute-e2e-2`, created as below), a second later: **403
  `quota_exceeded`**, `platform_max_active_jobs` 1/1 and `platform_max_gpus`
  1/1, headroom 0, "Nothing was submitted or held".
- Owner 1's job: `failed`, `capacity_exhausted`; SkyPilot request log
  `VcpuLimitExceeded` ×5 (quota still 0). The console hold is `released`
  (600000 back), compute's hold is `closed`. 0 `compute_settlements`, 0
  `usage_events`, gpu_seconds 0. Console API balances: both users 50,000,000.

## Second staging test owner

Same procedure as the first (gpu-ready record), through the staging
console's HTTP API: user `compute-e2e-2` (id 3), `add_quota` 50,000,000,
token as that user. In staging SSM, SecureString:
`/unify-compute-staging/test_user2_password`, `/unify-compute-staging/test_api_key_2`.

## What this does not prove

- That the budget action fires on real spend. The dry run proves the deny it
  attaches; the trigger depends on AWS Budgets' data lag (ADR-0010 bound).
- That alerts reach anyone, until the operator confirms the subscription.
