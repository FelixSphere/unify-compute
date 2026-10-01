# Console billing bridge — contract and console change list

- **Status:** Spec. Not implemented on either side. `compute-api/internal/console`
  holds the client (request signing and shapes); the console fork holds no
  server for it yet.
- Direction: compute-api → console. The console never calls compute.
- Licence note: the server half is written in `FelixSphere/unifyapi-console`
  as new files under AGPL. It exposes existing console behaviour (validate a
  key, move quota, write a consume log). It contains no compute logic.

## Why a bridge rather than direct DB access

compute-api must not read or write the console's database. Two writers on
`users.quota` with the console's own Redis cache and batch-update path in
between is how balances stop reconciling; the fork's `AGENT-WORKFLOW.md` already
records that tenant balances are not authoritative in the hot path for the
same reason. Every quota movement goes through the console's own code paths.

## Trust boundary

Server-to-server, modelled on the console's existing Builder bridge:

- `X-Compute-Timestamp`: Unix seconds. Requests older than 30 s are rejected.
- `X-Compute-Signature`: hex HMAC-SHA256 over
  `METHOD + "\n" + PATH + "\n" + TIMESTAMP + "\n" + EXACT_BODY`
  with the shared secret `COMPUTE_BRIDGE_SECRET` (≥ 32 chars).
- The secret is per environment. Staging and production never share one.
- Missing `COMPUTE_BRIDGE_SECRET` on the console disables the routes entirely
  (`404`), same as the Builder bridge.
- Routes sit under `/api/compute/v1/` and are excluded from the browser
  session auth; the HMAC is the only credential. They are rate-limited by the
  console's global API limiter.

The client implementation is `compute-api/internal/console/sign.go`; its test
pins the canonical string so both sides can be checked against the same vector.

## Quota unit

Amounts cross the bridge as `quota` integers in the console's unit
(`QuotaPerUnit = 500000` per USD 1). compute-api converts from USD at the edge:
`quota = round_half_up(usd × 500000)`. The console never sees a USD string; the
customer never sees a quota integer.

## Endpoints

All bodies are JSON. All responses use the console's `{ "success": bool,
"message": string, "data": … }` envelope so the console's existing handlers
and tests apply.

### `POST /api/compute/v1/verify`

Resolve a customer key to a billing identity.

Request:
```json
{ "key": "sk-…" }
```

Response `data`:
```json
{
  "user_id": 1042,
  "tenant_id": 0,
  "token_id": 771,
  "group": "default",
  "status": "active",
  "remain_quota": 12500000,
  "token_unlimited": false,
  "token_remain_quota": 4000000
}
```

Semantics: same checks as `TokenAuth` on the relay path (key exists, enabled,
not expired, user enabled), without the model-specific header handling.
`remain_quota` is the billing entity's balance (tenant if `tenant_id > 0`,
else user). Suspended tenants return `status: "suspended"` and compute treats
that as a refusal.

Errors: `401` unknown/disabled key (`success: false`, message from the
console's existing i18n), `403` suspended.

### `POST /api/compute/v1/holds`

Reserve quota against the billing entity for a job.

Request:
```json
{ "hold_id": "hold_01J8…", "user_id": 1042, "token_id": 771, "quota": 1270000, "job_id": "job_01J8…", "sku": "a10g-24gb-x1", "expires_at": 1757606400 }
```

Response `data`:
```json
{ "hold_id": "hold_01J8…", "quota": 1270000, "remain_quota": 11230000 }
```

Semantics: decrement the billing entity's quota and the token's `remain_quota`
(unless unlimited) atomically, mirroring `PreConsumeTokenQuota` plus the
entity-level pre-consume, and persist a `compute_holds` row. Idempotent on
`hold_id`: a replay returns the original row with `200`. Refuse with `402` if
`remain_quota < quota`. Expired holds (past `expires_at`) are released by a
console-side sweeper and reported to compute on the next `settle`/`release` as
`409 hold_expired`.

### `POST /api/compute/v1/holds/{hold_id}/settle`

Charge metered usage against a hold.

Request:
```json
{ "event_id": "ue_01J8…", "cumulative_quota": 430000, "gpu_seconds": 1290, "sku": "a10g-24gb-x1", "final": false }
```

Response `data`:
```json
{ "hold_id": "hold_01J8…", "settled_quota": 430000, "hold_remaining": 840000 }
```

Semantics: the console moves `cumulative_quota − previously_settled` from the
hold to consumed, and writes one consume-log row per call through
`RecordConsumeLog` with `model_name = "compute/<sku>"` so it appears in the
customer's usage view and in tenant reporting. Idempotent on `event_id`.
`cumulative_quota` above the hold is refused with `409 hold_exceeded`; compute
must extend first. `final: true` settles and releases the remainder in one
call.

### `POST /api/compute/v1/holds/{hold_id}/extend`

Request: `{ "additional_quota": 300000 }`. Same refusal rules as creating a
hold (`402`). Response `data` echoes the new `quota` and `remain_quota`.

### `POST /api/compute/v1/holds/{hold_id}/release`

Return the unsettled remainder to the billing entity and the token. Idempotent.
Response `data`: `{ "released_quota": 840000, "remain_quota": 12070000 }`.

## Console change list (minimal, all new files unless noted)

Following the fork's rule "prefer adding a file over editing one". Files:

```
model/compute_hold.go            compute_holds table (hold_id PK, user_id, tenant_id, token_id,
                                 job_id, sku, quota, settled_quota, status, expires_at, timestamps)
                                 + compute_settlements table (event_id PK, hold_id, quota_delta, gpu_seconds)
model/compute_hold_test.go       hold/settle/release/expire, idempotency, tenant vs untenanted user
controller/compute_bridge.go     HMAC verification, the five handlers, envelope responses
controller/compute_bridge_test.go signature vector shared with compute-api, replay window, disabled-when-unset
router/api-router.go             one UNIFYAPI-BRAND block: apiRouter.Group("/compute/v1") wiring   (upstream file, ~8 lines)
model/main.go                    one UNIFYAPI-BRAND line: AutoMigrate the two tables               (upstream file, already touched by the fork)
docs/compute-bridge.md           copy of this contract from the console's point of view
```

Environment: `COMPUTE_BRIDGE_SECRET`. No new dependency (`crypto/hmac` is
stdlib; the Builder bridge already does the same).

Pricing: the console does **not** get a compute price table. It moves quota
integers it is told to move. Prices live in compute-api so the console's
"three prices, never mixed" rule (`docs/HOW-PRICING-WORKS.md`) is not disturbed.

Consume log: `model_name = "compute/<sku>"`, `prompt_tokens = 0`,
`completion_tokens = 0`, `quota = delta`, `other = { "gpu_seconds": …, "job_id": …, "hold_id": … }`.
Reconciliation and tenant usage already sum `quota` per log row, so compute
spend appears in existing reports without further changes. The catalogue-based
credit-supply draw-down ignores an unknown model name and draws nothing, which
is correct: compute does not consume supplier credit lots.

## Release order

1. Console: ship the bridge with `COMPUTE_BRIDGE_SECRET` unset (routes dark).
2. Staging: set the secret on both sides, run compute-api's bridge smoke.
3. Only then does compute-api get a production console URL.

Owned by the UnifyAPI CI/CD agent for step 1 and the unify-compute CI/CD agent
for steps 2–3.
