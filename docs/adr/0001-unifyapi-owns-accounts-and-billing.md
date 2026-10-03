# ADR-0001 — UnifyAPI owns accounts, keys, and the balance

- **Status:** Superseded by ADR-0011 (proposed)
- **Date:** 2026-09-10

## Context

UnifyAPI (`FelixSphere/unifyapi-console`, a fork of new-api) already has the
customer-facing plumbing a paid compute product needs: registration and login,
organisations (tenants) with a shared billing entity, `sk-` API keys with
per-key limits, a prepaid balance denominated in money (`QuotaPerUnit = 500000`
per USD 1), top-ups and payment integration, consume logs, invoices, and an
operator console with tenant reporting.

The research note that selected SkyPilot lists all of these as things the
compute product "still needs". Building them again for compute would produce a
second login, a second balance, and a second invoice for the same customer.

## Decision

Compute reuses the UnifyAPI account plane. Concretely:

- A compute customer **is** a UnifyAPI user (or tenant). There is no separate
  compute signup.
- A compute request authenticates with the customer's existing UnifyAPI
  `sk-` key. Compute does not mint keys.
- Compute spend draws down the same balance token spend does. GPU seconds are
  converted into the console's quota unit through a SKU price table that lives
  in this repository.
- Compute uses the console's two-step billing shape — reserve an estimate, then
  settle the actual — extended over the life of a job: **hold** at submit,
  **settle** periodically and at termination, **release** the remainder.
- The console records each settlement as a consume-log entry so it shows in the
  customer's existing usage view and invoice.

The customer sees: one key, one balance, one bill, two kinds of line item.

## Consequences

- The console must expose a small bridge API for key verification and
  hold/settle/release (`docs/spec/console-billing-bridge.md`). That is new
  console-side code, written in the console repository under its licence.
- Compute holds lock up balance for the duration of a job. Hold sizing and
  settlement cadence are product decisions, recorded in `docs/architecture.md`
  as open until decided.
- A console outage does not stop running jobs, but it does stop new submits
  (no key verification) and delays settlement. Exposure is bounded by the hold.
- Compute has no user table. Anything that looks like it needs one — per-user
  defaults, saved job templates — is keyed by the console's user id and stored
  in compute's own database.
