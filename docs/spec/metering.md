# Metering — three meters, what is billed when, and how it reaches the console

- **Status:** Spec, not implemented. Nothing here is built. `compute-api` has
  the SKU catalogue and `sku.EstimateQuota`; the ledger, settlement loop,
  storage sampler and egress collector are task C5. Every row in the tables
  below carries an ID (`B…`, `H…`, `R…`) so C5 can name a test after it.
- Depends on: [`job-api.md`](job-api.md) (job states, the `usage` object),
  [`console-billing-bridge.md`](console-billing-bridge.md) (the verbs),
  ADR-0001 (the console owns the balance), ADR-0005 (artifacts in our S3;
  **Proposed**, not yet on `main` — it lives in the finetune worktree), and
  [ADR-0008](../adr/0008-zero-balance-policy.md) (what happens at zero balance;
  **Proposed**).
- All prices below are **placeholders**. They exist so the arithmetic can be
  tested, not as a commitment. None of them goes on the site.

## The three meters

| | GPU time | Artifact storage | Egress |
|---|---|---|---|
| **Unit** | second | GB-hour (1 GB = 2^30 bytes) | GB (1 GB = 2^30 bytes) |
| **What is measured** | Seconds a job's cluster is `UP` and the job is still the customer's (see end events below), summed over attempts | Bytes stored under the job's artifact prefix `jobs/<job_id>/`, integrated over time | Bytes sent by S3 in response to GET requests on objects under `jobs/<job_id>/` |
| **Source** | SkyPilot cluster and job lifecycle, observed by compute-api's backend poller (C4): cluster `UP`, job process exit, cluster down, preemption | `ListObjectsV2` over the prefix, summing `Size`, once an hour per retained job, plus one sample at terminal state and one at deletion | S3 server access logs on the artifacts bucket (`Bytes Sent` field, keyed by object key → job id) |
| **Rounding** | Integer seconds per attempt, fractional second truncated. Cost computed exactly on cumulative seconds and rounded half-up once, to the quota unit | Byte-seconds accumulated as an integer; cost rounded half-up once per daily charge | Bytes summed as an integer; cost rounded half-up once per daily charge |
| **Price source** | `compute-api/config/skus.json`, `price_usd_per_hour` (exists) | `compute-api/config/meters.json`, `storage_usd_per_gb_month` (**proposed**, see below) | `compute-api/config/meters.json`, `egress_usd_per_gb` (**proposed**) |
| **Placeholder price** | whatever `skus.json` says (A10G `1.2000`/h today, itself a placeholder) | `0.1000` per GB-month | `0.1000` per GB |
| **Rate applied** | Snapshotted on the job at submit; a price change never alters a running or finished job | Rate in effect at the hour of accrual | Rate in effect at the time of the request in the log record |
| **Reaches the console** | Per-job hold, settled every 5 min and at terminal state | Daily per-account charge (hold + final settle) | Same daily per-account charge, separate line |
| **Consume-log `model_name`** | `compute/<sku>` (as the bridge spec says today) | `compute/storage` | `compute/egress` |

What the meters do **not** cover, so nobody goes looking for it:

- Network traffic from the job's own instance (pulling the image, reading
  inputs, calling out to the internet). The upstream charges us for that; it is
  folded into the SKU's hourly rate. If a workload makes that unprofitable it
  is a SKU price change, not a fourth meter.
- Reading inputs from the customer's own bucket. That traffic is on their
  account (ADR-0005: inputs stay where the customer put them).
- Logs. Retained 7 days after terminal state (`job-api.md`) and not billed.
- Storage of outputs **while the job is non-terminal**. Whatever the workload
  writes during the run is covered by the GPU rate; the storage meter starts at
  terminal state, when the artifact set is final.

### GPU seconds in detail

An **attempt** is one SkyPilot cluster lifetime. A job has one attempt unless
it is interrupted and retried (whether retry is default-on is still open in
`architecture.md`; the rules below hold either way).

For each attempt:

- **Start** = the time compute-api observes SkyPilot report the cluster `UP`.
  This is `started_at` on the first attempt and the moment the job enters
  `running`. Image pull and the job's own setup happen after `UP`, on a GPU
  that is the customer's, and are billed.
