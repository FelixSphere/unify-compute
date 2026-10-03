---
environment: staging
action: release
outcome: success
run: 37093005905
commit: e5ce49023749034694f50352325b894eadcb075c
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:9dfc652abae49a0b622eda994c4152aa16b3a295b830e76daeae2685b03496b8
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:6981df3526a829868b3360d938b3240217f0e9d19790c303505b0cdcbf587f63
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:cfbbbdd90db29c223f1ad5483be798b41bd9950dd645064cbc5bfc8656fc6b0b
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:7b9bb6596a8cba954dccd8ff8bd4922eaf23d43d54b0ea9fe8a9b7315bace4ea
hotfix: false
---

# 2026-10-03 — gpu-ready: staging can run a real job once the GPU quota lands

Operator-approved ("好" to the plan). Goal: `batch-infer-vllm` on
`a10g-24gb-x1` (g5.xlarge) runs on staging as soon as AWS grants quota
L-DB2E81BA (request `d81c8fcd…`, CASE_OPENED, quota 0 today).

## What shipped

- compute-api `9dfc652a…`, built from main `e5ce490` (publish run 37092446467).
  Over staging's previous build `667b381` (wave2-jobs, run 37091108250): #43
  test edits, #44 (compute-api reads the instance role from a file; no
  container reaches IMDS), #45 SkyPilot overlay reset, and #46/#47 (records,
  console pin). No price change: `skus.json` is unchanged since a8562dd, and
  `/compute/v1/skus` is identical on staging and production.
- site `6981df35…` (publish run 37092453326). Same content as `7b9bb659…`;
  nothing under `app/`, `components/`, `public/`, `lib/` or `deploy/site/`
  changed since ea56779. Rebuilt, so it carries a new build id.
