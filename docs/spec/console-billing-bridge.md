# Console billing bridge — contract and console change list

- **Status:** Spec, last revised 2026-10-02. `compute-api/internal/console`
  holds the client (request signing and shapes) and `consoletest` a fake of the
  server. The console's server half is merged (unifyapi-console #225) but not
  released, and runs dark until `COMPUTE_BRIDGE_SECRET` is set. Where either
  side does not yet match this revision is listed under "Revision 2026-10-02".
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
- Missing `COMPUTE_BRIDGE_SECRET` on the console disables the routes entirely:
  every bridge path answers `404` with **no body**. This deliberately differs
  from the Builder bridge, which answers `401` when its secret is unset: a dark
  bridge should not advertise that it exists, and compute-api must be able to
  tell "bridge off" from "credential wrong" (`401`) without guessing.
- Routes sit under `/api/compute/v1/` and are excluded from the browser
  session auth; the HMAC is the only credential.
- A request body is at most **32 KiB (32768 bytes)**. A larger one is refused
  with `413 payload_too_large`, decided before the timestamp or the signature is
  examined, so a `413` says nothing about either. compute-api never sends a
  larger body: the client refuses locally (`ErrRequestTooLarge`) and nothing
  reaches the console.

The client implementation is `compute-api/internal/console/client.go`; its test
(`client_test.go`) pins the canonical string so both sides can be checked
against the same vector.

## Rate limiting

The bridge routes are **excluded from the console's global API limiter**. That
limiter is per IP (360 requests per 180 s by default, about 2 requests/s), and
every bridge call comes from compute-api's one egress IP, so under it the whole
compute product would share 2 requests/s and a burst of settles would starve
`verify`.

Instead the bridge has its own limiter: default **50 requests/s with a burst of
100**, configurable on the console with `COMPUTE_BRIDGE_RATE_LIMIT`. Over the
limit the console answers `429` with a `Retry-After` header (seconds) and
`message: "rate_limited"`, and compute-api waits at least that long before
retrying.

The limiter runs **before** HMAC verification: the cheap rejection comes
first, so unsigned or forged traffic is bounded too and cannot make the console
spend HMAC work without limit. A `429` therefore says nothing about whether
the request was correctly signed. The full order on the console is: bridge
enabled (else bodiless `404`), rate limit (`429`), body size (`413`),
timestamp and signature (`401`), then the handler.

The limiter is **per console process**, in memory, not shared between nodes.
With N console nodes the effective limit is N × the configured rate (and
N × the burst), and how much of that one caller sees depends on how the load
balancer spreads its connections. compute-api assumes only the configured
per-node rate, never the total: it does not size its sending rate on N, and it
honours every `429` and its `Retry-After` however many nodes are running,
because one node can refuse while another still has room.

## Errors and `404`

Every error body (except the disabled bridge's, which has none) is the envelope
with `success: false` and a stable machine-readable code from the table below
in `message`:

```json
{ "success": false, "message": "hold_not_found", "data": null }
```

Implementations may additionally include a `code` field with the same value;
clients must read `message` and must not depend on `code` being present.
Human-readable text, where a server wants one, goes in the console's logs, not
in `message`.

| `message` | Status | When |
|---|---|---|
| `invalid_request` | `400` | Body is not valid JSON, or a required field is missing or out of range (including a missing `sku` on `settle`) |
| `invalid_signature` | `401` | `X-Compute-Signature` missing or wrong |
| `invalid_timestamp` | `401` | `X-Compute-Timestamp` missing, unparsable, or more than 30 s from the console's clock |
| `invalid_key` | `401` | `verify`: key unknown, disabled or expired |
| `disabled` | `403` | `verify`: the key's user is disabled |
| `suspended` | `403` | `verify`: the billing tenant is suspended |
| `insufficient_balance` | `402` | `holds`, `extend`: the entity's (or a limited token's) remaining quota is below the amount |
| `hold_not_found` | `404` | The `hold_id` in the path is unknown |
| `hold_exceeded` | `409` | `settle`: `cumulative_quota` above the hold |
| `hold_expired` | `409` | `settle`, `extend`, `release`: the hold passed `expires_at` and was swept |
| `hold_closed` | `409` | `settle`, `extend`: the hold was released or final-settled |
| `sku_mismatch` | `409` | `settle`: `sku` differs from the hold's `sku` |
| `cumulative_quota_regressed` | `409` | `settle`: `cumulative_quota` is lower than the amount already settled |
| `idempotency_conflict` | `409` | A `hold_id`, `event_id` or `extend_id` reused with a different request or on a different hold (see `holds`, `settle` and `extend`) |
| `payload_too_large` | `413` | Request body over 32 KiB; checked before the timestamp and signature |
| `rate_limited` | `429` | Over the bridge limiter; `Retry-After` is set |
| `internal_error` | `500` | Unexpected server failure (a database error, a panic in a bridge handler). The detail goes only to the console log, never into the response |

