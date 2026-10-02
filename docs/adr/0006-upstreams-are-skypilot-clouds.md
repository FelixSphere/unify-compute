# ADR-0006 — Upstreams are SkyPilot clouds; a SKU maps to a candidate list; trust tier is a SKU attribute

- **Status:** Accepted
- **Date:** 2026-09-21

## Context

The product sells GPU seconds under our own SKUs but owns no GPUs
([ADR-0003](0003-skypilot-orchestration-foundation.md): SkyPilot provisions
into accounts we configure). The question was whether we can buy capacity from
other platforms — hyperscalers, neoclouds, GPU marketplaces, rented Kubernetes
clusters — and resell it, the way UnifyAPI relays tokens to model providers
through New API channels.

SkyPilot already abstracts about twenty providers behind one `resources`
block, adds any Kubernetes cluster via a kubeconfig, and turns arbitrary
machines with GPU drivers into an SSH node pool. Its `any_of` fails over
between candidates on capacity.

What SkyPilot does not do: distinguish a hyperscaler from a peer-to-peer
marketplace when a job carries customer data, cap spend per provider, or
reconcile against invoices.

## Decision

1. **An upstream is a SkyPilot cloud, Kubernetes context, or SSH pool.** No
   provider abstraction of our own. Adding an upstream is credentials plus a
   catalogue entry, never code in `compute-api/`.
2. **A SKU maps to an ordered list of candidate providers**, each a (cloud,
   instance type or context, regions) tuple. Placement emits them as one
   SkyPilot `resources` block with `any_of`; catalogue order is preference
   order. The customer buys the SKU; we choose the provider.
3. **Trust tier is a SKU attribute, not a routing decision.** `hyperscaler`,
   `neocloud`, `marketplace`. All providers of a SKU share its tier. A customer
   is never moved down a tier for price or capacity, because the tier is what
   they chose when they picked the SKU.
4. **The deployment declares its enabled upstreams** (`UPSTREAMS` env). The
   catalogue is what could be sold; the registry is what this process may
   actually launch. Staging enables `aws` only.
5. **First upstream is AWS**, where the account, staging stack and first SKU
   already are. **Second is one neocloud**, chosen to prove the multi-upstream
   path and measure the price delta, not for capacity. Closes open question 3
   in `docs/architecture.md`.

Requirements and their status are in [`docs/spec/upstreams.md`](../spec/upstreams.md).

## Consequences

- `skus.json` grows `accelerator`, `tier`, `interruptible`, `providers`; a
  change to provider order or tier is a release with money and trust in it,
  reviewed like a price change.
- Spend caps, per-upstream metering and invoice reconciliation are ours to
  build; SkyPilot will not. They land with the ledger.
- Every upstream's terms of service are checked for resale restrictions before
  it is enabled in production, and the finding is recorded in the ADR that
  enables it. This ADR enables none in production.
- Data movement between the artifact bucket (ADR-0005, us-east-1) and a
  non-AWS upstream crosses the public internet. Measured before enabling.
- Kubernetes and SSH pools give weaker isolation than one VM per job. They are
  `marketplace` tier unless we operate the cluster.
