# Compute job API — v1 contract

- **Status:** Draft, implemented as `501` stubs in `compute-api/`. Shapes here
  are what the stubs return and what tests pin; behaviour marked *planned* is
  not built.
- Base URL: `https://api.compute.unifyai.us` (production, does not exist yet),
  `https://staging.compute.unifyai.us` (staging, not yet applied).
- All paths are under `/compute/v1`.

## Authentication

`Authorization: Bearer sk-…` with the customer's existing UnifyAPI key. The
same key that calls `api.unifyapi.ai/v1`. compute-api verifies it through the
console bridge (`console-billing-bridge.md` → `verify`) and caches the result
for at most 60 seconds; a revoked key stops working within that window.

A key with `unlimited_quota` set on the console still needs a positive balance
on its billing entity to submit a job, because compute holds against the
entity, not the key.

## Errors

OpenAI-style envelope so existing client error handling carries over:

```json
{ "error": { "type": "invalid_request", "code": "unknown_sku", "message": "sku 'h100-80gb-x1' is not offered", "param": "sku" } }
```

| HTTP | `type` | When |
|---|---|---|
| 400 | `invalid_request` | Malformed body, unknown SKU, runtime over the ceiling |
| 401 | `authentication_error` | Missing, malformed, revoked, or expired key |
| 402 | `insufficient_balance` | Hold refused: balance below the estimate |
| 404 | `not_found` | Job id unknown *for this key* (no existence leak across users) |
| 409 | `conflict` | Idempotency-Key reused with a different body; cancel on a terminal job |
| 429 | `rate_limited` | Per-key submit rate exceeded |
| 501 | `not_implemented` | Route exists, backend not built (current state) |
| 503 | `console_unavailable` | Key could not be verified or hold could not be placed |
| 503 | `upstream_unavailable` | The SKU is sound but no provider it offers is enabled in this deployment |

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
    "inputs": [ { "source": "s3://acme-data/docs/", "mount": "/mnt/in" } ],
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

`placement` is null until the job is `running` and says where it landed
(*planned*; the backend fills it from SkyPilot's cluster record).

Money is a decimal string in USD, never a float. Timestamps are Unix seconds.

`status` is one of:

```
queued ──▶ provisioning ──▶ running ──▶ succeeded
   │             │             │
   │             │             ├──▶ failed        (non-zero exit, image pull error)
   │             │             ├──▶ interrupted   (spot preemption, provider fault) *planned: auto-retry*
   └─────────────┴─────────────┴──▶ cancelled     (customer cancel, or balance exhausted)
```

`succeeded`, `failed`, `interrupted`, `cancelled` are terminal.

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

Validated before anything is held or persisted, in this order: body parses
(`malformed_body`), `sku` present (`missing_sku`) and offered (`unknown_sku`)
and sold (`sku_unavailable`), `spec.max_runtime_seconds` positive
(`missing_max_runtime`) and at most 86400 (`runtime_over_ceiling`), every
entry of `providers` offered by the SKU (`provider_not_offered`), and at
least one candidate enabled in this deployment (`503 upstream_unavailable`).

Flow (*planned*): verify key → price estimate = `max_runtime_seconds × sku rate`
→ `hold` on the console → persist job `queued` → hand to SkyPilot. If the hold
is refused the job is never persisted and `402` is returned with the estimate
in `error.message`.

Returns `201` with the Job.

### `GET /compute/v1/jobs/{id}`

Returns the Job. `404` if it does not belong to the calling key's user.

### `GET /compute/v1/jobs?status=running&limit=20&after=job_…`

Cursor list, newest first. `status` filter optional, repeatable.

```json
{ "object": "list", "data": [ … ], "has_more": true, "next": "job_…" }
```

### `POST /compute/v1/jobs/{id}/cancel`

Requests termination. Returns `202` with the Job (`status` may still read
`running` until SkyPilot confirms). `409` on a terminal job. Metering stops at
the moment the instance is confirmed down; the customer is charged for seconds
run, not for the cancel latency.

### `GET /compute/v1/jobs/{id}/logs?since=…&follow=false`

Plain-text stdout/stderr. `follow=true` streams as `text/event-stream`
(*planned*). Logs are retained 7 days after a terminal state.

### `GET /compute/v1/jobs/{id}/artifacts`

Lists files written under `outputs[].mount`, each with a signed download URL
valid for 1 hour (*planned; storage location is an open question in
`architecture.md`*).

## Metering and settlement (*planned*)

- GPU seconds accrue from SkyPilot reporting the cluster `UP` until it reports
  down or the job's process exits, whichever is first.
- Every 5 minutes while running, and once at termination, compute-api writes a
  `usage_event` (job id, from, to, seconds, sku, rate, amount) to its own
  append-only ledger, then calls `settle` on the console with the cumulative
  amount. The ledger is the source of truth; the console call is idempotent on
  the event id and retried until it succeeds.
- When cumulative settled amount reaches the hold, compute-api tries to extend
  the hold by one more interval; if refused (`402`), the job is cancelled with
  `error.code = balance_exhausted`.
- At terminal state, the remainder of the hold is released.

## Rate limits

Per key: 60 submits/hour, 600 reads/minute. Returned as `429` with
`Retry-After`. (*planned; the skeleton enforces none.*)

## Not in v1

Multi-node jobs, interactive sessions/SSH, persistent volumes, custom VPC
attachment, bring-your-own-cloud-account. Each is a later ADR.
