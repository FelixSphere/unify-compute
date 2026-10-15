# ADR-0013 — Customers choose any listed GPU; capacity is provisioned on demand; providers are not customer-facing

- **Status:** Accepted
- **Date:** 2026-10-14
- **Amends:** [ADR-0006](0006-upstreams-are-skypilot-clouds.md) (what the customer sees), ADR-0010 "Start small" (how capacity is added)

## Context

The catalogue listed four SKUs and sold one, because every other GPU waited
on a cloud quota or a provider account we had not opened. Customers saw one
choice. The operator's decision (2026-10-14): list every GPU and let the
customer choose first; request quotas and open provider accounts when a
customer has actually asked; integrate every major platform, CoreWeave
included, behind the catalogue; and never show the customer which platform
runs a job.

## Decision

1. **Every listed SKU is choosable.** `available: true` means "may be chosen",
   not "launchable here". The dashboard and the public marketplace show the
   whole catalogue; the run form lets a customer pick any SKU.
2. **A SKU this deployment cannot launch is accepted as demand.** A submit
   for a SKU none of whose providers is in `UPSTREAMS` is refused with
   `503 gpu_not_provisioned` before anything is held, a row is written to
   `gpu_demand`, and the operator is emailed (once per account, SKU and UTC
   day, through `notify`). Nothing is launched and nothing is charged.
   A customer who names a disabled provider explicitly still gets
   `upstream_unavailable` (ADR-0006 R7); an empty registry still refuses
   everything with `upstream_unavailable` (R6): those are configuration,
   not demand.
3. **Capacity follows demand, under "Start small".** The row is the signal
   to raise a quota or enable an upstream. Enabling one is still: resale
   terms checked and recorded in an ADR, a monthly spend cap, credentials
   with `remote_identity: NO_UPLOAD`, and `UPSTREAMS` on the host. Nothing
   in this ADR opens an account or raises a quota by itself.
4. **Provider names are not customer-facing in the product.** The dashboard
   never shows a cloud or offers a provider choice; the run form's
   "Providers" control is gone. The trust tier stays visible, as ADR-0006
   promised. The job API keeps `providers` on SKU objects and on submit for
   compatibility; a later ADR may remove it.

## Consequences

- A customer can be refused with `gpu_not_provisioned` for a GPU the
  catalogue shows. The message says so and points at the other SKUs.
- `gpu_demand` grows by one row per refused submit; the operator reads it
  (`SELECT sku, count(*) FROM gpu_demand WHERE created_at > now() - interval '30 days' GROUP BY 1`)
  to decide what to provision next.
- The AWS g6 (L4) and spot G quotas and the GCP, Lambda, RunPod and CoreWeave
  accounts remain operator steps, each with its own ADR when enabled.
