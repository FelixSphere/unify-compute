# ADR-0008 — Zero-balance policy: compute stops, artifacts get seven days

- **Status:** Proposed
- **Date:** 2026-10-01
- **Related:** ADR-0001 (the console owns the balance), ADR-0005 (artifacts in
  our S3, 30-day retention; Proposed, not yet on `main`),
  [`spec/metering.md`](../spec/metering.md) (meters, holds, cadence).

## Context

Compute charges three meters (`spec/metering.md`): GPU seconds while a job
runs, artifact storage after it ends, and egress when artifacts are
downloaded. v1 already says what happens to GPU time at zero balance: when the
hold cannot be extended, the job is cancelled with `balance_exhausted`. Two
things were undecided:

1. **Artifacts keep costing storage after the job, with no hold behind them.**
   Something has to happen when the customer's balance cannot pay for it.
2. **The console can be unreachable** while a job runs. `architecture.md` left
   open whether the job is killed once settlement has failed for longer than
   the hold covers.

Vast.ai's answer to (1): compute is auto-stopped at zero; storage keeps
accruing and the balance goes negative within a buffer; data is destroyed only
when there is no card on file. We cannot copy that directly:

- We do not own the balance (ADR-0001). Compute moves quota only through the
  bridge verbs (`verify`, `hold`, `settle`, `extend`, `release`). `hold` and
  `extend` refuse with `402` when the balance is short; there is no verb that
  drives a balance negative, and adding one would put compute debt into the
  same number that gates the customer's token traffic.
- The bridge tells us nothing about payment methods. "Card on file" is not
  something compute can see.

## Decision

### Compute (GPU seconds)

- **Extension refused.** Per `metering.md` H3–H4: the hold is kept at least
  1 h of rate ahead of usage. When an extension is refused (`402`), compute
  emits a job event and retries at every 5-minute settlement. When the hold
  has less than two intervals (10 min) of rate left, compute cancels the job
  with `error.code = balance_exhausted`. GPU metering stops at that cancel, so
  the final settle always fits in the hold. From the first refusal the customer
  has about 50 minutes to top up. This is the existing v1 behaviour, made
  precise.
- **Console unreachable — the open question, decided: yes, the job is
  stopped, but only when the hold runs out.** The job keeps running, usage
  accrues in compute's ledger, settlement retries. When unsettled usage leaves
  less than two intervals of the hold, compute cancels with
  `error.code = billing_unavailable`, and the final settle is retried until the
  console answers. Because of H3, that is never sooner than about 50 minutes
  into an outage (or the job's own `max_runtime_seconds`, whichever is first).

  Why stop rather than run on: past the hold, compute is spending a balance it
  cannot see and cannot lock, and the customer's token traffic may be
  spending the same balance at the same time once the console recovers. A
  console outage longer than an hour already stops all token traffic and all
  new compute submits; it is a UnifyAPI incident in its own right. Running on
  would make our exposure unbounded for the sake of an outage that is already
  stopping everything else. The cost of the decision — a customer's job
  stopped because of our outage — is real, and is why the runway is 1 h and
  not one settlement interval.

### Storage and egress (after the job)

- **Collected daily, never negative.** Storage and egress are charged once a
  day per account (`metering.md` R5) as a `hold` followed by a final `settle`.
  If the hold is refused (`402`), nothing is charged and the account enters
  **arrears**: compute records the refused amount in its own ledger as
  unbilled. The console balance is never driven below zero by compute.
- **Unreachable is not refused.** A `5xx` or timeout on the daily charge is
  retried; it does not start arrears.
- **While in arrears:**
  - each daily run first tries to charge the whole outstanding amount plus the
    new day; success clears arrears;
  - `POST /jobs` first tries to collect the outstanding amount the same way;
    if that is refused, submit returns `402 insufficient_balance` with
    `error.code = storage_arrears` — no new GPU spend while storage is unpaid;
  - `GET /jobs/{id}/artifacts` still lists files but returns `402` instead of
    signed URLs, so egress cannot grow the unbilled amount;
  - job events (and the webhook, once C6 exists) report arrears start, the
    scheduled deletion time, and deletion.
- **Grace period: 7 days** from the first refused daily charge.
- **Deletion rule.** If arrears are not cleared within 7 days, compute deletes
  every artifact prefix (`jobs/<job_id>/`) belonging to the account, stops the
  storage meter at that moment, records `artifacts_deleted` with reason
  `balance_exhausted` on each affected job, and **writes off** the unbilled
  amount. Nothing is charged later for data that no longer exists.
- **30-day retention is unchanged** (ADR-0005). Arrears can only bring deletion
  forward, never postpone it.
- **No card-on-file distinction.** The bridge cannot express it, so the rule is
  the same for every account.

Why 7 days: long enough to cover a weekend and a slow top-up; short enough that
what we write off stays small — at the placeholder USD 0.10 per GB-month a
week of 100 GB is about USD 2.30. A longer grace mostly helps customers who
have left, at our cost.

### Bridge changes this needs

No new verb. Compute expresses the whole policy with `verify`, `hold`,
`settle`, `extend` and `release`. It does rely on one relaxation proposed in
`metering.md` ("Proposed changes to the bridge"): a hold with `token_id: 0`
and no `job_id`, charged against the billing entity only, because daily
storage charges belong to the account and may outlive the key that submitted
the job. Until the console accepts that change, R5 charges cannot be placed and
this ADR cannot be implemented as written.

## Consequences

- A customer who runs out of balance loses running compute within about an
  hour, and artifacts within 7 days, and is never left with a negative balance
  from compute.
- We carry a bounded loss: up to 7 days of storage and whatever egress one
  hour of signed URLs allows, per account in arrears, plus the gap between a
  preemption and its last observation (`metering.md` I3).
- Deletion is irreversible and compute cannot email the customer: the console
  owns contact details. Until there is a notification path, the only warnings
  are job events, webhooks and the `402` responses. **This must be resolved
  before the first paid customer**; see open points.
- A console outage longer than about 50 minutes stops running jobs with
  `billing_unavailable`. The CI/CD agent and the UnifyAPI CI/CD agent should
  treat a console release that takes the bridge down for longer than that as
  customer-visible for compute, not just for token traffic.
- New `error.code` values on the Job: `billing_unavailable`; on submit:
  `storage_arrears`. `balance_exhausted` already exists.

## Open points for the operator

1. **Customer notification before deletion.** Either the console gains a way
   for compute to ask it to email a user (a new bridge verb, e.g.
   `POST /api/compute/v1/notify` — **not** proposed here, only named), or
   deletion waits until one exists. The 7-day clock is meaningless to a
   customer who never hears it started.
2. **Write-off versus collect.** This ADR writes off arrears at deletion. The
   alternative, collecting them on the next top-up, is fairer to us and worse
   for the customer; it needs no bridge change either way.
3. **Grace length** may want to differ for tenants (organisations) on invoiced
   terms, if the console ever offers them. Not a v1 concern.
4. The 1-hour runway in H3 is the knob that trades locked balance against
   outage tolerance. If console incidents turn out to run longer, raise it in
   `metering.md`, not here.
