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

**Root cause, found by the diagnostic rerun below:** the pinned vLLM image
cannot start. `vllm` dies at import with `RuntimeError: operator
torchvision::nms does not exist`, because its torchvision does not match
its torch. Neither the platform nor the GPU is at fault.

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
   The rerun found the cause: torchvision does not match torch in this
   image. See "Diagnostic rerun".
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
- Why run 1's command failed, by its own output. The rerun below found
  the cause, and run 1 almost certainly shared it, but run 1's output is
  gone.
- The real AWS cost, until Cost Explorer catches up.
- The email contents.

## Left on staging

Account 23: active, compute enabled, available USD 4.9883. Key 2 revoked,
no live session. Job `job_01M51Q9XFVNXGW6KSMCTFZK16J` `failed`. Manifest
`s3://unify-compute-staging-artifacts-871376691052/jobs/job_01M51Q9XFVNXGW6KSMCTFZK16J/_manifest.json`
(30-day retention). No GPU instance.

## Diagnostic rerun (UTC, 2026-10-16)

The coordinator approved one diagnostic rerun within the operator's
original USD 2 cap. This was not a retry of the template: it was a plain
spec, because `spec.command` cannot be overridden on a curated template.
It used the template's exact image and env (`MODEL`, `MODEL_REVISION`,
`MAX_MODEL_LEN`), the same input (freshly presigned) at `/mnt/in`, an output
mount `/mnt/out`, `max_runtime_seconds` 1200, and the command
`bash -c 'set -o pipefail; { nvidia-smi; python -c "…versions…"; vllm
run-batch … ; } 2>&1 | tee /mnt/out/job.log'`, so the output would
survive teardown as an artifact.

**Account 24, not 23.** Account 23's password went with the first run's
scratch files, and a reset token cannot be recovered from its digest.
Signing in again would have meant writing to the database, so instead a
new throwaway, `success+cicd-diag-1016@simulator.amazonses.com` (account
24), was signed up and verified as before. `enable-compute` gave audit
`ae_01M51RQMSJW3JQ5FDPKJQTB65V`, and `balance credit --usd 5.00` gave
`ae_01M51RQN54ZAWCGH0MX4SFJBAX`. Key id 3 (`uck_QWkpolwS…`, `jobs`) was
revoked after the run, and the session was logged out. Account 23 is
unchanged.

Job `job_01M51RS5SME90CREBC70MVACNJ`, `Idempotency-Key:
first-real-job-diag-2026-10-16-a1`.

| Time | What |
|---|---|
| 07:10:48 | Submit; 201 `provisioning` at 07:10:50, hold USD 0.4000 |
| 07:10:56 | `i-0366850013f660792` launched (g5.xlarge, workers SG, worker profile) |
| 07:11:18–07:16:49 | `initialize_docker` (image pull), 5 m 31 s |
| 07:17:33 | "Cluster launched" |
| 07:17:42.3 | SkyPilot job 1 starts; `nvidia-smi` prints at 07:17:44 |
| 07:17:53 | compute-api sees `UP`: `job.placed`, `job.running`; metering starts |
| 07:18:11.5 | SkyPilot job 1 `FAILED` (the command ran 29.2 s; run 1's ran 28.8 s) |
| 07:18:19 | `job.failed` `nonzero_exit`; settle and release; `sky.down` |
| 07:18:32 | `sky.down` done; terminate requested. `terminated` by 07:23:49 |

Provisioning took 7 m 03 s, and the instance ran for about 7 m 36 s until
the terminate request.

**Cost:** `gpu_seconds` 18, settled **USD 0.0060** (3000 units), USD 0.3940
released. Account 24 is at available USD 4.9940, held 0, and `admin audit`
says the invariant holds. AWS cost is about USD 0.13 (estimate). **The
exercise as a whole: about USD 0.26 of AWS GPU time against the USD 2 cap,
and USD 0.0177 charged to the two throwaway accounts.**

**Artifacts:** `complete: true`, one file, `/mnt/out/job.log` (5068 B,
sha256 `f91da366bb1b…`), downloaded through its presigned URL (200).
`/logs` was again empty after the end. That was the gap this rerun worked
around.

**The decisive lines in job.log:**

```
| NVIDIA-SMI 580.159.04             Driver Version: 580.159.04     CUDA Version: 13.0     |
bash: line 1: python: command not found
  File "/usr/local/lib/python3.12/dist-packages/vllm/transformers_utils/config.py", line 21, in <module>
    from transformers.models.auto.image_processing_auto import get_image_processor_config
  ...
  File "/usr/local/lib/python3.12/dist-packages/transformers/image_utils.py", line 54, in <module>
    from torchvision.io import ImageReadMode, decode_image
  File "/usr/local/lib/python3.12/dist-packages/torchvision/_meta_registrations.py", line 163, in <module>
    @torch.library.register_fake("torchvision::nms")
RuntimeError: operator torchvision::nms does not exist
```

**Root cause.** In `vllm/vllm-openai:v0.30.0-cu129@sha256:a67f8f18…`, the
`vllm` CLI imports `vllm.config`, which imports transformers' image
processing, which imports torchvision. torchvision's compiled ops do not
match the installed torch, so loading it raises `operator torchvision::nms
does not exist`, and `vllm` exits before it reads any argument. This is the
torch 2.14.0 (CUDA 13.0) beside torchvision 0.28.0+cu129 mismatch that
`templates.md` already records for this base image. The finetune image
works around it by reinstalling torchvision 0.29.0+cu130. The batch
templates use the base image unchanged.

- **Run 1 almost certainly failed the same way.** It was the same image and
  the same entry point, the command ran within 0.4 s of the same time, and
  it ended before any model download. Run 1's own output was lost, so this
  is inferred, not observed.
- **It does not depend on the GPU, the driver (580.159.04, CUDA 13.0) or
  the platform.** The error is at import time, so it can be reproduced
  without a GPU, on any amd64 host:
  `docker run --rm --entrypoint vllm <image> --help`.
- **Every template on this image fails today:** `batch-infer-vllm` and
  `batch-embed`. The finetune image (`batch-infer-lora`, `finetune-lora`)
  is a different build and was not exercised.
- The diagnostic one-liner's `python -c` printed nothing: the image has
  `python3`, not `python`. The traceback alone is decisive.

**Anomaly, new in the rerun:** metering started 11 s **after** the command
did. compute-api saw the cluster `UP` at 07:17:53, but the job had started
at 07:17:42. In run 1 it started 7 s **before**. So `gpu_seconds` follows
compute-api's status poll, not the cluster's `UP`, in both directions.
That matters little at this size, but the meter is off by up to one poll
interval at each end.

Teardown: no instance, ENI or tagged volume is left in the workers VPC.
The reaper at 07:21:20 reported `workers: []`. SkyPilot shows no clusters.
The quota, the caps and the budget are unchanged.

### What the rerun leaves to decide (not done here, no code changed)

- Fix the vLLM image the batch templates pin. Options: build it like the
  finetune image (torchvision 0.29.0+cu130), or pin another vLLM tag whose
  torch and torchvision match. Then prove it without a GPU with
  `vllm --help`, then with one more GPU run.
- The two log findings above (anomaly 2) stand. Had this command not teed
  into `/mnt/out`, the cause would have been lost a second time.
- Until the image is fixed, `batch-infer-vllm` and `batch-embed` will fail
  for every customer, after about 7 minutes of provisioning, and they read
  "Available".
