# Product design — what we borrow from CoreWeave and Vast.ai, and the work it breaks into

- **Status:** Proposal, 2026-10-01. Nothing here is built. It reorganises the
  product into surfaces, phases them, and breaks the work into tasks sized for
  one agent and one worktree each. Accepting it means writing ADR-0007 (task
  D1); until then the v1 scope in `spec/job-api.md` stands.
- **Binding constraints, not reopened here:** UnifyAPI owns accounts, keys and
  balance (ADR-0001); compute-api is separate and proprietary, HTTP only to the
  console (ADR-0002); SkyPilot is the orchestration layer (ADR-0003); upstreams
  are SkyPilot clouds with trust tiers (ADR-0006, PR #3, not yet merged);
  P2 is deferred (operator, 2026-10-01);
  artifacts live in our S3 (ADR-0005, Proposed, in the finetune worktree).

## What the two references actually are

Researched 2026-10-01 from public docs; sources at the end. Both own something
we do not: CoreWeave owns hardware, Vast.ai owns a two-sided market. We resell
capacity from upstreams, so what transfers is their **customer contract**, not
their supply side.

| | CoreWeave | Vast.ai | Us |
|---|---|---|---|
| Customer's primary unit | A Kubernetes cluster (CKS) and its Node Pools | An instance rented from a host's offer | A job (v1); later an instance and an endpoint |
| How capacity is described | Named instance types (`gd-8xh100ib-i128`): GPU, count, fabric, vCPU, RAM, NVMe, zone | Per-machine offers, ~50 filterable fields, host-set prices | Curated SKUs with a trust tier; provider is our choice, not theirs |
| Capacity classes | On-demand, spot (`computeClass: spot`), reserved, flex reservation | On-demand, interruptible (bid; paused, disk kept), reserved = prepaid discount | On-demand and interruptible as separate SKUs (R3) |
| Meters | Instance-hour, storage GB-month, egress free | GPU per second, storage per second (also while stopped), bandwidth per byte | GPU seconds only, today |
| Balance at zero | Invoiced; not applicable | Compute auto-stopped; storage accrues negative within a buffer; data destroyed if no card | **Undecided** (architecture.md open question) |
| Lifecycle visibility | Node state labels, conditions, events naming blocking pods | Explicit states incl. "will never reach running — destroy and retry" | Six job states, no events |
| Developer surface | Console, kubectl, Terraform provider, `cwic` CLI, FOCUS usage export | Console, REST + OpenAPI, CLI/SDK mirroring it, scoped keys, signed webhooks | REST contract only |

## What we take, what we leave

**Take from CoreWeave**

1. **Fabric and topology in the SKU.** A multi-GPU SKU says whether it has
   NVLink/InfiniBand, because that decides whether multi-GPU training is fast or
   merely possible. Field, not marketing copy.
2. **Requested vs granted is explicit.** CoreWeave truncates over-quota requests
   silently; that is the behaviour to avoid. Our quota refusals are `429/403`
   with the headroom in the error, never a partial grant.
3. **Health as visible events.** We do not run GPU health checks (we do not own
   the hardware), but every upstream signal SkyPilot gives us — preemption,
   provisioning failure, failover to the next provider — becomes a job event the
   customer can read.
4. **Usage export in a standard shape.** FOCUS-style per-hour rows per job and
   meter, so the customer can reconcile our charges, and we can reconcile
   upstream invoices (R12) with the same code.
5. **A capacity check before commitment.** CoreWeave's Capacity Finder; ours is
   `GET /skus/{id}/availability`, answered from SkyPilot's optimizer per
   enabled upstream.

**Take from Vast.ai**

1. **Three meters, each a line on the job.** GPU seconds, artifact storage
   GB-hours, egress GB. Upstreams charge us for all three; folding storage and
   egress into the GPU rate only moves the surprise (ADR-0005 already flags
   egress as ours to pay).
2. **A written billable-state table.** For every job state: is GPU metered, is
   storage metered. `provisioning` is not GPU-billed; that is already the v1
   promise and it becomes a table the tests pin.
3. **A zero-balance policy decided in advance.** Compute stops when the hold
   cannot be extended (already in v1); what happens to artifacts that keep
   costing storage is the part that is missing.
4. **Templates.** Image + command + env + default disk + ports as a named,
   reviewed object. Curated ones (`finetune-lora`, `batch-embed`,
   `batch-infer-vllm`) are how the first customers start without writing a
   spec.
5. **Explicit "won't recover" errors.** `failed` carries an `error.code` that
   says whether a retry can help (`image_pull_failed` no, `capacity_exhausted`
   yes).
6. **Scoped keys and signed webhooks.** A key that can run compute but not see
   billing; a webhook on terminal states signed like the bridge.
7. **Serverless endpoints on the same SKUs** (P2, deferred): min/max replicas,
   scale to zero, OpenAI-compatible route. SkyServe gives us the mechanism.

**Leave**

- Anything two-sided: host onboarding, offer slicing, bidding, reliability
  scores for third-party machines. Marketplace upstreams are already covered by
  the `marketplace` trust tier.
- Raw offer search. The customer chooses a tier and a SKU; we choose the
  provider. That is ADR-0006 and it is the product.
- Fleet health tooling (HPC verification, node triage). Needs owned hardware.
- Kubernetes as the customer-facing unit. Our customer is calling an API with
  an `sk-` key, not operating a cluster.

## The product, reorganised into five surfaces

```
 Catalogue        Workloads                    Data             Money                Access
 ─────────        ─────────                    ────             ─────                ──────
 SKUs (+fabric)   Jobs            v1           Artifacts  v1    Hold/settle   v1     sk- key        v1
 Tiers (R2)       Templates       P1           Inputs     v1    3 meters      P1     Scoped keys    P1
 Upstreams (R6)   Events/webhook  P1           Volumes   (P2)   Zero-balance  P1     OpenAPI + CLI  P1
 Availability P1  Instances      (P2)                           Usage export  P1     Terraform      P3
 Quotas       P1  Endpoints      (P2)                           Reserved      P3
                  Multi-node      P3

 (P2) = deferred by the operator on 2026-10-01; not planned.
```

- **v1** is the existing contract made real: a job submitted with an `sk-` key
  runs on one upstream, is metered, settled, and its artifacts downloadable.
- **P1** makes v1 usable by someone other than us without hand-holding:
  templates, events, quotas, the billing semantics written down, a CLI.
- **P2 — deferred.** The operator decided on 2026-10-01 not to do interactive
  instances, volumes or serverless endpoints yet, design included. They are
  the other workload shapes customers of both references expect, and each
  would need an ADR because v1 excludes them; revisit after R1 is green.
- **P3** is what only makes sense with revenue: reserved capacity, multi-node,
  Terraform provider.

No phase ships to customers before staging has run it (AGENTS.md, staging
first), and none goes on the marketing site before the CI/CD agent says so.

## Task breakdown

Every task is one agent, one worktree, one branch, one PR. "Base" is the
branch to cut from: `main` once #1–#3 are merged, otherwise `main-ph0vlz` plus
whatever is listed under depends. Task IDs: **D** design/doc, **C** compute-api
code, **X** another repository, **I** infrastructure.

### Wave 0 — unblock (operator)

| ID | Task | Who |
|---|---|---|
| W0 | Merge #1 → `main` (merge commit, so #2 and #3 keep their history), retarget and merge #2 (red `pinned-images` is known and intended), then #3 | operator / any agent with approval |

#1 and #2 merged on 2026-10-01 (`ae6cb25`, `89600be`). #3 waits on the
operator's test-guard approval because it modifies existing tests. Tasks that
need #3's code (C3, C4, C7) wait for it; everything else cuts from `main`.

### Wave 1 — start in parallel

| ID | Task | Touches | Depends | Done when |
|---|---|---|---|---|
| D1 | ADR-0007: product surfaces and phasing (this document, condensed into a decision) | `docs/adr/0007-*`, `docs/architecture.md` | — | ADR `Status: Proposed`; architecture.md links it; no present-tense claims about unbuilt things |
| D2 | Billing semantics spec: three meters, billable-state table, zero-balance policy (ADR-0008) | `docs/spec/metering.md`, `docs/adr/0008-*`, `docs/spec/job-api.md` metering section | — | Table covers every job state × meter; zero-balance ADR answers the architecture.md open question; hold sizing and settlement cadence decided with numbers |
| C1 | Job store: Postgres schema, migrations, repository layer, idempotency (24 h) | `compute-api/internal/store/`, `THIRD-PARTY-LICENSES.md` | W0 | `go test` runs against a real Postgres in CI; driver is MIT/BSD and recorded |
| C2 | Fake console: an in-process test server implementing the bridge contract (verify/hold/settle/extend/release) from the spec, for compute-api tests | `compute-api/internal/console/consoletest/` | W0 | Signature vector from `client_test.go` verified by the fake; no code from the console repo |
| X1 | Console-side bridge routes in `FelixSphere/unifyapi-console` (AGPL, new files only) | other repo | spec only | Routes behind `COMPUTE_BRIDGE_SECRET`, 404 without it; the pinned signature vector passes; released only via the UnifyAPI CI/CD agent |
| C3 | OpenAPI 3.1 document generated from `spec/job-api.md` and served at `/compute/v1/openapi.json` | `compute-api/internal/api/`, `docs/spec/openapi.json` | W0 incl. #3 | A test fails if a route exists without an OpenAPI entry |

### Wave 2 — the job path end to end

| ID | Task | Touches | Depends | Done when |
|---|---|---|---|---|
| C4 | SkyPilot backend: launch/status/logs/down against the SkyPilot API server, using `internal/skypilot` placement from #3; records `placement` (R10) | `compute-api/internal/backend/` | W0, C1 | Integration test against a local SkyPilot API server with the `kubernetes` (kind) cloud; no real GPU needed |
| C5 | Metering and settlement: append-only usage ledger, 3 meters, settlement loop, extend-or-cancel at hold exhaustion | `compute-api/internal/ledger/` | C1, C2, D2 | Every row of D2's billable-state table is a test case; console-down test proves usage is not lost |
| C6 | Job events and webhooks: event log per job, `GET /jobs/{id}/events`, signed webhook on terminal state, retry with backoff | `compute-api/internal/events/` | C1 | Webhook signature uses the bridge scheme with a per-key secret; delivery is at-least-once with an event id |
| C7 | Templates: `config/templates.json`, `GET /compute/v1/templates`, `template` on submit with field-level override | `compute-api/internal/template/`, `config/` | W0 | Three curated templates; `finetune-lora` matches what `finetune/` runs |
| C8 | Quotas: per-account concurrent jobs and GPUs per tier, enforced at submit, error carries headroom | `compute-api/internal/quota/` | C1 | Over-quota is refused, never truncated |
| I1 | Artifacts bucket + IAM in the staging stack (ADR-0005) | `deploy/aws/staging/` | W0, ADR-0005 accepted | `make tf-validate`; coordinate with the finetune worktree, which owns ADR-0005 |

### Wave 3 — prove it, then extend

| ID | Task | Depends | Done when |
|---|---|---|---|
| C9 | `GET /skus/{id}/availability` from SkyPilot's optimizer per enabled upstream | C4 | Cached ≤ 60 s; never claims capacity SkyPilot did not report |
| C10 | Usage export: FOCUS-shaped CSV/JSON per account and month | C5 | Same rows feed the upstream reconciliation (R12) |
| C11 | CLI `unify` (Go, stdlib): `jobs submit/get/logs/cancel`, `skus`, `templates` | C3 | Generated from or tested against the OpenAPI document |
| R1 | First staging run: publish the image, pin its digest, apply staging, run the reference fine-tune as a job | C4, C5, X1 in staging console, I1 | **CI/CD agent only.** Exercise: submit → hold → run → settle → artifacts → release, plus cancel mid-run and console-down mid-run |

### Dependency graph

```
W0 ──┬─ C1 ──┬─ C4 ── C9
     │       ├─ C5 ── C10 ─┐
     │       ├─ C6         │
     │       └─ C8         ├─ R1 (CI/CD agent)
     ├─ C2 ──── C5         │
     ├─ C3 ──── C11        │
     ├─ C7                 │
     └─ I1 ────────────────┤
X1 ────────────────────────┘
D1, D2 — no code dependency; D2 gates C5
```

### Rules every task agent follows

- Read `AGENTS.md` first. Licence boundary: nothing from `unifyapi-console` in
  this repo; new dependencies MIT/Apache/BSD and recorded in the same commit.
- Own `git worktree` and branch `<type>/<id>-<short>`; say which ports you
  used.
- Files outside the "Touches" column belong to another task. If you need a
  change there, say so in the PR rather than making it.
- Hand off with the AGENTS.md checklist output, what is left, and — for code —
  the specific behaviour to exercise in staging.
- No agent but the CI/CD agent publishes images, applies Terraform, or
  deploys.

## Sources

CoreWeave: docs.coreweave.com — capacity plans, spot node pools, GPU
instances, CKS node pools and quotas, Kueue, SUNK, Dedicated Inference,
storage, node state transitions, SkyPilot tutorial, changelog through
2026-09-30; coreweave.com/pricing. Vast.ai: docs.vast.ai — concepts,
search-offers, instance types, reserved instances, templates, instance
management and create-instance API, storage types and volumes, billing and
billing FAQ, API keys, teams, notification webhooks, serverless architecture,
host verification. Figures that the sources contradict (Vast reserved discount
ceiling, CoreWeave node NVMe size) are not relied on above.
