# Compute job API — v1 contract

- **Status:** Draft, implemented in `compute-api/`. A deployment with a job
  store (`DATABASE_URL`) verifies keys through the console bridge and runs
  jobs; one without (the "dark build") checks the key's shape only and
  answers every authenticated job route `501` after validation. Behaviour
  marked *planned* is not built in either. No job has been run on a real
  cloud through this API yet.
- Base URL: `https://api.compute.unifyai.us` (production, does not exist yet),
  `https://staging.compute.unifyai.us` (staging, not yet applied).
- All paths are under `/compute/v1`.

## Authentication

`Authorization: Bearer sk-…` with the customer's existing Unify API key. The
same key that calls `api.unifyapi.ai/v1`. compute-api verifies it through the
console bridge (`console-billing-bridge.md` → `verify`) and caches the result
for at most 60 seconds; a revoked key stops working within that window.
Implemented. A key not shaped like one (`sk-` and at least 8 characters) is
`401 missing_key` without a console call. The console's answer maps as
follows; refusals are cached for the same 60 seconds, a console that cannot
answer is not cached at all:

| Console `verify` | compute-api |
|---|---|
| `200` | the key's owner is `tenant:<id>` or `user:<id>` (`quotas.md`) |
| `401 invalid_key` (unknown, disabled or expired key) | `401 authentication_error`, `invalid_key` |
| `403`, `data.status: "disabled"` (user disabled) | `403 authentication_error`, `account_disabled` |
| `403`, `data.status: "suspended"` (tenant suspended) | `403 authentication_error`, `account_suspended` |
| anything else: unreachable, `5xx`, `429`, bridge dark (bodiless `404`), or a `401` for our own signature or clock | `503 console_unavailable` |

A deployment without `CONSOLE_URL` cannot verify any key, so every
authenticated job route answers `503 console_unavailable` there.

A key with `unlimited_quota` set on the console still needs a positive balance
on its billing entity to submit a job, because compute holds against the
entity, not the key.

A Unify Compute key (`uck_…`) sent to a deployment on the console bridge is
`401 invalid_key` without a console call.

### With compute's own accounts (`ACCOUNTS_BACKEND=local`)

Implemented (task A5), deployed nowhere yet; ADR-0011 and `accounts.md`
"API keys". `Authorization: Bearer uck_…` with a key created on the dashboard:
`uck_` and 40 characters from `[0-9A-Za-z]`. Any other shape, `sk-` keys
included, is `401 missing_key` without a database read. The key is looked up
by its SHA-256 on every request, with no cache, so a revoked key fails on the
very next request; `last_used_at` is written at most once a minute.

| Lookup | compute-api |
|---|---|
| active key | the owner is `account:<account_id>` (`quotas.md`) |
| unknown, revoked or expired key, or the account is closed | `401 authentication_error`, `invalid_key` |
| a non-GET route (submit, cancel) with a key that lacks the `jobs` scope | `403 permission_error`, `scope_denied` |
| the database cannot answer | `500 internal_error` |

Scopes: `jobs` submits, cancels and reads; `read` only reads. Submit then
checks the account itself, after the request's own validation and before
quota and the hold: suspended or closed is `403 authentication_error`,
`account_suspended`; job submission not turned on (ADR-0011 D3, by an
operator) is `403 permission_error`, `compute_not_enabled`. A suspended
account's keys still read. The per-key rate limits count per key id.

## Errors

OpenAI-style envelope so existing client error handling carries over:

```json
{ "error": { "type": "invalid_request", "code": "unknown_sku", "message": "sku 'h100-80gb-x1' is not offered", "param": "sku" } }
```

