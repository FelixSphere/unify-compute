# Compute upstreams — functional requirements

- **Status:** Requirements R1–R9 and R16 implemented in `compute-api/`
  (catalogue, placement, validation, API exposure) and R10 (placement recorded
  and served).
  R11–R15 are planned and depend on the job backend, the ledger, or a process
  outside the code. Each item says which.
- Decision record: [ADR-0006](../adr/0006-upstreams-are-skypilot-clouds.md).

An **upstream** is a source of GPU capacity we buy from and resell by the
second: a hyperscaler account, a neocloud account, a marketplace account, a
Kubernetes cluster somebody rents us, or bare machines reachable over SSH. In
New API terms it is a channel. In SkyPilot terms it is a cloud, a Kubernetes
context, or an SSH node pool.

## Catalogue

| # | Requirement | Status |
|---|---|---|
| R1 | Every SKU names the providers it can run on: for each, the SkyPilot cloud, the instance type (or the context for Kubernetes/SSH pools), and the regions. A SKU with no provider cannot be `available`. | Implemented: `sku.Provider`, validated at boot |
| R2 | Every SKU carries a **trust tier** — `hyperscaler`, `neocloud`, or `marketplace` — and all of its providers belong to that tier. A customer who buys a hyperscaler SKU is never placed on a marketplace host, whatever the price. The tier is what the customer chooses; the provider is what we choose. | Implemented: `sku.Tier`, required for available SKUs |
| R3 | Every SKU names its SkyPilot accelerator (`A10G`, `L4`, `A100-80GB`) and whether it is `interruptible` (spot). Interruptible capacity is a different SKU, not a flag at submit, because it is a different price and a different failure mode. | Implemented |
| R4 | A SKU's `regions` field is the union of its providers' regions. | Implemented, derived when omitted |
| R5 | The catalogue is validated when the binary starts: unknown cloud, duplicate cloud within a SKU, missing instance type, missing tier, or an available SKU without providers stops the process. A bad catalogue never serves. | Implemented, `sku.Parse` |
| R16 | Every **available** SKU with `gpu_count` > 1 declares its `interconnect` — `pcie` or `nvlink` — and all of its providers deliver it, as with the tier (R2). On the same accelerator and the same price line, eight GPUs on PCIe and eight on NVSwitch differ by an order of magnitude, so a multi-GPU SKU that says nothing is an ambiguity the customer pays for. A single-GPU SKU may not declare one: it describes GPU-to-GPU wiring and there is no pair. The node-to-node `network` (`ethernet`, `infiniband`) is validated when present but read by nothing: multi-node jobs are P3. | Implemented: `sku.Interconnect`, `sku.Network`, validated at boot |

## Placement

| # | Requirement | Status |
|---|---|---|
| R6 | The deployment declares which upstreams it holds credentials for (`UPSTREAMS=aws,lambda`). The catalogue says where a SKU *could* run; the registry says where *this process may send it*. An empty registry refuses every submit with `503 upstream_unavailable`. | Implemented: `upstream.Registry` |
| R6a | A listed SKU none of whose providers is enabled, asked for without a `providers` list, is demand (ADR-0013): `503 gpu_not_provisioned`, a `gpu_demand` row, an operator email once per account, SKU and UTC day. | Implemented: `api.Server.Demand`, `demand.Service` |
| R7 | A submit may restrict placement to a subset of the SKU's providers (`"providers": ["aws"]`). Naming a provider the SKU does not offer is `400 provider_not_offered`. Naming one the deployment has not enabled is `503 upstream_unavailable`. | Implemented |
| R8 | Placement resolves to one SkyPilot `resources` block. One candidate is emitted flat; several are emitted under `any_of` in catalogue order, and SkyPilot fails over between them on capacity. Catalogue order is therefore the preference order and is a reviewed change. | Implemented: `skypilot.ResourcesFor` |
| R9 | `max_runtime_seconds` has a platform ceiling, set per deployment in `compute-api/config/platform.json`: **4 hours** (14400) since ADR-0010's "Start small" amendment of 2026-10-03, was 24 hours. The API contract never allows more than 24 hours, and a deployment may only lower it. Above the ceiling is `400 runtime_over_ceiling`, for a hand-written spec and a template override alike, and a template whose default is above it fails the boot. An `available: false` SKU is `400 sku_unavailable`. Both are checked before anything is persisted or held. The GPU reaper's maximum age is the ceiling plus 1 h, derived from the same file. | Implemented: `quota.Platform.MaxRuntimeSeconds`, `api.Server.RuntimeCeilingSeconds` |
| R10 | Once a job is running, the Job object records where it landed: `placement: {cloud, region, instance_type}`. Metering, reconciliation, and the customer's invoice line all key off this. | Implemented: the backend writes it at cluster `UP`, with a `job.placed` event, before the job reads `running` (`store.SetPlacementWithEvent`, [`backend.md`](backend.md)); `GET /jobs/{id}` serves it |

## Money and trust

| # | Requirement | Status |
|---|---|---|
| R11 | Each upstream has a monthly spend cap. When the cap is reached the upstream is treated as not enabled; jobs fall over to the next provider or are refused. SkyPilot has no budget control, so this lives in compute-api. | Planned: needs the ledger |
| R12 | GPU seconds are metered per job and per upstream, and reconciled monthly against each upstream's invoice. A mismatch above 5% blocks price changes for that SKU until explained. | Planned: needs the ledger and a month of invoices |
| R13 | Inputs and outputs cross upstream boundaries over the public internet unless the artifact bucket is in the same provider and region. The cold-start and egress cost of that is measured per upstream before the upstream is enabled in production. | Planned: `finetune/record.py` measures cold start; egress is ADR-0005 |
| R14 | Before an upstream is enabled in production its terms of service are checked for resale restrictions, and the finding is recorded in the ADR that enables it. No upstream is enabled on a hunch. | Process; not code |
| R15 | Customer isolation on VM upstreams is one job per VM. On Kubernetes and SSH pools it is namespace-level and weaker; those pools are `marketplace` tier unless we operate the cluster ourselves. | Process, enforced by the tier rule (R2) |

## API surface added

- `GET /compute/v1/skus` — SKU objects gain `accelerator`, `tier`, `interruptible`, `providers`, and, on multi-GPU SKUs, `interconnect` (R16).
- `GET /compute/v1/upstreams` — public; every cloud the catalogue references, whether this deployment has it enabled, and which SKUs it serves.
- `POST /compute/v1/jobs` — validates `sku`, `providers`, and `spec.max_runtime_seconds` and returns the 400/503 errors above before anything is held; a deployment without a SkyPilot API server (`SKYPILOT_API_SERVER_URL`) is also `503 upstream_unavailable`. The dark build (no job store) still answers what passes `501 not_implemented`.

## Out of scope for now

Per-customer provider preferences, price-aware routing across tiers, and
selling a customer's own cloud account as an upstream (bring-your-own-cloud).
Each would be its own ADR.
