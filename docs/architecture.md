# Architecture

**Status: target architecture, mostly unbuilt.** The marketing site is live.
`compute-api/` is a skeleton with the job API contract wired to `501` stubs.
The console bridge is a spec. Sections marked **Open** are undecided. What the
product will and will not include, and in which phase, is proposed in
[ADR-0007](adr/0007-product-surfaces-and-phasing.md) (Proposed, not yet
accepted).

## One account plane, two execution planes

UnifyAPI already sells inference by the token: a developer signs up at
`app.unifyapi.ai`, gets an `sk-` key, tops up a dollar-denominated balance, and
calls `api.unifyapi.ai/v1`. Compute sells the same customer GPU time by the
second. The decision ([ADR-0001](adr/0001-unifyapi-owns-accounts-and-billing.md))
is that these are two kinds of spend against **one** account, key, and balance,
not two products with two logins.

```
                       ┌──────────────────────────────────────────────┐
                       │  Account plane — UnifyAPI console (AGPL fork)│
                       │  users · tenants · sk- keys · balance        │
                       │  top-ups · invoices · consume log            │
                       └───────────▲──────────────────▲───────────────┘
                 verify key /      │                  │   verify key / hold /
                 pre-consume /     │                  │   settle / release
                 post-consume      │                  │   (HTTP, HMAC-signed)
                                   │                  │
        ┌──────────────────────────┴───┐      ┌───────┴───────────────────────┐
        │  Token plane                 │      │  Compute plane — this repo    │
        │  api.unifyapi.ai/v1          │      │  api.compute.unifyai.us       │
        │  relay to model providers    │      │  /compute/v1/jobs …           │
        │  (inside the console binary) │      │  compute-api (Go, proprietary)│
        └──────────────────────────────┘      └───────┬───────────────────────┘
                                                      │ SkyPilot API
                                              ┌───────▼───────────────────────┐
                                              │  FelixSphere/skypilot (Apache) │
                                              │  provisions GPUs in our cloud  │
                                              │  accounts, runs the job,       │
                                              │  tears it down                 │
                                              └───────────────────────────────┘
```

The token plane is upstream new-api behaviour and lives inside the console.
The compute plane is a separate process because of the licence boundary
([ADR-0002](adr/0002-compute-service-is-separate-and-proprietary.md)) and
because its lifecycle is minutes to hours, not milliseconds.

## Why the console's billing model fits

new-api's `quota` is an integer denominated in money: `QuotaPerUnit = 500000`
per USD 1. Token usage is converted into it through model ratios. GPU seconds
can be converted into the same unit through a SKU price table. The customer
sees one balance.

new-api also already bills in two steps — pre-consume an estimate, then
post-consume the actual, refunding or charging the difference. A compute job
needs exactly that shape, stretched over time:

| Token relay | Compute job |
|---|---|
| Pre-consume estimated tokens at request start | **Hold** the estimated cost at job submit |
| Post-consume the actual token count at response end | **Settle** metered GPU seconds at job end, and periodically while it runs |
| Refund the difference | **Release** the unused part of the hold |

The bridge contract in [`spec/console-billing-bridge.md`](spec/console-billing-bridge.md)
is those three verbs plus key verification, exposed by the console over HTTP
and called by compute-api.

## What compute-api owns

These are the responsibilities compute-api is designed to carry in v1. Of them,
only the job API routes (as `501` stubs) and the SKU catalogue exist today;
job state, idempotency, metering and the SkyPilot integration do not.

- The job API ([`spec/job-api.md`](spec/job-api.md)): submit, get, list,
  cancel, logs, artifacts. Authenticated with the customer's existing `sk-` key.
- Job state and idempotency. Postgres, its own database, never the console's.
- The SKU catalogue: which GPU shapes are offered and at what price per second.
  Compute prices live here, in code, the same way the console keeps its model
  price baseline in code rather than in the database — so a diff is reviewable.
- Metering: per-job GPU seconds from SkyPilot's cluster lifecycle events,
  written to an append-only usage ledger before being settled to the console.
- Talking to SkyPilot: launch, status, logs, down.
- The upstream registry: which clouds this deployment may launch into, and
  the SKU → provider placement that turns a job into a SkyPilot `resources`
  block ([ADR-0006](adr/0006-upstreams-are-skypilot-clouds.md),
  [`spec/upstreams.md`](spec/upstreams.md)).

