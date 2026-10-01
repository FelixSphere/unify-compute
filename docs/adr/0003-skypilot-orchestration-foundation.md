# ADR-0003 — SkyPilot is the orchestration foundation

- **Status:** Accepted
- **Date:** 2026-09-10 (records the decision made in `research/open-source-foundation.md`)

## Context

The compute product has to provision GPU capacity across clouds and Kubernetes,
run a job on it, recover from interruptions, collect logs, and tear the
capacity down. Ray, BentoML, and vLLM were evaluated alongside SkyPilot; the
comparison is in the research note.

## Decision

SkyPilot is the orchestration layer, consumed through the
`FelixSphere/skypilot` fork (Apache-2.0), pinned to a tested upstream release
(v0.13.0 at the time of writing). The fork is kept upstream-compatible and
unmodified; it exists so a release can be pinned and, if ever needed, patched
with an ADR.

`compute-api` drives SkyPilot through its API server, not by shelling out to
the CLI, so the two can run on different hosts and the API server can be
replaced by a managed deployment later.

## Consequences

- SkyPilot deploys into cloud accounts we configure. It does not supply GPUs;
  provider credentials and quotas are ours to manage.
- SkyPilot's cost estimates are not a billing ledger. Compute meters GPU
  seconds itself from cluster lifecycle events and reconciles against provider
  invoices.
- A SkyPilot version bump is a release that changes what gets provisioned. The
  CI/CD agent reads upstream's changelog between pins before shipping one.
- Apache-2.0 carries a NOTICE obligation; the packaging that ships SkyPilot
  carries the notice.
