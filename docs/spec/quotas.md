# Quotas and per-key rate limits

- **Status:** Spec, partially implemented. What exists and what does not is
  listed under [What is built](#what-is-built); everything else here is the
  contract the rest must meet.
- Task C8 in [`product-design.md`](../product-design.md). Code:
  `compute-api/internal/quota/`, `compute-api/internal/ratelimit/`,
  `compute-api/config/quotas.json`.

## The rule

**Requested vs granted is explicit.** A submit that would take an owner over
any limit is refused, whole, before a console hold is placed or a job is
persisted. It is never granted in part: a job asking for 4 GPUs against a
headroom of 2 does not run on 2. The refusal lists every limit the request
would exceed, the usage, and the headroom, so a client can act on it without
parsing prose.

This is the behaviour CoreWeave gets wrong (over-quota requests are silently
truncated), and the reason it is a product principle and not a detail.

## Who a quota belongs to

The **owner**: the billing entity a key resolves to through the console
bridge's `verify` — `tenant:<tenant_id>` when the key belongs to a tenant,
`user:<user_id>` otherwise (`console.Identity.Owner`). That is the same entity
whose balance the hold is placed against, so every key of one account shares
one quota; issuing more keys does not buy more concurrency. Jobs are already
stored under this owner (`jobs.owner`).

Rate limits are different: they are **per key**, as `job-api.md` has always
said, because they protect the service from one runaway client, not the
account from over-commitment.

## Limits

| Limit | Counts | Default | Why this default |
|---|---|---|---|
| `max_active_jobs` | Jobs in `queued`, `provisioning` or `running` | **2** | Enough to run one job and iterate on the next. Our own upstream accounts' GPU quotas are finite, and small on new cloud accounts, so one new customer must not be able to consume a provider's whole allowance for every other customer. |
| `max_queued_jobs` | Jobs in `queued` | **2** | Equal to `max_active_jobs`, so it does not bind at the default. It exists for raised accounts: a queued job already holds up to two hours of balance (`metering.md` H1) while waiting, so an account raised to 16 active jobs should not also be able to park 16 holds in a queue. |
| `max_gpus.hyperscaler` | Sum of `gpu_count` over active jobs on hyperscaler SKUs | **2** | Two single-GPU jobs, or one 2-GPU job. An 8-GPU SKU is refused for a new account with headroom 2 — explicitly, which is the point — until someone raises it. |
| `max_gpus.neocloud` | Same, neocloud SKUs | **2** | Same reasoning; tiers are separate budgets because they are separate upstream accounts with separate capacity. |
| `max_gpus.marketplace` | Same, marketplace SKUs | **0** | Closed until an account is reviewed. Marketplace hosts are third parties with weaker isolation (`upstreams.md` R15), and an unknown account running abusive workloads there costs us our standing with the marketplace, not just money. No marketplace SKU is available today, so this refuses nothing yet; when one is, a new account sees `max_gpus` marketplace `max: 0` and knows it is an account limit, not missing capacity. |

Queued jobs count against `max_active_jobs` and `max_gpus` from the moment
they are accepted. Counting only running jobs would let an account queue any
number of 8-GPU jobs that then all start together; a queued job has also
already taken its hold, so it is committed spend.

The defaults are deliberately conservative. Raising one for an account is a
one-line override and a review (below); lowering one after customers have
built on it is a broken promise. Start small.

### Where limits live

`compute-api/config/quotas.json`, shipped inside the image like `skus.json`.
A limit change is a code change: reviewed, merged, released through staging
like any other (AGENTS.md). There is no admin API in this task.

```json
{
  "defaults": {
    "max_active_jobs": 2,
    "max_queued_jobs": 2,
    "max_gpus": { "hyperscaler": 2, "neocloud": 2, "marketplace": 0 }
  },
  "owners": {
    "tenant:7": { "max_active_jobs": 8, "max_gpus": { "hyperscaler": 8 }, "reason": "design partner; reviewed 2026-10-01" }
  }
}
```

An override replaces only the fields it names; a tier missing from its
`max_gpus` keeps the default. `reason` is required, so the file explains
itself in review. The file is validated at boot and a bad one stops the
process: unknown fields, unknown tiers, a default without all three tiers,
negative values, `max_queued_jobs` above `max_active_jobs` (after applying an
override), an owner that is not `user:<id>` or `tenant:<id>`, or an override
without a reason.

## Enforcement at submit

Quota is the **last** check before the hold, after everything in
`job-api.md`'s validation list. A request that is wrong in itself (unknown
SKU, runtime over the ceiling, no enabled upstream) is told so, not told the
account is full.

```
validate body / sku / runtime / providers / upstream     (400, 503)
→ quota: owner's usage + this job ≤ every limit           (403 quota_exceeded)
→ hold on the console                                     (402)          planned
→ persist job queued                                                      planned
```

### Refusal

`403`, `type` and `code` `quota_exceeded`, `param: "sku"`, and `quotas`: one
entry per limit exceeded.

```json
{
  "error": {
    "type": "quota_exceeded",
    "code": "quota_exceeded",
    "param": "sku",
    "message": "sku \"a10g-24gb-x4\" would exceed this account's quota (hyperscaler GPUs: 1 of 2 in use, this job needs 4, headroom 1). Nothing was submitted or held; wait for or cancel an active job, choose a smaller SKU, or ask support to raise the limit.",
    "quotas": [
      { "limit": "max_gpus", "tier": "hyperscaler", "max": 2, "used": 1, "requested": 4, "headroom": 1 }
    ]
  }
}
```