- **End** = the earliest of:
  1. the job's process exiting (any exit code);
  2. compute-api accepting a cancel (the `202` on
     `POST /jobs/{id}/cancel`, or compute-api's own cancel for
     `balance_exhausted` / `billing_unavailable`);
  3. the cumulative billed seconds across all attempts reaching
     `max_runtime_seconds`;
  4. preemption or provider fault: the time the provider reports, or, if it
     reports none, the **last observation of the cluster `UP`** — never the
     time compute-api noticed it was gone;
  5. SkyPilot reporting the cluster down for any other reason.
- **Billed seconds** = `floor(end − start)`, both taken at millisecond
  resolution.

Teardown after the end event (uploading outputs, `sky down`, the instance
draining) is our cost, never the customer's.

`max_runtime_seconds` caps **billed seconds**, not wall-clock time. Retries
cannot make a job cost more than `max_runtime_seconds × rate`; that is what
makes the GPU estimate a true ceiling.

**Arithmetic.** For a job with cumulative billed seconds `s` at SKU hourly
price `p` (exact decimal from `skus.json`):

```
quota(s) = round_half_up(p × s / 3600 × 500000)
```

This is `sku.EstimateQuota` today. The per-second rate is never rounded on its
own; there is one rounding, at the end. Each settlement sends `quota(s)` for the
**cumulative** `s`, so the console's deltas may differ from one another by one
quota unit but their sum is always exactly `quota(total)`. (`job-api.md`'s SKU
section says the per-second rate is "rounded half-up to 6 decimals at
settlement"; read that as describing display, not the computation — the code
and this spec round once.)

### Storage GB-hours in detail

- The meter for a job starts at the moment the job reaches a terminal state
  (`finished_at`), with a sample taken then.
- It is sampled hourly. Between samples the stored size is taken as the
  earlier sample's (a step function), so a job's byte-seconds are
  `Σ bytes_at_sample_i × (t_{i+1} − t_i)`.
- It stops at the **earliest** of: retention expiry (30 days after terminal
  state, ADR-0005), deletion for arrears (ADR-0008), or any other deletion we
  perform. It stops at the scheduled time even though S3's lifecycle rule
  removes objects up to a day later; that lag is ours to pay.
- A job with no artifacts accrues nothing and makes no bridge calls.
- Artifacts of `failed`, `interrupted` and `cancelled` jobs are retained and
  billed exactly like those of `succeeded` jobs. A customer who wants them gone
  sooner needs a delete endpoint, which v1 does not have (open point below).
- A later job that references an earlier job's artifacts (ADR-0005) reads the
  same prefix; the bytes are billed once, to the job that wrote them.

Daily cost for one account = `round_half_up(Σ byte_seconds × r_month / 730 /
(2^30 × 3600) × 500000)` over all its jobs for that UTC day, where `r_month` is
`storage_usd_per_gb_month`. 730 hours per month is fixed, so a GB-hour has the
same price in February as in March.

### Egress GB in detail

- Source of truth: S3 server access log records for `GET` on keys under
  `jobs/`. `Bytes Sent` of `-` counts as zero. Each log record is charged once,
  keyed by its request id, so a late-delivered log simply lands in the next
  daily charge.
- Every successful download is billed regardless of the job's state, including
  repeated use of the same signed URL within its one-hour validity.
- **Pre-check at URL issuance** (not a charge): `GET /jobs/{id}/artifacts`
  verifies that the account's `remain_quota` covers the listed objects' total
  size at the egress rate, and that the account is not in arrears; otherwise it
  returns `402`. This bounds unbilled egress to what one hour of signed URLs can
  pull. It is a check against `verify`, not a hold, because egress is collected
  daily, not per job.
- Server access logs are not enabled by ADR-0005 or by anything in
  `deploy/aws/staging/` today. Turning them on, and the log bucket, belongs to
  task I1; this spec depends on it.

Daily cost for one account = `round_half_up(Σ bytes × r_gb / 2^30 × 500000)`
over the log records charged that day.

### Where storage and egress prices live (proposal)

A new file, `compute-api/config/meters.json`, next to `skus.json`, with the
same rules: decimal strings, a `note` that marks the numbers as placeholders,
shipped with the binary, and a change to it is a release with money in it.

```json
{
  "note": "Placeholder prices for developing the billing flow. Not customer-facing until reviewed and this note is removed.",
  "storage_usd_per_gb_month": "0.1000",
  "egress_usd_per_gb": "0.1000"
}
```