Two `404`s exist and are distinguished by the body:

| Response | Meaning |
|---|---|
| `404`, empty body | Bridge disabled (`COMPUTE_BRIDGE_SECRET` unset). compute-api reports `bridge_disabled`. |
| `404`, JSON envelope, `message: "hold_not_found"` | The bridge is on and the `hold_id` in the path is unknown. |

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
else user). A `200` always carries `status: "active"`; a suspended or disabled
account is never a `200`.

Errors:

- `401`, `message: "invalid_key"`: the key is unknown, or the **key** (token)
  is disabled or expired.
- `403`: the key is valid but the account may not spend. A disabled **user**
  is `message: "disabled"` with `data: { "status": "disabled" }`; a suspended
  tenant is `message: "suspended"` with `data: { "status": "suspended" }`. A
  disabled user is a `403`, not a `401`: the credential is fine, the account
  is not, and compute-api tells the customer so.

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
`hold_id`:

- Same `hold_id` and the same request body: `200` with the original row;
  nothing is reserved again.
- Same `hold_id` with a different body: `409 idempotency_conflict`; nothing
  moves. The comparison is against the hold's original request (`user_id`,
  `token_id`, `quota`, `job_id`, `sku`, `expires_at`), not its current
  quota, so a retried create still matches after the hold was extended.

Refuse with `402` if
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
customer's usage view and in tenant reporting. Idempotent on `event_id`,
which is unique across **all** holds: a replay on the same hold returns the
original result, and the same `event_id` on a different hold is `409
idempotency_conflict` with nothing recorded.
`cumulative_quota` above the hold is refused with `409 hold_exceeded`; compute
must extend first. `cumulative_quota` below what is already settled is
`409 cumulative_quota_regressed` (the bridge has no refund verb), and a `sku`
that differs from the hold's is `409 sku_mismatch`; neither records anything.
`sku` is required: a settle without one is `400 invalid_request`, not a
`sku_mismatch` and not an implicit "use the hold's SKU", so a caller that lost
track of the SKU finds out instead of having it filled in. `final: true` settles and releases the remainder in one
call.

### `POST /api/compute/v1/holds/{hold_id}/extend`

Request:
```json
{ "extend_id": "ext_01J8…", "additional_quota": 300000 }
```

`extend_id` is required and client-generated. compute-api generates `ext_`
followed by a ULID; the prefix is a convention, and the console enforces only
that it is non-empty and at most 64 characters (`400 invalid_request`
otherwise). Same refusal rules as creating a hold (`402`). Response `data`
echoes the new `quota` and `remain_quota`.

Idempotent on `extend_id`, which, like `event_id`, is unique across all
holds. An extend that commits but whose response is lost would otherwise be
retried and reserve `additional_quota` a second time: the surplus comes back only at final settle, release or expiry, so for
the life of the job the customer's balance reads low and a later hold or
extend can fail with a false `402`.

- Same `extend_id` and same `additional_quota`: `200` with the hold's current
  state (`quota`, `remain_quota`); nothing is reserved again.
- Same `extend_id` with a different `additional_quota`, or on a different
  hold: `409 idempotency_conflict`; nothing moves.

### `POST /api/compute/v1/holds/{hold_id}/release`

Return the unsettled remainder to the billing entity and the token. Idempotent.
Response `data`: `{ "released_quota": 840000, "remain_quota": 12070000 }`.

## Console change list (minimal, all new files unless noted)

Following the fork's rule "prefer adding a file over editing one". Files:

```
model/compute_hold.go            compute_holds table (hold_id PK, user_id, tenant_id, token_id,
                                 job_id, sku, quota, settled_quota, status, expires_at, timestamps)
                                 + compute_settlements table (event_id PK, hold_id, quota_delta, gpu_seconds)
                                 + compute_extensions table (extend_id PK, hold_id, additional_quota)
model/compute_hold_test.go       hold/settle/release/expire, idempotency, tenant vs untenanted user
controller/compute_bridge.go     HMAC verification, the five handlers, envelope responses
controller/compute_bridge_test.go signature vector shared with compute-api, replay window, disabled-when-unset
router/api-router.go             one UNIFYAPI-BRAND block: apiRouter.Group("/compute/v1") wiring   (upstream file, ~8 lines)
model/main.go                    one UNIFYAPI-BRAND line: AutoMigrate the two tables               (upstream file, already touched by the fork)
docs/compute-bridge.md           copy of this contract from the console's point of view
```

Environment: `COMPUTE_BRIDGE_SECRET`, `COMPUTE_BRIDGE_RATE_LIMIT` (optional). No new dependency (`crypto/hmac` is
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

## Revision 2026-10-01

Approved by the operator after the first implementations (compute-api PR #9,
the fake console; PR #10, the job store; unifyapi-console PR #225, the bridge
server) found gaps:

- `extend` takes a required `extend_id` (non-empty, ≤ 64 chars; `ext_` + ULID
  by convention), unique across all holds; a different amount or a different
  hold under the same id is `409 idempotency_conflict`.
- A settle `event_id` reused on a different hold is `409 idempotency_conflict`.
- A `hold_id` replay with the same body returns the original row (`200`); with
  a different body it is `409 idempotency_conflict` (no separate
  `hold_id_conflict` code).
- `settle` refuses a different `sku` (`409 sku_mismatch`) and a lower
  cumulative amount (`409 cumulative_quota_regressed`).
- `verify` refuses a suspended tenant or disabled user with `403` and
  `data.status` = `"suspended"` / `"disabled"`; it never returns those as `200`,
  and a disabled user is no longer a `401`.
- Every error body carries a machine code in `message` (an extra `code` field
  with the same value is optional; clients read `message`); the codes are
  listed under "Errors and `404`".
- The two `404`s are distinguished: empty body = bridge disabled, JSON
  `hold_not_found` = unknown hold.
- Bridge routes leave the console's global API limiter for a dedicated one
  (50 rps, burst 100, `COMPUTE_BRIDGE_RATE_LIMIT`), checked before the
  signature, answering `429 rate_limited` with `Retry-After`.
- File references corrected to `client.go` / `client_test.go`.
- The Builder bridge answers `401`, not `404`, when its secret is unset; the
  compute bridge's `404` is kept on purpose.

## Revision 2026-10-02

Approved by the operator as part of wave 2, closing four leftovers from the
first implementations:

- `settle`'s `sku` is required: missing → `400 invalid_request`; present but
  different from the hold's → `409 sku_mismatch`, as before.
- `internal_error` (`500`) is in the error table: an unexpected server failure,
  with the detail only in the console log.
- Request bodies over 32 KiB → `413 payload_too_large`, checked before the
  signature verification's body handling. Until now the console refused them
  as `401 invalid_signature`, which sent the caller looking for a wrong secret.
- The dedicated rate limiter is per console process: with N nodes the
  effective limit is N × the configured rate. compute-api assumes only the
  per-node rate and must still honour every `429` and `Retry-After`.

Where the implementations stand against this revision:

| Item | compute-api client | `consoletest` fake | Console (unifyapi-console) |
|---|---|---|---|
| `sku` required on settle | the ledger, now the only settle caller, always sends the hold's `sku` | a missing or empty `sku` is `400 invalid_request`, checked after the hold lookup | **not yet on `main`**: a missing `sku` is still accepted and the hold's SKU is used; the fix is in review (FelixSphere/unifyapi-console, branch `fix/compute-settle-sku-required`) |
| `internal_error` | `CodeInternalError` | a forced `500` answers `internal_error` | already on every unexpected-error path; a handler panic now answers it too instead of the server-wide panic body |
| `413 payload_too_large` | `CodePayloadTooLarge`; refuses locally above 32 KiB, sends nothing | implemented, before the signature | implemented, before the timestamp and signature |
| Per-process limiter | surfaces a `429` as `*Error` and does not expose `Retry-After`. The ledger treats a `429` as unavailable and retries on its next tick (300 s), which honours any `Retry-After` shorter than that without reading it. `verify` is not retried: a `429` there answers the customer `503 console_unavailable` | single process | documented in `docs/compute-bridge.md` |