| HTTP | `type` | When |
|---|---|---|
| 400 | `invalid_request` | Malformed body, unknown SKU, runtime over the ceiling |
| 401 | `authentication_error` | Missing, malformed, revoked, or expired key |
| 403 | `authentication_error` | The key is valid but its account may not spend (`account_disabled`, `account_suspended`) |
| 403 | `permission_error` | `ACCOUNTS_BACKEND=local` only: the key lacks the `jobs` scope (`scope_denied`), the account may not run jobs yet (`compute_not_enabled`), or the job's hold would take the key past its monthly cap (`key_cap_exceeded`, set in the dashboard; nothing is submitted) |
| 402 | `insufficient_balance` | Hold refused: balance below the estimate |
| 403 | `quota_exceeded` | Submit would exceed an account quota or a platform-wide limit (one active job across all accounts while the platform starts small); `error.quotas` lists each limit with usage and headroom ([`quotas.md`](quotas.md)). Never a partial grant, no `Retry-After` |
| 404 | `not_found` | Job id unknown *for this key* (no existence leak across users) |
| 409 | `conflict` | Idempotency-Key reused with a different body; cancel on a terminal job |
| 429 | `rate_limited` | Per-key submit or read rate exceeded; always with `Retry-After` |
| 500 | `internal_error` | Placement failed for a reason the API does not distinguish (`placement_failed`); not expected in normal operation |
| 501 | `not_implemented` | The dark build (no job store); `spec.outputs` without artifact storage (`outputs_unsupported`); `follow=true` on logs |
| 503 | `console_unavailable` | Key could not be verified or hold could not be placed |
| 503 | `upstream_unavailable` | The SKU is sound but no provider it offers is enabled in this deployment |
| 503 | `gpu_not_provisioned` | The SKU is listed but none of its providers is enabled here yet; the request is recorded as demand (ADR-0013) and nothing is held |
| 503 | `storage_unavailable` | Artifact storage could not be reached |

## Objects

### Job

```json
{
  "id": "job_01J8Q6ZK3R7Y9W2X4V6T8S0A1B",
  "object": "compute.job",
  "status": "running",
  "sku": "a10g-24gb-x1",
  "created_at": 1757520000,
  "started_at": 1757520042,
  "finished_at": null,
  "spec": {
    "image": "ghcr.io/acme/embed:1.4",
    "command": ["python", "run.py", "--input", "/mnt/in", "--output", "/mnt/out"],
    "env": { "BATCH_SIZE": "256" },
    "inputs": [ { "source": "https://acme-data.s3.amazonaws.com/docs.jsonl?X-Amz-Signature=…", "mount": "/mnt/in" } ],
    "outputs": [ { "mount": "/mnt/out" } ],
    "max_runtime_seconds": 7200
  },
  "usage": {
    "gpu_seconds": 1830,
    "estimated_cost_usd": "2.5400",
    "settled_cost_usd": "0.8600",
    "hold_usd": "2.5400"
  },
  "error": null,
  "placement": { "cloud": "aws", "region": "us-east-1", "instance_type": "g5.xlarge" },
  "metadata": { "pipeline": "nightly-embed" }
}
```

