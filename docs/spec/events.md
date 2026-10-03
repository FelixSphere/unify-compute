# Job events and webhooks

- **Status:** Spec, partially implemented. What exists in `compute-api/`:
  the event log and the delivery queue (`internal/store`, migration `0003`),
  the event types and the webhook delivery worker (`internal/events`), and
  `GET /compute/v1/jobs/{id}/events` (answers `501` only in the dark build,
  without a job store), the emitters (`internal/jobs`: every `job.*` type
  except `job.failover`, and `billing.balance_exhausted`), and the delivery
  worker running in the binary every 5 s. **Not built:** webhook
  registration (`/compute/v1/webhooks`), `job.failover` (the backend does
  not observe a failover between candidates yet) and
  `billing.hold_extended` (the ledger does not report a successful extend).
  Sections below say which is which.
- Product rationale: `docs/product-design.md` — CoreWeave's "health as visible
  events" and Vast.ai's signed webhooks (task C6).

## Why

A job's status says where it is, not how it got there. A customer whose job
spent 40 minutes in `provisioning` cannot tell "waiting for capacity" from
"failed over from AWS to GCP twice". Every signal SkyPilot gives us that the
customer would want to see — placement, failover, preemption, provisioning
failure — becomes an event on the job, readable by polling and, for the
states a pipeline waits on, pushed by a signed webhook.

## Event object

```json
{
  "id": "evt_01J8Q6ZK3R7Y9W2X4V6T8S0A1B",
  "object": "compute.event",
  "job_id": "job_01J8Q6ZK3R7Y9W2X4V6T8S0A1A",
  "type": "job.failed",
  "created_at": 1757520042,
  "data": { "error": { "code": "image_pull_failed", "message": "manifest unknown" }, "retryable": false }
}
```

- `id` is `evt_` followed by a ULID, the same encoding as job ids. It is the
  deduplication key: a webhook delivered twice carries the same id.
- `created_at` is Unix seconds, like every timestamp in the job API. Several
  events can share a second; order is the log's append order, which the list
  endpoint preserves and the id does not guarantee within one millisecond.
- `data` is always a JSON object, `{}` when the type carries nothing.
- Events are append-only. The database refuses an `UPDATE` on the events
  table; an event is never rewritten, only followed by another one.
- Every event belongs to one job. Account-level events (artifact storage
  arrears under ADR-0008) would need a nullable `job_id`; not in this version.

## Event types

| Type | Emitted when | `data` | Webhook by default |
|---|---|---|---|
| `job.queued` | Job persisted | `{ "sku": "a10g-24gb-x1" }` | no |
| `job.provisioning` | Handed to SkyPilot | `{}` | no |
| `job.placed` | SkyPilot reports where it landed | `{ "placement": { "cloud", "region", "instance_type" } }` | no |
| `job.running` | Cluster `UP`, process started; GPU metering starts here | `{}` | no |
| `job.failover` | Provisioning on one provider failed and SkyPilot moved to the next in the SKU's order | `{ "from": "aws", "to": "gcp", "reason": "capacity_exhausted" }` | no |
| `job.preempted` | Job entered `interrupted` (spot preemption, or the provider took the node) | `{ "provider": "aws" }` | **yes** |
| `job.succeeded` | Process exited 0 | `{}` | **yes** |
| `job.failed` | Job entered `failed` | `{ "error": { "code", "message" }, "retryable": true }` | **yes** |
| `job.cancelled` | Job entered `cancelled` | `{ "reason": "customer" \| "balance_exhausted" \| "billing_unavailable" \| "queue_expired" }` | **yes** |
| `billing.hold_extended` | `extend` succeeded on the console | `{ "hold_usd": "2.5400", "expires_at": 1757527200 }` | no |
| `billing.balance_exhausted` | An extension was refused and the job is about to be cancelled | `{}` | no |

Required fields, enforced by `events.Validate` before anything is written:
`job.queued.sku`, `job.placed.placement`, `job.failover.from` and `.to`,
`job.preempted.provider`, `job.failed.error.code`, `job.cancelled.reason`,
`billing.hold_extended.hold_usd`. Other fields are optional.

