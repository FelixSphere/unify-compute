# ADR-0013 — Top-up bonus credit and customers' refund requests

- **Status:** Accepted (operator, 2026-10-10)
- **Date:** 2026-10-10 (rebased on B2 2026-10-21)
- **Amends:** [ADR-0011](0011-compute-owns-accounts-and-billing.md) §4
  ("Refunds"): customers can ask for a refund from the dashboard; the
  operator still issues every one. Top-ups earn bonus credit.
- **Builds on:** B2 (#102, `internal/payments/refunds.go`), which applies
  every refund from Stripe's `charge.refunded`, however it was started.
- **Related:** [`spec/accounts.md`](../spec/accounts.md) ("Top-up", "Refund
  request", "Admin CLI").

## Context

On 2026-10-09 the operator asked whether users could pledge their balance as
collateral and receive money for it, or convert unused credit back into cash.
We advised against both while Compute is this small:

- Converting stored value to cash is money transmission, and lending against
  collateral needs a lending licence.
- Stripe prohibits both. That puts at risk the FelixSphere LLC Stripe account
  that Compute shares with Unify API.
- Paying cash out against card top-ups is a laundering route for stolen cards.
- Our cash would be committed up front, against the start-small rule.

The operator chose two low-risk measures instead, on 2026-10-10:

1. **Refunds to the original payment method**, which a customer can ask for.
   B2 (merged 2026-10-14) built the refund itself: `admin topup refund` and
   the webhook that applies it. Customers still had to write an email to ask.
2. **A prepaid discount**, given as bonus credit on larger top-ups.

Neither moves money anywhere except back to the card it came from. Neither
changes a GPU price.

## Decision

### 1. Bonus credit on a top-up

| Top-up | Bonus |
|---|---|
| USD 10 – 49.99 | none |
| USD 50 – 99.99 | 3% |
| USD 100 (the current per-top-up maximum) | 5% |

- The bonus is `floor(amount_cents × rate)`, in cents. It is fixed when the
  top-up is created and stored on it (`bonus_cents`), so changing the tiers
  never changes a payment already in flight. The tiers are a constant in
  `internal/payments` (`BonusTiers`). Changing them takes a PR that amends
  this table.
- The webhook that credits the payment credits the bonus in the same
  transaction, as a `topup_bonus` balance entry keyed `topup_bonus:tu_…`. A
  redelivered event writes neither.
- Before the customer pays, the form shows the credit they will get, and the
  Checkout line item names it ("Unify Compute credit, includes USD 5.00
  bonus"). The card is charged exactly the amount entered.
- **A new top-up's bonus does not count against the limits.** The USD 100
  per-top-up maximum applies to the payment. The USD 200 balance cap is
  checked against the balance plus the payment, not plus the bonus that
  payment will earn. Bonus credit already in the balance counts like any
  other credit. So a top-up can take an account past USD 200 by at most its
  own bonus (USD 5).
- **Cost:** at most USD 5 for each USD 100 paid, taken from margin.

### 2. The bonus follows the money

- **No cash value, never refunded by itself.**
- When part of a top-up leaves through a refund or an open dispute, the same
  share of its bonus leaves too. That share is
  `floor(bonus × (refunded + disputed) / amount)`, or all of the bonus once
  nothing of the payment is left. It is computed on running totals, so
  partial refunds never drift by rounding. It is written as a negative
  `topup_bonus` entry next to B2's `refund` or `dispute` entry, and it may
  take `available` below zero, as they may.
- A won dispute gives back the bonus that dispute took (`topup_disputes.bonus_cents`).
- The operator's refund check counts the debit: the refund and its bonus
  share must fit in `available`. Otherwise the refund is refused with the
  largest amount that would fit.
- The refund email names the bonus removed, when there is any.

### 3. Customers ask, the operator decides

- On `/billing`, each top-up with something refundable offers **Request
  refund**: an amount (by default all that is refundable) and an optional
  reason of up to 500 characters. Only owners and admins can ask, as only
  they can top up.
- This creates a `refund_request` (`rr_…`) in state `open`. A top-up has at
  most one open request; the customer can withdraw it. Asking moves no money
  and writes `refund_requested`. It also queues an operator notice to
  `OPERATOR_EMAIL` in `payment_emails`, keyed by the request id.
- The operator lists requests with `admin refund-request list`, refunds with
  B2's `admin topup refund`, or declines with
  `admin refund-request decline <rr_…> --message … --reason …`. A decline's
  message is emailed to the requester.
- **Any applied refund of the top-up closes its open request** as
  `refunded`, whatever amount was asked, including a refund made in the
  Stripe dashboard. The operator decided the amount.
- **Why not self-service.** An instant refund turns a stolen card into
  credit and back again with nobody looking. At our volume, a person
  checking each refund costs little.
- **Throttle:** 5 requests per account per 24 hours.

### What this does not do

- No cash-out of balance, no transfers between accounts, no lending. These
  would need their own ADR, a lawyer, and probably a licensed partner.
- The Terms and Conditions still do not cover refunds or bonus credit
  (`spec/accounts.md`, "Terms and Conditions"). This ADR is how we behave,
  not customer-facing terms. The legal review (O5) still has to write those.

## Consequences

- Migration 0015 adds `topups.bonus_cents` and `bonus_reversed_cents`,
  `topup_disputes.bonus_cents`, the `topup_bonus` balance-entry kind and
  `refund_requests`.
- Top-ups created before 0015 have no bonus, and nothing changes for them.
