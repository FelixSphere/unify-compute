---
environment: staging
action: release                 # EXERCISE, not a release. check-records.sh accepts only release|rollback; this names the release whose images the job ran on
outcome: success                # that release's outcome. The JOB failed (nonzero_exit), see below
run: 38723813378
commit: 0e2b69439e9f8a6d8173dd94b16a1ae3ed6ccc33
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:05ba021ce498d3b55597392f135f6cb70373722984ab8e81d9e466bfa64153ce
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:c2899e5f4815bf0dd4574ac0cdc3e7d59541575c1c347c87b73080f8ba7a15e2
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:05ba021ce498d3b55597392f135f6cb70373722984ab8e81d9e466bfa64153ce
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:c2899e5f4815bf0dd4574ac0cdc3e7d59541575c1c347c87b73080f8ba7a15e2
hotfix: false
---

# 2026-10-15 — EXERCISE: the first real GPU job on staging (it failed)

**This is an exercise, not a release.** Nothing was deployed, no code or
configuration changed, and production was not touched. The field block
names release run 38723813378 (gpu-catalogue-wrap) because those are the
images the job ran against: compute-api `05ba021c…` (`494f43d`), site
`c2899e5f…` (`0e2b694`).

Operator-authorised ("跑") after being told this was the first real GPU
spend: one `a10g-24gb-x1` (g5.xlarge, USD 1.20/h) for minutes, at most
USD 2, cancel after 20 minutes running, no retry unless transient. The file
is named after the operator's local date (2026-10-15, PDT). Every time
below is UTC, which was already **2026-10-16**.

**Result:** the platform did its part, from submit through teardown. The
job's own command exited non-zero 29 s after it started, and **the reason
is lost**. compute-api started tearing the cluster down 17 s after the job ended, and
nothing keeps a job's logs after teardown (`backend.md` open question 4).
The failure was not transient, so it was not retried.

## The job

- Account 23, `success+cicd-firstjob-1015@simulator.amazonses.com`, a new
  throwaway. The brief named account 22, but account 22 is
  `success+cicd-wrap-1015@…`, and `cicd-gpu-1015@…` is account 8. Neither
  had a stored password or session. Account 23 was signed up through
  `POST /dashboard/v1/signup` and verified through `/verify`. Its code came
  from its `email_codes` row (read-only over SSM) and was recovered offline
  from `sha256(id:code)`.
- `admin account enable-compute 23 --reason "first real job,
  operator-authorised 2026-10-15"` gave audit `ae_01M51Q39G8ZVB2NX1S4D8G291K`
  (06:41:23Z). `admin balance credit 23 --usd 5.00 --reason "first real
  job"` gave audit `ae_01M51Q39X06ZAP7ZWWN35KPJKM`, entry
  `be_01M51Q39X30995C727KY629P8T`.
- Key id 2 (`uck_HXHTrIWU…`, scope `jobs`) was created through
  `POST /dashboard/v1/keys`. After the run it was revoked (204) and the
  session logged out. The secret, password, cookie and presigned URL were
  deleted from scratch.
- Submit: template `batch-infer-vllm`, `sku a10g-24gb-x1`, overrode
  `spec.max_runtime_seconds: 1200`, so the platform itself enforced the
  20-minute limit, and `Idempotency-Key: first-real-job-2026-10-16-a1`. The
  input was the existing two-line test file
  `s3://unify-compute-staging-artifacts-871376691052/e2e-inputs/2026-10-03/requests.jsonl`
  (362 B, `/v1/chat/completions`, `max_tokens` 8), presigned for 3 h, and
  it read 200 from outside before the submit. The template was chosen over
  a plain `nvidia-smi` spec because a matching input already existed, and
  the template exercises the input fetch, the artifact upload and a real
  workload.
- Job `job_01M51Q9XFVNXGW6KSMCTFZK16J`. Hold `hold_01M51Q9XG6Z3M2Y8M4APBXV4GK`
  was **USD 0.4000** (1.20 × 1200 s), and `estimated_cost_usd` read 0.4000.

## Timeline (UTC, 2026-10-16)

| Time | What | Source |
|---|---|---|
| 06:44:59 | `POST /compute/v1/jobs`; 201 `provisioning` at 06:45:01; events `job.queued`, `job.provisioning` | API |
| 06:45:01 | `sky.launch` request `7e70ea68` | SkyPilot requests.db |
| 06:45:07 | `CreateSecurityGroup` refused (`UnauthorizedOperation`); SkyPilot warned and went on, as on 2026-10-03 | provision.log |
| 06:45:09 | `i-040cd2ca7719a6213` launched, first try, us-east-1a | EC2 |
| 06:45:38 | SSH up; `initialize_docker` starts; `docker pull` of the vLLM image at 06:45:39 | provision.log |
| 06:51:00 | image pulled and container up (5 m 20 s; the image is 44.7 GB unpacked) | provision.log |
| 06:51:01–06:51:44 | runtime setup (28 s), Ray, skylet; "Cluster launched" | provision.log |
| 06:51:46 | compute-api sees `UP`: `job.placed` (aws / us-east-1 / g5.xlarge), `job.running`, `started_at`; GPU metering starts | API, metering_attempts |
| 06:51:53.3 | SkyPilot job 1 starts | sky.queue |
| 06:52:22.1 | the command has exited; the upload trap writes the manifest (`_manifest.json` created_at 06:52:22.07) | S3 |
| 06:52:22.2 | SkyPilot job 1 `FAILED`, `end_at`; metering ends here | sky.queue, metering_attempts |
| 06:52:39 | compute-api reads `FAILED`: `job.failed` `nonzero_exit`, final usage event, settle and release, `sky.down` requested | API, balance_entries |
| 06:52:53 | `sky.down` done; EC2 terminate (User initiated 06:52:53) | requests.db, EC2 |