`retryable` on `job.failed` is the Vast.ai "won't recover" signal from
`product-design.md`: `false` for `image_pull_failed` and a non-zero exit,
`true` for `capacity_exhausted`. The table of codes belongs to the backend
(C4); the event only carries its answer.

The four terminal types correspond one-to-one with the four terminal job
statuses (`interrupted` → `job.preempted`). Exactly one terminal event is
emitted per job.

*Emitters are implemented* in `internal/jobs`. `job.queued` is written in
the same transaction as the job row; every later `job.*` event in the same
transaction as the status change or placement it announces
(`store.UpdateStatusWithEvent`, `store.SetPlacementWithEvent`), so a crash
between the two cannot lose the terminal webhook and a refused transition
writes no event. That is also what keeps a restart from emitting a terminal
event twice: the process that takes a job over writes the same transition,
which the store refuses once it has happened. `job.placed` precedes `job.running`. `job.preempted` covers
every `interrupted` job, preemption or provider fault; its `provider` is
`"unknown"` when the job never reached a placement. `job.cancelled.reason`
is `customer` unless compute-api stopped the job (`balance_exhausted`,
`billing_unavailable`), or did not start it in time after a restart
(`queue_expired`, backend.md "Restart"). `billing.balance_exhausted` is written on the first
refused extension (metering.md H4), about 50 minutes before the job would be
stopped.

## `GET /compute/v1/jobs/{id}/events?after=evt_…&limit=50`

Implemented. Owner-scoped exactly like `GET /jobs/{id}`: a job that does not
exist and a job of another owner are the same `404 not_found`.

Oldest first, so a client tailing a job passes the last id it saw as `after`
and receives only what is new. `limit` defaults to 50, maximum 100.

```json
{ "object": "list", "data": [ … ], "has_more": false, "next": "evt_…" }
```

`next` is the id of the last event returned, present whenever `data` is
non-empty, so a poller can always resume from it (unlike the job list, where
`next` appears only with `has_more`: a job log grows at its tail, the job list
does not).

`after` that does not name an event of this job is `400 invalid_request`,
code `invalid_cursor`. A malformed `limit` is `400`, code `invalid_limit`.

## Webhooks

### Registration — spec only

```
POST   /compute/v1/webhooks           { "url": "https://…", "event_types": ["job.succeeded", …] }
GET    /compute/v1/webhooks
DELETE /compute/v1/webhooks/{id}
```

- A webhook belongs to the calling key: it receives events for jobs that key
  submitted (`jobs.token_id`), and revoking the key stops it. The store also
  allows an owner-wide webhook (no key) for operator-seeded endpoints; the API
  never creates one.
- `event_types` defaults to the four terminal types. Any type from the table
  above may be listed; an unknown one is `400`.
- `POST` returns `201` with the webhook object and its `secret`
  (`whsec_` + 64 hex characters). The secret is shown once; `GET` never
  returns it. Rotating is delete-and-recreate.
- At most 5 webhooks per key.
- `url` must be `https`, must not carry credentials, and its host must not
  resolve to a loopback, private, link-local (including the cloud metadata
  address `169.254.169.254`) or otherwise non-public address. The check runs
  **at dial time on every delivery**, not only at registration, because DNS
  can change after the URL is accepted.

Why not built now: the dial-time address guard is the part that has to be
right before any customer-supplied URL is called from inside our VPC, and it
deserves its own review. Until it exists, webhooks exist only as rows a test
or an operator writes (`store.CreateWebhook`), pointing at trusted URLs.

```json
{ "id": "wh_01J8Q7…", "object": "compute.webhook", "url": "https://hooks.acme.dev/compute", "event_types": ["job.succeeded", "job.failed", "job.cancelled", "job.preempted"], "created_at": 1757520000 }
```

### Delivery — implemented, running in the binary

When an event is appended, the same statement creates one delivery for every
webhook of the job's owner that matches the job's key (or has no key) and
lists the event's type. A webhook registered later does not receive earlier
events; they remain readable from the events endpoint. Deleting a webhook
drops its pending deliveries.

Each attempt is `POST <url>` with the event object as the body, exactly as
the events endpoint returns it, and these headers:

