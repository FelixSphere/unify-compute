# ADR-0007 — Product surfaces and phasing

- **Status:** Proposed. The *Left* line "Anything two-sided" is amended for
  templates only by [ADR-0012](0012-a-brokered-template-marketplace.md)
  (Proposed); a two-sided supply market stays excluded.
- **Date:** 2026-10-01 (condenses `docs/product-design.md`)

## Context

`spec/job-api.md` defines a v1 job contract and nothing beyond it. Every
extension proposed since — templates, quotas, events, storage meters,
instances, endpoints — has arrived as a one-off idea with no shared map of
where it belongs or what it waits on.

`docs/product-design.md` studied two references from their public docs on
2026-10-01. Both own something we do not: CoreWeave owns hardware, Vast.ai
owns a two-sided market. We resell capacity from upstreams chosen by us
(ADR-0006, PR #3, not yet merged), so what transfers is their **customer
contract**, not their supply side.

Binding and not reopened here: UnifyAPI owns accounts, keys and balance
(ADR-0001); compute-api is separate and proprietary, HTTP-only to the console
(ADR-0002); SkyPilot is the orchestration layer (ADR-0003); artifacts live in
our S3 (ADR-0005, Proposed, in the finetune worktree).

Nothing named in this ADR is built. `compute-api/` is a skeleton whose job
routes return `501`.

## Decision

The product is organised into five surfaces, each phased:

| Surface | v1 | P1 | P2 (deferred) | P3 |
|---|---|---|---|---|
| Catalogue | SKUs with fabric/topology; trust tiers and upstreams (ADR-0006) | Availability check, quotas | — | — |
| Workloads | Jobs | Templates, job events and webhooks | Interactive instances, serverless endpoints | Multi-node jobs |
| Data | Artifacts, inputs | — | Volumes | — |
| Money | Hold / settle / release | Three meters, zero-balance policy, usage export | — | Reserved capacity |
| Access | `sk-` key | Scoped keys, OpenAPI document, CLI | — | Terraform provider |

- **v1** is the existing contract made real: a job submitted with an `sk-` key
  runs on one upstream, is metered, settled, and its artifacts are
  downloadable. Its exit criterion is the first staging run (R1) being green.
- **P1** makes v1 usable by someone other than us without hand-holding.
- **P2 is deferred by the operator on 2026-10-01** — interactive instances,
  volumes and serverless endpoints, design included. It is **not planned
  until R1 is green**. When it is picked up, each of the three needs its own
  ADR, because `spec/job-api.md` excludes them from v1.
- **P3** is what only makes sense with revenue.

No phase reaches customers before staging has run it, and none goes on the
marketing site before the CI/CD agent says so (AGENTS.md).

### Taken from CoreWeave

1. **Fabric and topology on the SKU** — NVLink/InfiniBand decides whether
   multi-GPU training is fast or merely possible; it is a field, not copy.
2. **Requested vs granted is explicit** — over-quota is refused with the
   headroom in the error, never silently truncated as CoreWeave does.
3. **Upstream signals as job events** — we cannot run health checks on
   hardware we do not own, but preemption, provisioning failure and failover
   are things the customer should be able to read.
4. **Usage export in a FOCUS-style shape** — one format lets customers
   reconcile our charges and lets us reconcile upstream invoices.
5. **A capacity check before commitment** — `GET /skus/{id}/availability`
   from SkyPilot's optimizer, so a customer learns "no capacity" before a hold.

### Taken from Vast.ai

1. **Three meters** (GPU seconds, artifact storage, egress) — upstreams charge
   us for all three; folding two into the GPU rate only moves the surprise.
2. **A written billable-state table** — which job states meter what, pinned
   by tests, so "provisioning is not billed" is checkable.
3. **A zero-balance policy decided in advance** — especially for stored
   artifacts that keep costing after compute stops.
4. **Templates** — a reviewed image + command + env object is how the first
   customers start without writing a spec.
5. **Retryable vs terminal failure codes** — `failed` says whether a retry can
   help, so customers do not loop on `image_pull_failed`.
6. **Scoped keys and signed webhooks** — a key that runs compute without
   seeing billing; webhooks signed like the bridge.
7. **Serverless endpoints on the same SKUs** — taken as a direction only; it
   is P2 and deferred.

### Left

- **Anything two-sided** (host onboarding, bidding, host reliability scores) —
  we are not a marketplace; marketplace upstreams are a trust tier (ADR-0006).
- **Raw offer search** — the customer picks a tier and SKU, we pick the
  provider; that choice is the product.
- **Fleet health tooling** (HPC verification, node triage) — needs owned
  hardware.
- **Kubernetes as the customer-facing unit** — our customer calls an API with
  an `sk-` key; they do not operate a cluster.

## Consequences

- The task breakdown in `docs/product-design.md` (waves 0–3, tasks D/C/X/I/R)
  is the work plan for v1 and P1. It does not cover P2 or P3.
- The billing items here (three meters, billable-state table, zero-balance
  policy, hold sizing, settlement cadence) are decided in task D2
  (`docs/spec/metering.md`, ADR-0008), not in this ADR. Until D2 lands the
  only meter specified is GPU seconds.
- `spec/job-api.md` stays the v1 contract. P1 additions (templates, events,
  quotas, availability) amend it as they are built; until then they are not
  part of it.
- Any work on instances, volumes or endpoints before R1 is green contradicts
  this ADR. Picking P2 up is a new operator decision, recorded in a new ADR
  that supersedes this one's P2 deferral.
- Accepting this ADR changes no code and makes no customer-visible promise.