Provisioning took **6 m 45 s** from submit to `running`. 5 m 20 s of that
was the image pull. The command ran 28.8 s in SkyPilot's terms. The instance
existed for about **7 m 44 s**.

## Cost

| | |
|---|---|
| Hold at submit | USD 0.4000 |
| `gpu_seconds` | 35 (06:51:46.388 → 06:52:22.158, floored) |
| Settled (charged to the account) | **USD 0.0117** (5833 quota units) |
| Released | USD 0.3883 |
| Account balance after | available USD 4.9883, held 0.0000 |
| AWS cost (estimate, not measured) | about USD 0.13: g5.xlarge on-demand USD 1.006/h × 464 s, plus about USD 0.004 of 256 GB gp3 and a public IPv4 for 8 min |
| Cost Explorer, Oct 1–17, tag `Project=unify-compute-workers` | USD 0, because Cost Explorer lags by up to 24 h. Read it again tomorrow |

Balance history for account 23 (`GET /dashboard/v1/transactions`; the
same rows are in `balance_entries`):

| Entry | Kind | Amount | Available after | Held after |
|---|---|---|---|---|
| `be_01M51Q39X30995C727KY629P8T` | admin_credit | +5.0000 | 5.0000 | 0.0000 |
| `be_01M51Q9XGX4M9P8EWMD79EQK0G` | hold | -0.4000 | 4.6000 | 0.4000 |
| `be_01M51QQX6S5VJRGCPFWB22JZFH` | settle | -0.0117 | 4.6000 | 0.3883 |
| `be_01M51QQX6WSPSF9RSHQ3QEEP9D` | release | +0.3883 | 4.9883 | 0.0000 |

Two usage events: `ue_01M51QQX6GEHF9AJNHDTRMX15M` (35 s, 5833) and the
final `ue_01M51QQX6KEFWW5XNQ2EJXF4PY` (0 s, `final`). The hold is `closed`,
settled 5833. `admin audit --account 23`: "balance invariant holds".

## What passed

- **Submit path, on a real cloud for the first time:** key, template
  resolution, quota, hold, persist, launch. The 201 came back in 2 s.
- **Worker placement, as `docs/staging.md` asks of the first real launch:**
  g5.xlarge on-demand (no spot lifecycle). Profile
  `unify-compute-staging-worker`. Subnet `subnet-09b7ce4de68b7b23a`,
  10.73.7.20, with a public IP. The **only** security group is
  `unify-compute-staging-workers` (`sg-0afdbfd4c04ef1304`; inbound is SSH
  from the staging EIP 34.197.4.93/32 plus the self rule). This settles the
  2026-10-03 "unproven" item: the refused `CreateSecurityGroup` is harmless.
  Tags `Project=unify-compute-workers` and `Environment=staging` are on the
  instance and the volume. IMDSv2 is required, hop limit 1. The root volume
  is 256 GB gp3, encrypted, DeleteOnTermination, and gone after termination.
  AMI `ami-041c37c1cd2c36a67` (`skypilot:custom-gpu-ubuntu-cuda13`,
  20260603).
- **No credentials on the worker.** Checked over SSH from the SkyPilot
  container at 06:48 (VM) and 06:51 (container), while the job was
  provisioning:
  - On the VM, absent: `/home/ubuntu/{.aws,.lambda_cloud,.nebius,.kube,.config/gcloud,.azure,.runpod,.oci}`,
    `/root/.aws`, `/root/.kube`.
  - In `sky_container` (root), absent: `/root/{.aws,.lambda_cloud,.nebius,.kube,.config/gcloud,.azure}`.
    No `AWS_*`, `LAMBDA*`, `NEBIUS*` or `KUBECONFIG` variables.
  - IMDS from the VM and from the container: `unify-compute-staging-worker`.
    That role has one inline policy, `deny-all` (`Deny * *`), and no attached
    policies.
  - SkyPilot's generated cluster YAML mounts only `~/.sky/.runtime_files`,
    and its setup runs `rm ~/.aws/credentials`. The server config has
    `allowed_clouds: [aws]` and `remote_identity: unify-compute-staging-worker`.