`inputs[].source` is an `https://` URL, public or presigned by the customer.
The run step downloads it into the `mount` directory, named after the URL's
last path segment, before the command runs. `s3://` and other sources are
refused at submit (`400 invalid_input_source`, before anything is held),
until customer buckets are read through a cross-account role (a later ADR;
ADR-0005). `outputs[].mount` is a directory the job writes. Everything
under it is uploaded when the command exits (see
[Artifacts](#get-computev1jobsidartifacts)).

`placement` is null until the job is `running` and says where it landed.
The backend writes it from SkyPilot's cluster record when the cluster is
first seen `UP`, before the job reads `running` ([`backend.md`](backend.md)),
together with a `job.placed` event.

Money is a decimal string in USD, never a float. Timestamps are Unix seconds.

`status` is one of:

```
queued ──▶ provisioning ──▶ running ──▶ succeeded
   │             │             │
   │             ├─────────────┼──▶ failed        (non-zero exit; before running: image pull
   │             │             │                   failure, capacity exhausted on every candidate provider)
   │             ├─────────────┼──▶ interrupted   (spot preemption, provider fault; before running:
   │             │             │                   preempted before the job started) *planned: auto-retry*
   └─────────────┴─────────────┴──▶ cancelled     (customer cancel, or balance exhausted)
```

Allowed transitions:

| From | To |
|---|---|
| `queued` | `provisioning`, `cancelled` |
| `provisioning` | `running`, `failed`, `interrupted`, `cancelled` |
| `running` | `succeeded`, `failed`, `interrupted`, `cancelled` |

`succeeded`, `failed`, `interrupted`, `cancelled` are terminal.

`provisioning → failed` covers a job that never started: the image could not
be pulled, or no candidate provider had capacity. `provisioning → interrupted`
covers a node preempted after it was acquired but before the job started.
Provisioning time is never GPU-billed, whichever way it ends: GPU seconds are
metered only while the job is `running`, from cluster `UP` (rows B2, B4 and B8
of the billable-state table in `docs/spec/metering.md`, proposed in PR #8). A
job that ends from `provisioning` is charged no GPU time and its hold is
released in full.

### SKU

```json
{
  "id": "a10g-24gb-x1", "object": "compute.sku",
  "gpu": "NVIDIA A10G", "accelerator": "A10G", "gpu_count": 1, "gpu_memory_gb": 24, "vcpus": 4, "memory_gb": 16,
  "price_usd_per_hour": "1.2000",
  "tier": "hyperscaler", "interruptible": false,
  "providers": [ { "cloud": "aws", "instance_type": "g5.xlarge", "regions": ["us-east-1"] } ],
  "regions": ["us-east-1"], "available": true
}
```

A SKU with `gpu_count` > 1 also carries `interconnect` (`pcie` or `nvlink`),
which says whether its GPUs are wired for fast multi-GPU work or merely
present; it is absent on single-GPU SKUs, where it has no meaning. Every
provider behind the SKU delivers it, so placement never trades it away
(`spec/upstreams.md` R16). `network` (`ethernet`, `infiniband`) describes the
node-to-node fabric and is informational: multi-node jobs are not in v1.

Prices are per hour for display and per second for metering
(`price_usd_per_hour / 3600`, rounded half-up to 6 decimals at settlement).
The catalogue lives in `compute-api/config/skus.json` and ships with the
binary; a price change is a code change.

`tier` is the trust level of every provider behind the SKU (`hyperscaler`,
`neocloud`, `marketplace`); a job is never placed outside its SKU's tier.
`providers` is the ordered list of places the SKU can run; order is our
preference, and SkyPilot fails over along it. `interruptible` SKUs run on spot
capacity and may end `interrupted`. `regions` is the union of the providers'
regions. See [`upstreams.md`](upstreams.md) and ADR-0006.

## Endpoints

The machine-readable form of this contract is
[`openapi.json`](openapi.json) (OpenAPI 3.1), served by every build at
`GET /compute/v1/openapi.json` (public). The canonical file is
`compute-api/internal/api/openapi.json`, embedded in the binary; the copy here
is kept byte-identical by a test, and a test fails if a route is registered
without an entry there. A change to an endpoint below changes both.

### `GET /compute/v1/skus`

Lists offered SKUs. Public (no auth) so the pricing page can render from it.

### `GET /compute/v1/upstreams`

Every cloud the catalogue references, whether this deployment has it enabled,
and which SKUs it serves. Public, like `/skus`.

```json
{ "object": "list", "data": [ { "object": "compute.upstream", "cloud": "aws", "enabled": true, "skus": ["a10g-24gb-x1"] } ] }
```

### `POST /compute/v1/jobs`

Submits a job. Headers: `Authorization`, optional `Idempotency-Key` (≤ 255
chars, retained 24h; same key + same body returns the original job with `200`,
same key + different body is `409`).

Body: the `spec` object above plus `sku` (required), optional `providers`
(a subset of the SKU's provider clouds, to pin placement; default all), and
optional `metadata` (≤ 16 string pairs). Server fills everything else.

Optional `template`: the id of a template from `GET /compute/v1/templates`.
The template supplies the spec and the default `sku`; the body may then set
only the fields that template lists as overridable, and binds a `source` to
each of its input mounts. The resolved spec goes through the validation below
unchanged. Rules and the template error codes are in
[`templates.md`](templates.md).

Validated before anything is held or persisted, in this order: body parses
(`malformed_body`), `sku` present (`missing_sku`) and offered (`unknown_sku`)
and sold (`sku_unavailable`), `spec.max_runtime_seconds` positive
(`missing_max_runtime`) and at most the deployment's ceiling, 14400 today and
never above 86400 (`runtime_over_ceiling`, upstreams.md R9), every
entry of `providers` offered by the SKU (`provider_not_offered`), at
least one candidate enabled in this deployment (`503 upstream_unavailable`),
and, last, the account's quotas (`403 quota_exceeded`, [`quotas.md`](quotas.md)).
With a job store, three more groups of checks sit between those: after the
runtime, the spec must be launchable as given — `metadata` at most 16 pairs
(`invalid_metadata`), `spec.image` and `spec.command` present
(`missing_image`, `missing_command`), env names valid and not starting
`UNIFY_COMPUTE_` (`invalid_env`, `reserved_env`), every input an `https://`
URL (`invalid_input_source`), mounts absolute (`invalid_mount`), and
`spec.outputs` only where artifact storage is configured
(`501 outputs_unsupported`); after the providers, a SkyPilot API server to
launch on (`503 upstream_unavailable`); and an `Idempotency-Key` of at most
255 characters (`invalid_idempotency_key`). The dark build skips those and
answers what passes `501`.

Flow (implemented): verify key → in one transaction under a per-owner lock,
quota, then `hold` on the console for `sku rate × min(max_runtime_seconds,
7200)` ([`metering.md`](metering.md) H1), then persist the job `queued` with
its `job.queued` event → hand to SkyPilot. An over-quota submit never reaches
the console. If the hold is refused the job is never persisted and
`402 insufficient_balance` is returned with the hold amount and the job's
full ceiling (`max_runtime_seconds × rate`) in `error.message`; a console
that cannot answer is `503 console_unavailable`, also with nothing persisted.
A failure after the hold was placed releases it.

Returns `201` with the Job, reading `provisioning` once the launch is handed
to SkyPilot. A process that does not hold the worker lock (during a rolling
restart, [`backend.md`](backend.md#restart)) persists the job and returns
it `queued`; the lock holder launches it within one poll round. An idempotent replay returns `200` with the original job and
places, persists and launches nothing.

### `GET /compute/v1/jobs/{id}`

Returns the Job. `404` if it does not belong to the calling key's owner.
`usage` is read from the metering ledger: `gpu_seconds` metered so far,
`settled_cost_usd` what the console has confirmed, `hold_usd` the live hold
(`0.0000` once released) and `estimated_cost_usd` the ceiling
`max_runtime_seconds × rate` at the rate snapshotted at submit.

### `GET /compute/v1/jobs?status=running&limit=20&after=job_…`

Cursor list, newest first, owner-scoped. `status` filter optional,
repeatable (`invalid_status`); `limit` 1 to 100, default 20
(`invalid_limit`); `after` must be one of the owner's job ids
(`invalid_cursor`). `next` is present only with `has_more`.

```json
{ "object": "list", "data": [ … ], "has_more": true, "next": "job_…" }
```

### `POST /compute/v1/jobs/{id}/cancel`

Requests termination. Returns `202` with the Job, which normally already
reads `cancelled`; teardown continues afterwards. Answered by a process that
does not hold the worker lock, or for a job whose attempt has not been taken
up since a restart, the Job still reads its earlier status; the cancel is
recorded and carried out at the time it was accepted. `409 job_terminal` on a
job that has already ended. GPU metering stops when the cancel is accepted,
not when the instance is confirmed down; the customer is charged for seconds
run, not for the cancel latency. A job cancelled before it ran has its hold
released in full. Implemented.

### `GET /compute/v1/jobs/{id}/logs?since=…&follow=false`

Plain-text stdout/stderr so far (`text/plain; charset=utf-8`), read from the
job's cluster through the SkyPilot API server; `503 logs_unavailable` when
that read fails. `since` is not supported yet and is ignored.
`follow=true` streams as `text/event-stream` (*planned*; answered `501`).
Logs are retained 7 days after a terminal state (*planned*: nothing keeps
them after teardown yet, so a finished or not-yet-launched job has an empty
body; `backend.md` open question 4).

### `GET /compute/v1/jobs/{id}/artifacts`

Lists the files the job wrote under `outputs[].mount`, each with a presigned
download URL ([ADR-0005](../adr/0005-artifact-storage.md), Proposed).
Implemented; answers `501` in a deployment until the job store, owner
resolution and artifact storage are all configured. `404` if the job does not
belong to the calling key's user.

```json
{
  "object": "list",
  "data": [
    { "object": "compute.artifact", "path": "/mnt/out/results.jsonl", "size": 18234,
      "sha256": "9f86d08…", "url": "https://unify-compute-production-artifacts-….s3.us-east-1.amazonaws.com/jobs/job_…/mnt/out/results.jsonl?X-Amz-…",
      "expires_at": 1757523600 }
  ],
  "complete": true,
  "truncated": false
}
```

- `path` is where the job wrote the file. `sha256` is the hex SHA-256 that
  S3 verified when the file was uploaded.
- `url` is valid until `expires_at`: at most one hour, less if the
  credentials that signed it expire sooner. Call again for fresh URLs.
- `complete` is `false` until the job's upload has finished and written the
  manifest. It stays `false` while the job runs, after a job that ended
  without reaching its upload (cancelled, preempted, stopped at
  `max_runtime_seconds`), and after retention has deleted the files. `data`
  is then empty.
- `truncated` is `true` if the job wrote more than 10 000 files; only the
  first 10 000 by path are listed.
- Outputs are uploaded from the end of the job's own run, whether the command
  succeeded or failed. That time is billed and counts against
  `max_runtime_seconds`, so leave headroom. One file is at most 5 GiB.
- Retained 30 days from upload, then deleted.
- *Planned:* `402` instead of URLs when the account is in arrears (ADR-0008).
  It needs the ledger.

The job's run step uses two internal routes, authenticated by the job's own
upload token rather than a customer key:
`POST /compute/v1/internal/jobs/{id}/artifacts/uploads` (one presigned PUT
per file) and `…/complete` (writes the manifest). Both refuse a terminal job
with `409`. They are not part of the customer contract.

## Metering and settlement (GPU implemented; storage and egress *planned*)

The full rules — three meters, the billable-state table, hold sizing,
settlement cadence, interruption and retry — are in
[`metering.md`](metering.md). What happens at zero balance is
[ADR-0008](../adr/0008-zero-balance-policy.md) (Proposed). In summary:

- GPU seconds accrue from SkyPilot reporting the cluster `UP` until the job's
  process exits, a cancel is accepted, `max_runtime_seconds` is reached, or the
  cluster goes down, whichever is first. Provisioning, including a retry's, is
  never billed.
- Every 5 minutes while running, and once at termination, compute-api writes a
  `usage_event` (job id, from, to, seconds, sku, rate, amount) to its own
  append-only ledger, then calls `settle` on the console with the cumulative
  amount. The ledger is the source of truth; the console call is idempotent on
  the event id and retried until it succeeds.
- The hold is kept at least one hour of SKU rate ahead of usage by `extend`.
  If an extension is refused (`402`) and the hold falls below 10 minutes of
  rate, the job is cancelled with `error.code = balance_exhausted`; if the
  console is unreachable for that long, with `error.code = billing_unavailable`.
- At terminal state, a final `settle` releases the remainder of the hold.
- Artifact storage (GB-hours, from terminal state until deletion) and egress
  (GB downloaded, from the artifacts bucket's S3 server access logs) are
  charged once a day per account, not against the job's hold.

## Rate limits

Per key: 60 submits/hour, 600 reads/minute (`GET` on `/jobs`, `/jobs/{id}`,
`/logs`, `/artifacts`, `/events`). Returned as `429` with `Retry-After` in
seconds.
Enforced per process, in memory; details and limits of that in
[`quotas.md`](quotas.md#per-key-rate-limits). Cancel is not limited yet.

Account quotas (concurrent jobs and GPUs per tier) are a separate mechanism,
per billing entity rather than per key, and refuse with `403`; see
[`quotas.md`](quotas.md).

## Not in v1

Multi-node jobs, interactive sessions/SSH, persistent volumes, custom VPC
attachment, bring-your-own-cloud-account. Each is a later ADR.