| Header | Value |
|---|---|
| `Content-Type` | `application/json` |
| `X-Compute-Event-Id` | the event id — **deduplicate on this** |
| `X-Compute-Event-Type` | the event type |
| `X-Compute-Webhook-Id` | the webhook id |
| `X-Compute-Delivery-Attempt` | `1`, `2`, … |
| `X-Compute-Timestamp` | Unix seconds when this attempt was signed |
| `X-Compute-Signature` | hex HMAC-SHA256, below |

**Signature.** The console bridge's canonical string, unchanged
(`console-billing-bridge.md`, `console.Canonical`):

```
METHOD + "\n" + PATH + "\n" + TIMESTAMP + "\n" + EXACT_BODY
```

with `METHOD` = `POST`, `PATH` = the escaped path of the registered URL
(`/` if it has none; the query string is not signed), and the key = the bytes
of the webhook's secret string, `whsec_` prefix included. One scheme across
the product means one verification snippet in the docs and one test vector.

A receiver should recompute the signature over the raw body before parsing
it, compare in constant time, and reject a timestamp more than 5 minutes from
its own clock. The body is identical on every attempt; the timestamp and
signature are fresh on each, so a captured delivery cannot be replayed past
the receiver's window.

**At least once.** A delivery succeeds on any `2xx` within 10 seconds.
Anything else — another status, a redirect (never followed), a timeout, a
connection error — is a failed attempt and is retried. A receiver that
processed the event but answered `500` will see it again with the same
`X-Compute-Event-Id`; that is the contract, not a bug.

**Backoff and give-up.** Attempt 1 is as soon as the worker sees the
delivery. After failed attempt *n* the next is `min(1 min × 2^(n−1), 6 h)`
later: 1, 2, 4, 8, 16, 32, 64, 128, 256 minutes, then 6 h, 6 h. After the
**12th** failed attempt — about 20.5 hours after the event — the delivery is
marked `failed` and never retried. No jitter yet (open question below).

**Concurrency.** The worker claims due deliveries with
`FOR UPDATE SKIP LOCKED` and a lease (attempt timeout + 50 s), so several
replicas can run it; a replica that dies mid-attempt leaves the delivery to
be retried when the lease expires, which is one more source of duplicates
the event id absorbs.

Ordering across events is **not** guaranteed: a retried `job.failover` can
arrive after `job.failed`. Receivers that care order by the events endpoint,
not by arrival.

## Storage

`compute-api/internal/store/migrations/0003_events.sql`:

- `job_events` — `id`, `seq` (identity, the order key), `job_id`, `type`,
  `data`, `created_at`; `UPDATE` refused by trigger. `job_id` has no foreign
  key: `AppendEvent` checks the job in the same statement, and the existing
  `TestMigrateConcurrent` drops and recreates `jobs`, which a referencing
  table would block. The migration is re-runnable for the same reason.
- `webhooks` — `id`, `owner`, `token_id` (null = owner-wide), `url`,
  `secret`, `event_types`, `created_at`.
- `webhook_deliveries` — one row per (event, webhook): `status`
  (`pending` / `delivered` / `failed`), `attempts`, `next_attempt_at`,
  `last_status`, `last_error`, `delivered_at`; unique on
  (`event_id`, `webhook_id`) so fan-out cannot double-queue.

## Open questions

- **Secret at rest.** The secret is stored as written, because signing needs
  it. Envelope-encrypting it with a KMS key is the obvious next step and
  needs the staging stack to have one.
- **Jitter.** A receiver that was down for an hour gets every queued delivery
  in the same minute when it returns. Full jitter on the backoff fixes it at
  the cost of deterministic tests; decide before customers register URLs.
- **Delivery visibility.** A customer cannot yet see failed deliveries or
  replay one. `GET /webhooks/{id}/deliveries` and a redeliver action are the
  usual shape.
- **`job.preempted` for provider faults.** `interrupted` covers spot
  preemption and provider faults alike; one event type for both, with a
  `reason`, or `job.interrupted`? Kept as specified in C6 for now.
- **Retention.** Events live as long as their job; the job API keeps logs 7
  days after terminal. No purge exists for either yet.
- **Per-key vs per-owner.** C6 asks for per-key webhooks and that is what the
  API shape says. If scoped keys (product-design Vast #6) land first, a
  webhook might instead carry its own scope.