- **GPU driver:** NVIDIA A10G, driver **580.159.04**, 23028 MiB. That
  answers `templates.md`'s "which driver SkyPilot's AWS image has is not
  verified": it is new enough for CUDA 13.0.
- **Placement** on the Job and in `job.placed`. **Metering** started at
  cluster `UP` and stopped at the command's end. **Settlement** charged
  35 s at USD 1.20/h, and the final settle released the rest of the hold.
- **Artifacts:** the upload trap ran after the failed command, as designed.
  The manifest was written (`complete: true`, `data: []`), because
  `results.jsonl` was never produced.
- **Teardown:** `sky.down` took 14 s, and the instance was terminated 31 s
  after the command ended. Nothing is left in the workers VPC
  (`vpc-03d3b922813cb0171`): no instance, no ENI, no tagged volume.
  `sky status`: no clusters. The reaper at 06:58:31: `workers: []`,
  `terminated: []`, `max_age_hours` 5, `over_concurrency_limit: false`.
- **Emails** (`notifications`, all `sent`): compute-enabled (the CLI printed
  "emailed"), low-balance (06:45:01, see anomalies), and `job-failed`
  (06:52:39). The SES simulator's success address accepts and discards, so
  only SES's acceptance is observable, not delivery or content.
- Nothing was raised. GPU quota L-DB2E81BA is still 4. Budget action
  `b4302f8f…` is still STANDBY. `platform.json` was not touched. Staging's
  six containers are unchanged: compute-api up 36 h, 0 restarts.

## What failed, and anomalies

1. **The job failed: `nonzero_exit`, "the job's command exited non-zero",
   `retryable: false`, cause unknown.** The command failed 28.8 s after
   SkyPilot started it. That is too soon to have downloaded the 8 GB model,
   so it failed during the input fetch, vLLM's start, or the model config
   or download. The pinned revision `cdbee75f…` exists on Hugging Face
   (200, not gated), and so does `config.json` at that revision.
   Unverified hypothesis: `templates.md` records that this image ships
   torch 2.14.0 (CUDA 13.0) beside torchvision 0.28.0+cu129, which breaks
   as soon as transformers imports vision code. Nothing here proves that.
   It needs the job's stderr.
2. **The logs were lost, and that is the main product finding.**
   - While the job provisioned, `/logs` answered `503 logs_unavailable`
     (`ClusterNotUpError`, status INIT), and compute-api logs that at ERROR
     on every poll. `job-api.md` says a not-yet-launched job has an empty
     body.
   - While it ran, the one read returned **200 with SkyPilot's internal
     text** `'sky.logs' request 42477b0e-… cancelled` as the body. compute-api
     cancelled its own log tail when it issued `sky.down`.
   - After the end, `/logs` returned 200 with an empty body.
   - Not one line of the job's output was ever served, and teardown of
     the worker began 17 s after the command ended. A customer whose template job
     fails gets a code and nothing else, and cannot work around it:
     `spec.command` is not overridable on the curated templates.
3. **Low-balance email on the first submit.** The credit of exactly
   USD 5.00 equals the default threshold (USD 5), so the account was
   armed, and the USD 0.40 hold crossed it. This is what `accounts.md`
   specifies, but every account an operator funds with USD 5 will get a
   low-balance warning on its first job.
4. **The admin CLI records `actor_id = unknown`** on both audit events, so
   the audit trail does not say who ran them.
5. `gpu_seconds` counts from cluster `UP` (06:51:46), 7 s before SkyPilot
   started the job. That is as `metering.md` specifies, and is billed (35 s
   billed against 28.8 s of command). `finished_at` (06:52:39) is when
   compute-api noticed, 17 s after metering ended.
6. From this machine, 2 of about 10 job reads timed out (curl 000), and
   compute-api's request log shows they never arrived. That is local egress,
   not staging.

## What this does not prove

- That a job **succeeds**. No `succeeded` state, no output file, and no
  presigned artifact URL has been seen on a real cloud.
- The 300 s settlement cadence, `extend`, cancel, `max_runtime_seconds`
  enforcement, `balance_exhausted`. The job ended too soon for any of them.
- Why the command failed (above). The next attempt needs the operator's
  decision and one of: logs kept past teardown (`backend.md` OQ4), a delay
  before `sky.down` on failure, or a plain-spec diagnostic run of the same
  image whose command tees its output into `/mnt/out`. That last one is
  another GPU launch, about USD 0.15, and outside this exercise's "no retry
  unless transient".
- The real AWS cost, until Cost Explorer catches up.
- The email contents.

## Left on staging

Account 23: active, compute enabled, available USD 4.9883. Key 2 revoked,
no live session. Job `job_01M51Q9XFVNXGW6KSMCTFZK16J` `failed`. Manifest
`s3://unify-compute-staging-artifacts-871376691052/jobs/job_01M51Q9XFVNXGW6KSMCTFZK16J/_manifest.json`
(30-day retention). No GPU instance.