Kept separate from `skus.json` because these prices are not per SKU: a job's
artifacts cost the same whichever GPU produced them. `GET /compute/v1/skus`
stays as it is; a public `GET /compute/v1/meters` for the pricing page is a
later, separate change. Like an unpriced SKU, a missing or unparsable
`meters.json` stops compute-api from starting rather than metering at zero.

For orientation only: S3 Standard in us-east-1 is about USD 0.023 per GB-month
and internet egress about USD 0.09 per GB, so the placeholders leave room above
upstream cost. Real prices are an operator decision after the reference run
(ADR-0005 asks for artefact sizes to be measured first).

## Billable-state table

"Hold" is quota locked on the console; it is never a charge. "—" means the
meter does not run.

| ID | Job status | GPU seconds | Storage GB-hours | Egress GB | Hold |
|---|---|---|---|---|---|
| B1 | `queued` | — | — | — (no artifacts) | Placed at submit, locked, not charged |
| B2 | `provisioning` (first attempt) | — never, however long it takes | — | — | Locked |
| B3 | `running` | Billed from cluster `UP` to the attempt's end event | — (covered by GPU rate) | Billed per byte downloaded, if anything is downloadable yet | Settled every 5 min, extended per H3 |
| B4 | `provisioning` after an interruption (retry) | — never; the retry's provisioning is ours | — | Billed if downloaded | Locked; no extension needed for this time |
| B5 | `running` on a retry attempt | Billed from that attempt's `UP` to its end; cumulative with earlier attempts, capped at `max_runtime_seconds` | — | Billed if downloaded | Settled and extended as B3 |
| B6 | `succeeded` | Stops at process exit; nothing after | Billed from `finished_at` to deletion | Billed per byte downloaded | Final settle at terminal state; remainder released |
| B7 | `failed` (non-zero exit after `UP`) | Billed from `UP` to process exit | Billed from `finished_at` to deletion | Billed | Final settle; remainder released |
| B8 | `failed` before `UP` (e.g. `capacity_exhausted`, provisioning error) | — (zero) | — unless something was written; normally nothing | — | Released in full; no settle row unless quota > 0 |
| B9 | `interrupted` (terminal, no retry) | Billed from `UP` to the preemption time per end-event rule 4; work lost is still seconds run | Billed from `finished_at` to deletion | Billed | Final settle; remainder released |
| B10 | `cancelled` from `queued` or `provisioning` | — (zero) | — | — | Released in full |
| B11 | `cancelled` from `running` by the customer | Billed from `UP` to cancel acceptance (`202`), not to confirmed down | Billed from `finished_at` to deletion | Billed | Final settle; remainder released |
| B12 | `cancelled`, `error.code = balance_exhausted` | Billed from `UP` to compute-api's cancel | Billed from `finished_at` to deletion (ADR-0008 governs collection) | Refused while in arrears (ADR-0008) | Final settle ≤ hold by construction (H4); remainder released |
| B13 | `cancelled`, `error.code = billing_unavailable` | Billed from `UP` to compute-api's cancel | Billed from `finished_at` to deletion | Billed | Final settle queued and retried until the console answers |
| B14 | Any terminal state, artifacts within retention | — | Billed hourly, collected daily | Billed per byte | None (per-job hold is gone; daily charges per R5) |
| B15 | Any terminal state, artifacts past retention or deleted for arrears | — | — from the scheduled deletion time | — (objects gone; signed URLs refused) | None |
| B16 | Job reached `max_runtime_seconds` | Billed up to exactly `max_runtime_seconds` cumulative; nothing after | From `finished_at` as B6 | Billed | Final settle = `quota(max_runtime_seconds)` at most |

## Holds and settlement

This answers `architecture.md` open questions 1 and 2.

### Hold sizing

Two choices were on the table: a full-estimate hold (`max_runtime × rate ×
factor`) or a flat per-SKU minimum. Neither is right on its own:

- A **safety factor** buys nothing. With the end-event rules above, billed GPU
  seconds can never exceed `max_runtime_seconds`, so `max_runtime × rate` is an
  exact ceiling. Storage and egress are collected after the job, not from its
  hold. Factor = **1.0**.
- A **flat minimum** locks balance for no reason: there is no minimum charge,
  so a hold larger than the ceiling is balance the customer cannot spend and we
  can never collect.
- A **full hold** for long jobs locks too much. A 24-hour job on an 8-GPU SKU
  at ~USD 25/h would lock USD 600 at submit, money the same customer may want
  for token traffic on the same balance.

So the hold is a rolling window over the exact ceiling:

| ID | Rule | Number | Why |
|---|---|---|---|
| H1 | Initial hold at submit | `rate × min(max_runtime_seconds, 7200)` | Jobs up to 2 h get their full ceiling held: if they start, they finish, with no mid-run balance kill. Longer jobs lock 2 h of rate, not their whole runtime. |
| H2 | Submit refusal | Hold refused (`402`) → job not persisted, error message carries the H1 amount and the full ceiling `max_runtime × rate` | The customer sees both the amount they need now and what the whole job can cost. |
| H3 | Keep-ahead extension | After each settlement, if `hold − settled < rate × 3600` and the hold does not yet cover `max_runtime_seconds`, `extend` by `rate × min(3600, uncovered seconds)` | Keeps at least 1 h of runway: that is the console outage a running job survives (H5). The hold oscillates between 1 h and 2 h of rate. |
| H4 | Extension refused (`402`) | Emit a job event, retry at every settlement; when `hold − accrued < rate × 600` (two intervals), cancel with `error.code = balance_exhausted` | Gives the customer about 50 minutes, from the first refusal, to top up. Two intervals of margin means the final settle always fits inside the hold. |
| H5 | Console unreachable (timeout, `5xx`, `503`) | Job keeps running, usage keeps accruing in the ledger, settlement retries; when `hold − accrued < rate × 600`, cancel with `error.code = billing_unavailable` | Resolves the open question. Detail and reasoning in ADR-0008. |
| H6 | Hold expiry | `expires_at = submitted_at + max_runtime_seconds + 48 h` | Covers queueing, provisioning and a day of settlement retries. If the console's sweeper releases it anyway, usage is charged through the R5 path; nothing is lost. |
| H7 | Release | At terminal state, one `settle` with `final: true` | Settles and releases in one call, which the bridge already supports. |

Placeholder arithmetic, A10G at USD 1.20/h: a 30-minute job holds USD 0.60
and never extends; a 10-hour job holds USD 2.40 at submit and extends by
USD 1.20 roughly every hour after the first; the cancel margin is USD 0.20.

### Settlement cadence

| ID | Rule | Number | Why |
|---|---|---|---|
| R1 | Interval | Every **300 s** from the attempt's `UP`, per job | Each settle writes one consume-log row the customer sees. 60 s gives a 24 h job 1,440 rows; 300 s gives 288. Exposure per interval is 5 min × rate (USD 0.10 on the A10G placeholder), and with the hold always ahead (H3) the interval does not bound exposure anyway — the hold does. |
| R2 | Final | One settle at terminal state, `final: true` | — |
| R3 | Ledger first | Each interval writes a `usage_event` (`id`, `job_id`, `attempt`, `from`, `to`, `seconds`, `sku`, `rate`, cumulative `quota`) to the append-only ledger, **then** calls `settle` with that event's id | The ledger is the source of truth; a crash between the two re-sends the same event id, which the console treats as a replay. |
| R4 | Retry | Failed settles retry with backoff; the next interval's settle carries the newer cumulative amount and supersedes the pending one | Cumulative amounts make a missed settle harmless. |
| R5 | Post-terminal charges | Once a day at **06:00 UTC**, per account: storage for the previous UTC day and every egress log record not yet charged, as two charges (`compute/storage`, `compute/egress`). Each charge is a `hold` followed immediately by `settle` with `final: true`, idempotent on an id derived from account + meter + day | The per-job hold is gone by then. Daily keeps the customer's usage view at two rows per day instead of one per job per hour, and 06:00 leaves time for access logs (best-effort, usually within hours) to arrive. A charge that rounds to 0 quota is skipped, not carried. |
| R6 | Expired-hold fallback | Usage that cannot be settled because the hold expired (`409 hold_expired`) is charged through the R5 mechanism, on the next run, under the GPU meter's `model_name` | — |

