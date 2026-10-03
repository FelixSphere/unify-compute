# ADR-0005 — Job artifacts live in our S3, uploaded by the run step, served by presigned URL

- **Status:** Proposed
- **Date:** 2026-09-21 (first draft), amended 2026-10-02
- **Origin:** the first draft was written in the finetune worktree
  (`unify-compute-finetune`, uncommitted, for the reference fine-tuning
  run). Its storage decision — our S3, one bucket per environment,
  `jobs/<job_id>/`, one-hour signed URLs, 30-day retention — is kept here
  unchanged. This version adds how outputs get there without mounting the
  bucket, how one job is kept out of another's files, what inputs are allowed
  for now, and where the meters come from.
- **Related:** ADR-0001 (the console owns the balance), ADR-0003 (SkyPilot),
  ADR-0008 (zero balance: arrears and deletion), ADR-0009 (one module, two
  environments), [`backend.md`](../spec/backend.md) open question 1,
  [`metering.md`](../spec/metering.md) (storage and egress meters).

## Context

The operator chose `batch-infer-vllm` as the first job to make work end to
end (2026-10-03). It reads a prompts file and writes results that must
outlive the instance and be downloadable by the customer. The reference
fine-tune (R1) writes a LoRA adapter of a few hundred MB with the same needs.

