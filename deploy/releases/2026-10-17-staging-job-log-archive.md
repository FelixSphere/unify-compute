---
environment: staging
action: release
outcome: success
run: 39022313260
commit: e7fe722850ab36e9efdb12ccff3c7eebccf89035
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fd586e94c7fe65a844ccae5f8ee4f44f39ff15591bd5f5cdfa37df4c67e77ae4
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:c2899e5f4815bf0dd4574ac0cdc3e7d59541575c1c347c87b73080f8ba7a15e2
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:05ba021ce498d3b55597392f135f6cb70373722984ab8e81d9e466bfa64153ce
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:c2899e5f4815bf0dd4574ac0cdc3e7d59541575c1c347c87b73080f8ba7a15e2
hotfix: false
---

# 2026-10-17 — job-log-archive on staging

The operator said "release it" for what was merged and unshipped on main
(`e7fe722`). Against the last records (gpu-catalogue-wrap, both hosts),
that is #121 alone.

## What shipped

compute-api `05ba021c…` (`494f43d`) → `fd586e94…` (`e7fe722`), published
by publish-image run 39022026530 (tag `job-log-archive-2026-10-17`,
`sha-e7fe722…`, amd64 + arm64). `git log 494f43d..e7fe722 -- compute-api`
is one commit:

- `296e4b6` #121: when an artifacts uploader is configured, **every** job
  now runs through the run step (not only jobs with `spec.outputs`). The
  step tees stdout and stderr to `/unify-compute-logs/job.log` and uploads
  it from its EXIT trap. `GET /jobs/{id}/logs` is empty while queued or
  provisioning; for a terminal job it reads
  `jobs/<id>/unify-compute-logs/job.log` from the artifacts bucket (its last
  4 MiB); while running it reads the cluster, falling back to the archive.
  A customer output mount may no longer be or contain `/unify-compute-logs`.

Site unchanged (`c2899e5f…`, `0e2b694`): nothing under `app/`,
`components/`, `public/` or `deploy/site/` changed since. No price,
migration, env var or Terraform change.

## Before / after

- Before (status run 39022054847): `05ba021c…` / `c2899e5f…`, PREVIOUS
  `05ba021c…` / `b3871cfc…`, IMDS lockdown present. Matches the
  gpu-catalogue-wrap record.
- Run 09:45:09–09:46:50Z. Backup
  `/data/backups/20261017T094558Z-39022313260`, copied to
  `s3://unify-compute-staging-backups-871376691052/staging/releases/20261017T094558Z-39022313260/`
  (compute 29 tables, console 59).
- Only `compute-api` was recreated. Smoke passed, `/healthz` build
  `e7fe722`. Started 09:46:03Z with "artifact storage configured"; 0
  restarts and 0 ERROR lines over the following ~15 minutes, through the
  checks below.
- PREVIOUS_RELEASE is now `05ba021c…` / `c2899e5f…`. `mode=rollback` is a
  compute-api-only step back with no schema concern (no migration).

## What was exercised

**What a job from before this release returns.** Staging has four jobs, all
`failed` and none with a stored log: `job_01M51Q9X…` (account 23) and
`job_01M51RS5…` (account 24) from the 2026-10-16 GPU runs, and two
console-era `tenant:1` jobs from 2026-10-03 that no `uck_` key can reach.
For a terminal job the new code GETs `jobs/<id>/unify-compute-logs/job.log`,
and `200` with an empty body depends on S3 answering a missing key with
404. The instance role has `s3:ListBucket` only under a `s3:prefix =
jobs/*` condition, so 403 was possible. That would have made every old
job's `/logs` a permanent `503 logs_unavailable`. Checked before the
release, over SSM on the host with the instance role
(`assumed-role/unify-compute-staging/i-06125c23…`):

- `get-object jobs/job_01M51Q9XFVNXGW6KSMCTFZK16J/unify-compute-logs/job.log`
  → `NoSuchKey`, which `awsv4.IsNotFound` maps to "no log". So an
  existing finished job on staging returns **`200`, empty body**, as it
  did before the release.
- `head-object` of the same job's `_manifest.json` → 118 bytes (the role
  reads the bucket).

**From outside, on the new build:**

- Signed out: `GET /compute/v1/jobs/job_01M51RS5…/logs` → `401
  missing_key`; `app.staging…/dashboard/v1/jobs/job_01M51RS5…/logs` → `401
  session_required`; the dashboard `/job?id=…` page → 200.
- A new throwaway, account 25 (`success+cicd-logs-1017@simulator.amazonses.com`),
  was signed up and verified (its code recovered offline from its
  `email_codes` hash, read-only over SSM). Session:
  `/dashboard/v1/jobs/<id>/logs` for account 24's job, for a `tenant:1` job,
  and for a made-up id → each `404 job_not_found`. Key id 4 (`uck_EqVj…`,
  `jobs`): `/compute/v1/jobs/<id>/logs` for account 24's job and a made-up
  id → `404 job_not_found`; `GET /compute/v1/jobs` → `200`, empty list.
  Key 4 was then revoked (204; the key then got 401) and the session
  logged out (204). The password, cookie and secret were deleted from
  scratch.

## What this does not prove

- **No owner-level read of a finished job.** Accounts 23 and 24 own the
  only reachable jobs, and their passwords were deleted after 2026-10-16.
  A reset token cannot be recovered from the database, and I did not mint
  a session or a password by writing to it. The `200`-empty answer for
  those jobs is inferred from the S3 probe above and the code
  (`archivedLog`: nil log, no fallback → empty 200). It was not observed
  through the API.
- **The write side of #121 is untested on a host.** No job ran: GPU spend
  needs the operator's go, and the batch templates' vLLM image is broken
  (#122, draft). The next staging job is the first to run through the new
  run step, which every job now takes. Watch whether bash process
  substitution (`exec > >(tee …)`) works in SkyPilot's run shell, whether
  the exit status survives, and whether `job.log` lands under the job's
  prefix.
- #121 added no unit test for the archive read (`LogArchive` has no fake
  in `internal/api` or `internal/jobs`). The terminal, running-fallback and
  missing-log paths are covered only by the probe above.
- Account 25 is left on staging: active, compute off, balance 0, key 4
  revoked, no session.