R5 needs the bridge to accept a hold that is not tied to a job or to a live
key. That is a proposed change, listed under
[Proposed changes to the bridge](#proposed-changes-to-the-bridge).

## Interruption and retry

| ID | Rule |
|---|---|
| I1 | Provisioning is never billed: not the first attempt's, not a retry's, not a failover to another upstream. |
| I2 | Seconds an interrupted attempt actually ran are billed, even though the work is lost. That is the promise in `architecture.md` ("seconds actually run"). |
| I3 | The end of an interrupted attempt is the provider's preemption time if reported, else the last observed `UP`; never the detection time. The gap between last observation and detection is ours. |
| I4 | A retry attempt's setup (image pull, re-reading inputs) is billed like the first attempt's, because it runs on a GPU held for the customer after `UP`. |
| I5 | `max_runtime_seconds` caps cumulative billed seconds across attempts. A retry that would exceed it is not started; the job ends `interrupted`. |
| I6 | Each attempt is a separate ledger stream; settlement sends the cumulative total across attempts against the one hold. |

A retry changes the job state machine in `job-api.md` (`interrupted` is
terminal there; a retried job would return to `provisioning`). That is the
retry task's change to make, not this spec's; B4 and B5 describe the billing
once it exists.

## Cost breakdown on the Job (proposal)

The `usage` object in `job-api.md` today carries GPU only. Proposed shape,
additive (no existing field changes meaning):

```json
"usage": {
  "gpu_seconds": 1830,
  "storage_gb_hours": "12.500000",
  "egress_gb": "3.200000",
  "cost_usd": {
    "gpu": "0.6100",
    "storage": "0.0017",
    "egress": "0.3200",
    "total": "0.9317"
  },
  "estimated_cost_usd": "2.4000",
  "settled_cost_usd": "0.9317",
  "unbilled_cost_usd": "0.0000",
  "hold_usd": "2.4000",
  "rate_usd_per_hour": "1.2000",
  "attempts": [
    { "attempt": 1, "started_at": 1757520042, "ended_at": 1757521872, "gpu_seconds": 1830, "end_reason": "exited" }
  ]
}
```

- `storage_gb_hours`, `egress_gb`: decimal strings, 6 places; accrued to date.
- `cost_usd.*`: accrued to date, per meter, 4 places, rendered from the same
  exact amounts that are settled. `total` is the exact sum of the three
  amounts, rounded once, so it can differ from the sum of the displayed
  per-meter values by 0.0001.
- `estimated_cost_usd`: unchanged meaning, the GPU ceiling
  `max_runtime_seconds × rate`. Storage and egress are not estimated.
- `settled_cost_usd`: now all meters, everything the console has confirmed.
- `unbilled_cost_usd`: accrued but refused by the console (arrears, ADR-0008),
  or written off. Non-zero only in those cases.
- `hold_usd`: current hold, `0.0000` after release.
- `rate_usd_per_hour`: the rate snapshotted at submit.
- `attempts[].end_reason`: `exited`, `cancelled`, `max_runtime`, `preempted`,
  `provider_fault`, `balance_exhausted`, `billing_unavailable`.

Storage and egress are collected per account per day (R5) but attributed per
job in the ledger, so a job's `usage` shows its own share. Per-job attribution
is what the usage export (C10) emits.

## Proposed changes to the bridge

**Not in `console-billing-bridge.md` yet. Proposed here; the bridge spec is
edited in a separate change once the operator accepts this.** No new verb is
needed. One existing verb is relaxed:

1. **`POST /holds` with `token_id: 0` and no `job_id`.** Means: hold against
   the billing entity only, do not touch any token's `remain_quota`. Needed for
   R5 and R6: storage and egress accrue after the job, possibly after the key
   that submitted it was deleted or disabled, and they belong to the account,
   not to a key. Today the spec requires both and decrements the token.
2. **`sku` on a hold and its settles may be a meter name** (`storage`,
   `egress`), giving `model_name = compute/storage` / `compute/egress` in the
   consume log. The current spec already passes `sku` through to `model_name`;
   this records that non-SKU values are expected, so the console does not
   validate them against anything.

Optional, not required by this spec or ADR-0008: a way for compute to notify a
customer by email (arrears started, deletion scheduled). See ADR-0008's open
points.

## Open points

- **Prices.** Every number in `meters.json` and `skus.json` is a placeholder.
  Storage and egress prices wait for measured artefact sizes from the reference
  run (ADR-0005).
- **Free allowances.** None proposed. A free egress allowance (CoreWeave
  charges none) is a pricing decision, easy to add in `meters.json`.
- **Artifact delete endpoint.** Without one, a customer cannot stop storage
  charges before 30-day retention. Small, worth adding in P1.
- **Queue timeout.** A job can sit `queued` holding balance with no limit
  stated anywhere. H6 bounds the hold's life, not the queue.
- **S3 access logs** must be enabled by I1 before egress can be metered.
- **`job-api.md` SKU section's "rounded to 6 decimals"** wording should be
  aligned with the single-rounding rule above when that section is next edited.