- console `e3cef17…@sha256:c48c638b…` (UnifyAPI CI/CD trial build with
  console#225 and #233; pinned by #47). It rolled in this release:
  `CONSOLE_IMAGE` was set in the host's `.env` beforehand, and host.sh's
  `compose up -d` recreated the console after the backup.

## Host and infrastructure changes before the release (in order)

1. IMDS lockdown (`scripts/imds-lockdown.sh` over SSM): `IMDS_LOCKDOWN_DONE
   all`, then `check-imds.sh`: `CHECK_IMDS_OK`.
2. `scripts/staging-secrets.sh --env staging` created
   `artifacts_token_secret` and `skypilot_admin_password`.
3. `terraform apply` envs/staging: **47 added, 2 changed, 0 destroyed**.
   aws_instance.this was updated in place (t4g.small → t4g.medium, a
   stop/start; hop limit 2 → 1). The instance-role policy gained
   sts:AssumeRole on the launch role. Adds: workers VPC 10.73/16 and
   its pieces, launch/worker roles, reaper, and ADR-0005's artifacts buckets and
   writer role. `CHECK_IMDS_OK` again after the stop/start.
4. SkyPilot host setup (Terraform output `skypilot_host_setup` over SSM). The first
   attempt failed: the host could not assume the launch role minutes after
   the policy change, most likely IAM propagation. A manual
   `sts assume-role` succeeded moments later, and the rerun ended
   `SKYPILOT_SETUP_DONE all`.
5. ADR-0005's `.env` lines, over SSM under the ops lock, per ADR-0005
   "Apply" 3: main's `docker-compose.yml` (adds the artifacts env), and
   `AWS_REGION`, `ARTIFACTS_BUCKET`, `ARTIFACTS_WRITER_ROLE_ARN`,
   `ARTIFACTS_TOKEN_SECRET` (from SSM, never printed), `PUBLIC_URL` and
   `CONSOLE_IMAGE`. The previous files are in
   `/data/backups/20261003T031225Z-manual-adr0005-env/` on the host. No
   container was recreated by this step. `COMPUTE_BRIDGE_SECRET` was already
   the same SSM value on compute-api and the console (written at first boot).

## The release

Before: compute-api `cfbbbdd9…`, site `7b9bb659…`. Backup
`/data/backups/20261003T032338Z-37093005905`, copied to
`s3://unify-compute-staging-backups-871376691052/staging/releases/20261003T032338Z-37093005905/`.
compute-api, the site and the console were recreated. After 30 s all six containers
(compute-api, console, postgres, redis, site, skypilot) were running with 0
restarts. Smoke passed.

## What was exercised

- compute-api boot log: `aws credentials from file`, `artifact storage
  configured`, `skypilot api server configured`, `worker lock acquired;
  driving jobs`, build e5ce490. Migrations 0001–0004 present (run by
  wave2-jobs).
- Console: tables `compute_extensions`, `compute_holds`,
  `compute_settlements`. A bridge route (`POST /api/compute/v1/verify`,
  unsigned) answers 401, not 404, so the bridge is on.
- A made-up key gives 401 `invalid_key` (decided by the console over the bridge).
- Test key: see "Staging test account" below. `GET /compute/v1/jobs` gives 200,
  an empty list.
- `POST /compute/v1/jobs` with `template: batch-infer-vllm`, one presigned
  https input: **201**, `provisioning`, hold $1.20
  (`job_01M3ZWXEJGKNZKPPWJEAPT8DNB`, 03:28:53Z). Auth, template, quota and
  hold all passed.
- compute-api restarted mid-provisioning (03:29, under the ops lock). The new
  process logged `worker lock acquired`, then `resumed job from an earlier
  process … status provisioning, attempts 0`. Exactly one SkyPilot launch
  request ran RunInstances; the other two requests for the cluster ran
  none.
- Outcome: `failed` at 03:29:27Z, error `capacity_exhausted` ("no candidate
  provider had capacity"), `retryable: true`. Events: queued → provisioning →
  failed. AWS's own error, in SkyPilot's request log:
  `VcpuLimitExceeded … current vCPU limit of 0` on RunInstances in all five
  zones.
- Hold and billing: the console's `compute_holds` row is `released` (600000
  refunded), compute's hold is `closed`, there are 0 `compute_settlements` and 0
  `usage_events`, and gpu_seconds is 0. The console API shows the test user's quota
  at 50,000,000 before and after.
- `check-imds.sh` after everything: `CHECK_IMDS_OK`, including the skypilot
  container.
- Reaper (`aws lambda invoke unify-compute-staging-gpu-reaper`): `workers:
  []`, `terminated: []`, no error.
- `sky check aws`: AWS [compute] enabled. Storage is disabled because the
  launch role cannot `s3:ListAllMyBuckets` (ADR-0010's minimal role).

## Found, not fixed

- **SkyPilot tried `CreateSecurityGroup`** in the workers VPC and was refused
  (decoded: no statement allows it, no explicit deny). It logged a warning
  and went on to RunInstances. Whether a real launch attaches
  `unify-compute-staging-workers` (`sg-0afdbfd4c04ef1304`) is still unproven.
  docs/staging.md already makes that a first-launch check. Nothing was
  widened.
- **The console's DB and API disagree on the test user's balance.** The API,
  the hold and its release use 50,000,000, but `users.quota` in the console DB
  still reads 0 minutes later. This is on UnifyAPI's trial build, so it is
  theirs to explain; reported, not touched.

## Staging test account (staging only)

Created through the console's own HTTP API, never by SQL:
`POST /api/setup` (root; the console had never been initialised), then
`POST /api/user/login` (bearer access token). Then `POST /api/user/` (user
`compute-e2e`, id 2), `POST /api/user/manage`
`{"action":"add_quota","mode":"add","value":50000000}` ($100). Then, logged
in as that user, `POST /api/token/` and `POST /api/token/1/key`. Kept in
SSM, SecureString: `/unify-compute-staging/console_root_password`,
`/unify-compute-staging/test_user_password`,
`/unify-compute-staging/test_api_key`. The test input is
`s3://unify-compute-staging-artifacts-871376691052/e2e-inputs/2026-10-03/requests.jsonl`.

## What this does not prove

- That a job runs, uploads artifacts or settles. Nothing launched.
- Which security group and subnet a worker gets, and that `/down` terminates it.
  That needs the quota.
- That the GPU budget alerts anyone. `deploy/aws/shared/gpu-budget` is not
  applied, because it requires an alert email and none was given.