| Field | Meaning |
|---|---|
| `limit` | `max_active_jobs`, `max_queued_jobs` or `max_gpus` |
| `tier` | For `max_gpus` only |
| `max` | The owner's limit after overrides |
| `used` | Current usage the decision saw |
| `requested` | What this submit adds (1 job, or the SKU's `gpu_count`) |
| `headroom` | `max − used`, never below 0 (a lowered limit can leave an owner above it) |

There is no `Retry-After`.

### Why 403 and not 429

Both appear in `product-design.md`; this spec decides **403**.

- `429` promises that the same request succeeds if you wait, and its
  companion `Retry-After` says how long. For a quota neither holds: a 4-GPU
  job against a 2-GPU limit never fits, and when a slot frees depends on
  another job's runtime, which nobody can put in a header.
- The OpenAI SDKs our customers already point at UnifyAPI retry `429`
  automatically with backoff. A quota refusal returned as `429` would be
  retried silently a few times and then surface late, looking like
  congestion — the opposite of explicit.
- The refusal is a policy decision about the account, which is what `403`
  says, and it matches the console bridge's own use of `403` for an account
  that may not spend.
- One code, one meaning: in this API `429` is always a per-key rate limit,
  always has `Retry-After`, and always clears by waiting.

### Concurrent submits

Two submits for the same owner must not both take the last slot. Usage is
counted from the job store, so a naive read-then-insert lets both read the
same usage and both pass.

`store.CreateAdmitted` closes that: in one transaction it takes a
transaction-scoped Postgres advisory lock keyed on the owner
(`pg_advisory_xact_lock(class, hashtext(owner))`), reads the owner's active
usage, calls the admission decision, and inserts the job. A second submit
for that owner blocks on the lock until the first commits or rolls back, then
reads usage that includes the first job. Other owners are not blocked (a hash
collision only makes two owners wait for each other, never share a quota).

The decision callback is also where the console **hold** is placed
(`quota.Enforcer.Admit(owner, sku, hold)`): the quota decision runs first, and
only a submit within quota calls the console. So an over-quota submit never
creates a hold, even under concurrency, and there is no hold to release after
a lost race. The cost is that one owner's submits are serialised across the
hold call, bounded by the console client's timeout, and a pooled connection
is held for that time. At 60 submits per key per hour that is not a
throughput limit.

An `Idempotency-Key` already bound to a live job is checked under the same
lock **before** the quota decision and replays the original job. A client
retrying a submit that succeeded gets its job back, not a `quota_exceeded`
caused by that very job.

Active usage is read with a status predicate that matches the partial index
`jobs_active_idx`, and is reported per SKU; the quota package maps SKUs to
tiers and GPU counts through the catalogue. No migration is needed. An active
job on a SKU the catalogue no longer lists fails the check closed (`500
quota_check_failed`) rather than guessing its GPUs: retire SKUs with
`available: false`, never by deleting them while jobs may still run on them.

## Per-key rate limits

From `job-api.md`: **60 submits per hour** and **600 reads per minute** per
key, as token buckets (burst 60 then one per minute; burst 600 then ten per
second). Over budget is `429`, `type`/`code` `rate_limited`, with
`Retry-After` in whole seconds, at least 1.

- Applied after the key's shape is checked, so an anonymous request is `401`
  and spends nobody's budget. Every authenticated submit counts, including
  ones refused later for validation or quota.
- Reads are `GET /jobs`, `GET /jobs/{id}`, `/logs`, `/artifacts`, `/events`.
  Cancel is in neither budget (open question below). Public routes (`/skus`,
  `/upstreams`, `/templates`, `/openapi.json`, `/healthz`) are not limited
  here.
- Buckets are in process memory, keyed by a SHA-256 of the key. With several
  replicas each enforces its own budget, so the effective limit is the
  per-process limit times the replica count. Staging runs one.
- Memory is bounded at 100,000 keys: refilled buckets are swept, and a flood
  of distinct keys beyond the cap resets every budget. That matters only
  while auth checks the key's shape and not its existence; once `verify` is
  wired, keys cannot be minted.

## What is built

| Piece | State |
|---|---|
| `config/quotas.json`, parsed and validated at boot | Built; a bad file stops the process |
| Decision (`quota.Evaluate`), every violation with headroom | Built, tested |
| Race-free admission (`store.CreateAdmitted` + `quota.Enforcer.Admit`) | Built, tested against Postgres, including concurrent submits for one slot |
| Submit handler runs the check before its `501` | Built, **but not enabled**: `Server.Quota` is nil in the binary, because enforcement needs the owner and the binary has neither `verify` wired into auth nor a database opened. Until both land, no submit is quota-checked in a deployment. |
| Handler uses `CreateAdmitted` with the hold inside the admission | Planned, with the backend (C4) and the console hold. Today the handler runs `Enforcer.Check`, a lock-free preflight, since nothing is persisted |
| Per-key rate limits | Built and enabled on every authenticated route except cancel |
| `GET /compute/v1/quotas` (limits + usage for the caller) | Not built; see open questions |

## Open questions

- **Usage endpoint.** A customer should be able to read their limits and
  usage before submitting, not only from a refusal. `GET /compute/v1/quotas`
  is the obvious shape; it is not in this task.
- **Cancel rate.** Cancel is in neither budget. It is cheap for us and
  customers cancelling in bulk after a bad submit should not be throttled, but
  it does call SkyPilot; decide when the backend lands.
- **Self-serve raises.** Overrides are a reviewed file today. Whether limits
  should rise automatically with account age or spend, and who approves a raise,
  is a product decision not made here.
- **Shared rate limits.** If compute-api runs more than one replica, the
  per-process buckets should move to Redis (already in the compose profile).