## What compute-api does not own

- Users, keys, balance, payments, invoices, tenant reporting. Console.
- GPUs. SkyPilot provisions them in cloud accounts we configure; the fork does
  not supply hardware.
- Model runtimes. vLLM, embedding servers, OCR pipelines are workload containers
  the job runs, not part of the platform.

## Scope and phasing

[ADR-0007](adr/0007-product-surfaces-and-phasing.md) (Proposed) groups the
product into five surfaces — catalogue, workloads, data, money, access — and
phases them v1, P1, P2, P3. v1 is the job contract in
[`spec/job-api.md`](spec/job-api.md); nothing in any phase is built yet.

Not in v1, and not built:

- **P1** — templates, job events and webhooks, quotas, an availability check,
  scoped keys, an OpenAPI document and CLI, storage and egress meters, a
  zero-balance policy, usage export. Planned after v1; the billing items are
  task D2's to specify.
- **P2** — interactive instances, persistent volumes, serverless endpoints.
  **Deferred by the operator on 2026-10-01**, design included; not planned
  until the first staging run (R1) is green. Each would need its own ADR.
- **P3** — reserved capacity, multi-node jobs, a Terraform provider. Only
  considered once there is revenue.

## Failure modes that have to be designed, not discovered

- **Console unreachable mid-job.** The job keeps running; usage keeps accruing
  in the local ledger; settlement retries. The hold placed at submit is the
  bound on exposure. Decided in [ADR-0008](adr/0008-zero-balance-policy.md)
  (Proposed): the hold is kept an hour ahead, and the job is cancelled with
  `billing_unavailable` only when that runs out.
- **Job runs longer than estimated.** Periodic settlement (every N minutes)
  draws down the hold; when the hold is consumed the service either tops up the
  hold from remaining balance or terminates the job. `max_runtime_seconds` on
  the job caps this and has a platform ceiling.
- **Interrupted GPU (spot preemption, provider fault).** Customer is charged
  for seconds actually run, never for the retry's provisioning time.
  **Open:** whether automatic retry is default-on.
- **Unpriced SKU.** Refused at submit. The gateway's testing showed that an
  unpriced model meters at zero and becomes invisible spend; compute must not
  repeat that.
- **Duplicate submit.** `Idempotency-Key` header, stored with the job for 24h.

## Environments

| | Console | Compute API | Where |
|---|---|---|---|
| Local | `deploy/compose` console container, empty DB | `go run ./cmd/compute-api` or container | developer machine |
| Staging | `console.staging.compute.unifyai.us`, own DB | `staging.compute.unifyai.us` | one t4g.small, `deploy/aws/envs/staging/` — **not yet applied** |
| Production | `app.unifyapi.ai` (UnifyAPI CI/CD agent's) | `api.compute.unifyai.us` — **does not exist** | one t4g.small, `deploy/aws/envs/production/` — defined, **not applied** ([ADR-0009](adr/0009-host-everything-on-aws.md), proposed) |

Staging runs its own console on purpose ([ADR-0004](adr/0004-staging-environment.md)):
the bridge endpoints do not exist in production yet, and even once they do, a
compute bug that places holds against real customer balances is not a staging
incident, it is a production one.

## Open questions, in the order they need answering

1. **Hold sizing.** Proposed in [`spec/metering.md`](spec/metering.md#hold-sizing):
   no safety factor and no flat minimum; the hold is `rate × min(max_runtime, 2 h)`
   at submit, extended to stay at least 1 h of rate ahead.
2. **Settlement cadence.** Proposed in [`spec/metering.md`](spec/metering.md#settlement-cadence):
   every 5 minutes plus a final settle on termination; storage and egress
   daily per account.
3. **First SKU and first provider.** One GPU type on one cloud, per the
   research note's suggestion. Which one decides the first SkyPilot config.
   Answered by [ADR-0006](adr/0006-upstreams-are-skypilot-clouds.md):
   `a10g-24gb-x1` on AWS; a neocloud second, for the price delta.
4. **Where job results live.** Object storage in our account with signed URLs,
   or the customer's bucket. Affects egress cost and data-residency story.
   [ADR-0005](adr/0005-artifact-storage.md) (Proposed) proposes our S3,
   uploaded by the run step with presigned URLs; until it is accepted this
   stays open.
