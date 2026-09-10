# ADR-0002 — The compute service is a separate, proprietary process

- **Status:** Accepted
- **Date:** 2026-09-10

## Context

ADR-0001 puts accounts and billing in the console. The obvious shortcut is to
add the job API to the console binary as well: one deploy, one database,
direct function calls to `PreConsume`/`PostConsume`.

Three things argue against it:

1. **Licence.** The console is AGPL-3.0 and its source is public. Anything
   built into it is AGPL and public. Orchestration, tenant isolation, metering,
   and pricing logic are the product; the gateway repository's ADR-0002 already
   names this "the single most expensive mistake available".
2. **Runtime shape.** The console is a millisecond-latency relay on one small
   instance. Jobs run for minutes to hours on GPUs SkyPilot provisions
   elsewhere, and their controller has to survive the console restarting.
3. **Upstream drift.** The fork tracks an upstream shipping several commits a
   day. Every line added to upstream files is a future merge conflict; the
   fork's own `AGENT-WORKFLOW.md` says "prefer adding a file over editing one".

## Decision

`compute-api` is its own service, in this private repository, under a
proprietary licence. It integrates with the console **over HTTP only**, through
the bridge API in `docs/spec/console-billing-bridge.md`.

Rules that follow:

- No Go package from `unifyapi-console` is imported. No file, handler, model,
  migration, or UI component is copied from it. Reading it to understand
  behaviour is fine; the behaviour is reimplemented against the HTTP contract.
- `compute-api` dependencies are MIT / Apache-2.0 / BSD only, recorded in
  `THIRD-PARTY-LICENSES.md` in the same commit that adds them. Anything else
  stops and gets asked about.
- The console-side half of the bridge — the handlers that verify a key and
  hold/settle/release quota — is written in the console repository, as new
  files, marked `UNIFYAPI-BRAND`, and stays AGPL. It exposes existing console
  behaviour; it contains no compute logic.
- SkyPilot (Apache-2.0) is consumed as a dependency or sidecar, not modified.
  If a patch is genuinely needed it goes to the fork with an ADR, not into this
  repository.

## Consequences

- Two databases: the console's and compute's. Job state, idempotency keys, and
  the usage ledger live in compute's.
- Two deployables in every environment, including local. `deploy/compose`
  carries both.
- A change to the bridge contract is a coordinated release: console first,
  compute second. The CI/CD agent's notes carry that ordering.
- The HTTP-only position is engineering hygiene, not a legal opinion. Before the
  first paid customer, it gets reviewed by someone qualified to do so.