The SkyPilot backend (PR #22) refuses `spec.outputs`. SkyPilot 0.13.0 can
`MOUNT` only a whole bucket (`sky/data/storage.py:1221-1237`,
`StorageModeError`), and mounting a bucket shared by every job would give
each job every other job's files. `COPY` mode only copies *into* the
cluster, so it cannot carry outputs out.

Two facts checked while writing this shaped the design:

- **AssumeRole from the instance role is role chaining, capped at one
  hour.** compute-api runs on EC2 with an instance role, so any role it
  assumes is chained, and AWS refuses `DurationSeconds` above 3600 for a
  chained session whatever the role's `MaxSessionDuration`
  ([IAM: role chaining](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles.html#iam-term-role-chaining);
  the error is "The requested DurationSeconds exceeds the 1 hour session
  limit for roles assumed by role chaining"). The obvious design —
  credentials minted at launch for `max_runtime + margin` (up to 12 h) and
  passed to the job as env vars — cannot be built: they would expire an
  hour after launch, before most jobs finish.
- **What is on the node.** The job runs inside the customer's image.
  SkyPilot installs `rsync curl wget patch openssh-server python3-pip fuse`
  into every container it sets up on a VM (`sky/provision/docker_utils.py:431`)
  and `rsync curl wget …` into every Kubernetes pod
  (`sky/templates/kubernetes-ray.yml.j2:936`), both assuming a Debian-based
  image. The AWS CLI is not installed. SkyPilot installs it into its own
  runtime only when an `s3://` COPY needs it (`sky/cloud_stores.py:60`), and
  `vllm/vllm-openai` does not ship it.

## Decision

### Storage

- **Outputs** go to one bucket per environment in the UnifyAI AWS account,
  `unify-compute-<env>-artifacts-<account>` (the module's naming, like the
  backups bucket), under `jobs/<job_id>/`. The key of a file is
  `jobs/<job_id>` followed by the absolute path the job wrote it at, e.g.
  `jobs/job_…/mnt/out/results.jsonl`. SSE-S3, public access blocked,
  ownership enforced, versioning off, TLS-only bucket policy.
- **`jobs/<job_id>/_manifest.json`** lists every file, `{path, key, size,
  sha256}`. compute-api writes it, from what S3 holds, when the run step
  reports it is done. A mount cannot be `/`, so no output can collide with
  the manifest's key.
- **Downloads** go only through `GET /compute/v1/jobs/{id}/artifacts`
  (owner-scoped): presigned `GET`s valid for one hour. If the instance-role
  credentials that sign them expire sooner, `expires_at` says so. A
  presigned URL dies with the credentials that signed it.

### Upload: the run step, curl, one presigned PUT per file

Options considered:

| | Verdict |
|---|---|
| `MOUNT` the shared bucket | Rejected outright: every job sees every job's files. **Never mount the shared bucket.** |
| A bucket per job, mounted | Isolated, but bucket sprawl (an account's bucket quota, a lifecycle and logging config per bucket), and a copy into this layout after the job |
| SkyPilot `COPY` file mount with scoped credentials | Inbound only. It cannot upload. |
| `aws s3 sync` in the run step, with scoped credentials in env | No CLI on the node. Installing it at the end of the job (pip, or SkyPilot's runtime) is a network dependency at the moment outputs are most at risk. The credentials would also expire after an hour (above). |
| A small static uploader binary | Has to be delivered to the node. Our raw-REST client cannot upload local file mounts through the API server, so it would be downloaded with curl. At that point curl can do the upload itself. |
| **curl, with one presigned PUT per file from compute-api** | **Chosen.** Only curl, find and sha256sum are needed. No AWS credentials ever reach the node, and no credential lifetime has to cover the job. |

How it works (`compute-api/internal/artifacts`, plugged into the backend
through `backend.TaskOptions.Outputs`):

1. At launch, compute-api adds two env vars to the task:
   `UNIFY_COMPUTE_ARTIFACTS_URL` (this compute-api's callback for the job)
   and `UNIFY_COMPUTE_ARTIFACTS_TOKEN`, an HMAC token over (owner, job id,
   expiry = launch + `max_runtime_seconds` + 2 h). It wraps the customer's
   command in a run script that creates the output mounts, runs the
   command, and from an **EXIT trap** uploads every regular file under
   them (symlinks are not followed), however the command ended. The script
   exits with the command's status. Customer env names starting
   `UNIFY_COMPUTE_` are refused so they cannot shadow these.
2. For each file, the node sends `{path, size, sha256}` with its token to
   `POST /compute/v1/internal/jobs/{id}/artifacts/uploads`. compute-api
   checks the token, that the job is **not terminal**, and that `path` is a
   clean absolute path under one of the job's own `spec.outputs`. It
   returns a presigned `PUT` for `jobs/<job_id><path>` that is valid for at
   most an hour, with `x-amz-checksum-sha256` as a signed header.
3. The node `curl -T`s the file. S3 refuses a body whose SHA-256 differs.
4. The node calls `…/complete`. compute-api lists the prefix, `HEAD`s each
   object with checksum mode, and writes the manifest.

Limits, stated rather than hidden: one file is at most 5 GiB (single PUT;
multipart is not built), and one job at most 10 000 files. An upload counts
as run time: it is billed GPU time and counts against
`max_runtime_seconds`. A job that is cancelled, preempted or stopped at its
cap is torn down by the backend and does not reach its upload, so its
artifact list stays empty (`complete: false`).

### Isolation

What keeps job A out of job B's files, layer by layer:

1. **No mount, no credentials on the node.** The node can only use the
   URLs it is given. Each URL names one key and binds one body.
2. **Keys are built by compute-api,** from the job id in the token-checked
   path and a file path that must lie under that job's declared outputs
   (`..`, relative paths, other mounts and the mount itself are refused).
3. **The signer cannot write elsewhere either.** Upload URLs and the
   manifest are signed by a session of `<name>-artifacts-writer`, assumed
   per job with an inline session policy allowing only `s3:PutObject` and
   `s3:AbortMultipartUpload` on `arn:aws:s3:::<bucket>/jobs/<job_id>/*`. A
   bug in step 2 still cannot produce a valid URL for another job's prefix.
   The role itself is trusted only by the instance role and can do nothing
   but put under `jobs/*`.
4. **The instance role cannot write at all.** It may assume the writer,
   `GetObject` under `jobs/*` and `ListBucket` with prefix `jobs/`, for
   manifests, downloads and the storage meter.
5. **The token dies with the job.** It is refused once the job is terminal
   or past its expiry. It travels in the task's env, so the customer's own
   code can read it, and so can SkyPilot's request records. What it buys is
   writes of checksummed files into that same job's prefix while the job
   runs, which the job could do anyway.
6. **Reads are owner-scoped** like every job route: another owner's job id
   is `404`, and download URLs are signed only for keys under the
   requested job's prefix.

The token secret is `/unify-compute-<env>/artifacts_token_secret` (SSM
SecureString, `scripts/staging-secrets.sh`). Without it, compute-api leaves
uploads off and the artifact routes answer `501`. Rotating it invalidates
the tokens of jobs running at that moment.

The instance's `http_put_response_hop_limit` is 2 so that compute-api, in a
container, can reach IMDSv2. Both instances already ran with 2 (the AL2023
default). The module now says so explicitly. It also means **every container
on the host can obtain the instance role**, including staging's console. See
open question 3.

### Inputs

For now an input is an **https:// URL**: public, or presigned by the
customer (an S3 presigned GET, a GCS signed URL). The backend refuses
anything else at launch when `TaskOptions.HTTPSInputsOnly` is set, which a
deployment does. Submit does not check yet: the existing template tests pin
`s3://` template inputs to later validation outcomes, and changing them
needs the operator. SkyPilot reads an `https://` file-mount source as Azure
Blob (`sky/cloud_stores.py:1053`), so the run step fetches it with curl
into the `mount` directory before the command runs, named after the URL's
last path segment. For a presigned S3 URL that is the object's name, so
`batch-infer-vllm`'s `/mnt/in` gets `requests.jsonl`. A failed fetch fails
the job.

`s3://` inputs are refused because SkyPilot would copy them with **its own**
cloud credentials, which are ours. A customer could name any bucket those
credentials can read, including this one. Reading private customer buckets
through a cross-account role the customer grants (the first draft's
proposal) is **deferred to a later ADR**. That ADR has to say how the role is
assumed for the copy without handing our credentials to the node.

A later job can read an earlier job's artifacts by passing a download URL
from `GET /jobs/{id}/artifacts` as an input, within that URL's hour. A
by-job-id reference that resolves to the same prefix without a second copy
(the first draft) is not built.

### Retention

A lifecycle rule expires objects under `jobs/` 30 days after creation,
which is when the run step uploaded them, at the end of the run. That is at
most the upload's duration before `finished_at`. S3 lifecycle runs once a day,
so objects can outlive the 30 days by up to a day. The storage meter stops
at `finished_at` + 30 days regardless (metering.md); the lag is ours.
Deletion for arrears (ADR-0008) is compute-api's job and is not built.
Longer retention is a product decision, not a default.

### Meters and cost lines

- **Storage:** `ListObjectsV2` over `jobs/<job_id>/`, as metering.md
  specifies. The instance role's `ListBucket` covers it. The sampler is C5's
  and is not built.
- **Egress:** S3 server access logs, which are now on. They go to
  `unify-compute-<env>-artifacts-logs-<account>` under `s3/`, date-partitioned
  by event time, SSE-S3, kept 90 days. Delivery is granted by bucket policy
  to `logging.s3.amazonaws.com`, restricted to this bucket and account. The
  collector and its read permission on the log bucket land with C5.
- Upload traffic into S3 is free. A node on another cloud pays that cloud's
  egress to reach S3. Downloads are ours to pay until the egress meter
  charges them on. The prices in `meters.json` are still placeholders.

## Consequences

- PR #22's refusal of `spec.outputs` stands when no uploader is configured
  and is lifted when one is. `GET /jobs/{id}/artifacts` and the two internal
  upload routes are implemented and tested against an STS/S3 fake and the
  real script under bash and curl. They answer `501` in a deployment until
  the job store and owner resolution are wired into the binary (with C4's
  submit wiring). Nothing has run on a real node or bucket.
- The arrears check before signing download URLs (ADR-0008, metering.md
  "Pre-check at URL issuance") needs the ledger (C5) and is not made yet.
- Data residency is us-east-1 only until there is a second region.
- **Apply** (CI/CD agent, staging first; plan of 2026-10-02: 18 to add,
  0 to change, 0 to destroy in each environment, instances untouched):
  1. `scripts/staging-secrets.sh --env staging` creates
     `artifacts_token_secret` and keeps the existing secrets.
  2. `terraform apply` in `deploy/aws/envs/staging`.
  3. A running host does not see the new `.env` lines: `user_data` is
     ignored after creation (ADR-0009). Either rebuild the host by
     ADR-0009's procedure, or add `AWS_REGION`, `ARTIFACTS_BUCKET`,
     `ARTIFACTS_WRITER_ROLE_ARN` (both in SSM),
     `ARTIFACTS_TOKEN_SECRET` and `PUBLIC_URL` to
     `/opt/unify-compute/compose/.env` and the matching lines to the
     host's `docker-compose.yml` over SSM. Either way it is a deliberate
     change to a running host.
  4. To exercise: a job with `outputs` whose command fails after writing
     two files ends `failed`, and `GET /jobs/{id}/artifacts` lists both
     files with working URLs. Another owner gets `404` for the same id. A
     PUT URL replayed against another job's key is refused. One download
     appears in the access-log bucket within a few hours.
  5. Then production, the same way.
- The first draft put the reference run under `finetune/<run_id>/`. The
  reference run is submitted as a job (R1), so it lives under `jobs/` like
  any other. Nothing grants `finetune/*`.
- Supersedes nothing. Closes open question 4 in `docs/architecture.md` and
  backend.md open question 1 once accepted.

## Open questions

1. **Files over 5 GiB.** A fine-tuned 7B model's merged weights are about
   14 GB. Multipart through presigned `UploadPart` URLs fits the same model
   (compute-api creates and completes the upload with the writer session;
   the node splits with `dd`). Needed before R1 writes merged weights, not
   for `batch-infer-vllm`.
2. **Non-Debian images.** SkyPilot's container setup assumes `apt-get`; the
   script assumes the curl it installs. An image without curl uploads
   nothing and says so in the job log. Whether to refuse such images at
   submit is open.
3. **Containers and the instance role.** With hop limit 2, staging's console
   container (AGPL, third-party code) can obtain the instance role and read
   every job's artifacts. It could already do that before this change, since
   the limit was already 2, but now there is something worth reading.
   Options: a `DOCKER-USER` iptables rule that drops 169.254.169.254 from
   every container but compute-api, or moving the console off the host.
   Production runs no console.
4. **Upload after the cap.** A job that hits `max_runtime_seconds` is torn
   down before it can upload. A grace period would bill the customer for it
   (it is run time), and the backend's cap handling (B16) would need to
   change. For now the advice is to leave headroom.
