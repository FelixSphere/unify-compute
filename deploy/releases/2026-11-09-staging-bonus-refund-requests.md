---
environment: staging
action: release
outcome: success
run: 42678010483
commit: 792b4ea4008ef24be7cd4b5716972c5cb10abd69
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:15cf14fb57d9a6a12e3483a07865463e33ba4944d49cc8c9225ef1510835c56f
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:a8281c7835a2c2b4646e838f1d6b4882342476eb8c3eda1143a481dc56ee3b9c
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:b7474c942012f43d362265171e480c24f4d93a0d085640916f32b2d187bdf042
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:ed5de01aba3f5695882a8f56bad0a1eaed8583d19585cb76dcb088307e77dca4
hotfix: false
---

# 2026-11-09: top-up bonus credit and refund requests (#134, ADR-0013), on staging

Released by the unify-compute CI/CD agent on the operator's approval ("合并并发布
#134"). The operator approved it knowing two things. **Stripe test mode has
never been run against this code**, and staging has no Stripe keys. **The
customer-facing bonus and refund wording has had no legal review** (O5).

## What shipped

Both images were built from `792b4ea` (merge of #134) by publish-image.yml
runs 42676931458 (compute-api) and 42676937571 (site), tag
`bonus-refund-requests-2026-11-09`, both arm64. CI on `792b4ea`: run
42676359892, green.

compute-api `b7474c94…` (`322f705`) → `15cf14fb…` (`792b4ea`).
`4aa4aa7..792b4ea`:

- `ea992f4`: ADR-0013. **Money is in it** (read as a diff, not a summary):
  - `payments.BonusTiers`: USD 100 → 5%, USD 50 → 3%, rounded down to the
    cent. The bonus is fixed in `topups.bonus_cents` when the Checkout session
    is created. Its line item becomes "Unify Compute credit, includes USD X
    bonus"; the amount charged is unchanged. The webhook credits it as
    `topup_bonus` in the same transaction as the top-up
    (idempotency `topup_bonus:<id>`).
  - Refunds and disputes take the bonus back pro rata on running totals
    (`bonusShare`, all of it once nothing is left), and a won dispute
    restores what that dispute took. This may take the balance below zero,
    as refunds already can.
  - Refund requests: `POST /dashboard/v1/topups/{id}/refund-request`
    (owner/admin, 5 per account per 24 h), `DELETE /refund-requests/{id}`,
    and `admin refund-request list|decline`. A refund request moves no
    money. Any refund of the top-up (B2's `admin topup refund` → Stripe →
    `charge.refunded`) closes it. Each request emails the operator
    (`OPERATOR_EMAIL`, else `contactus@felixsphere.com`).
  - Migration `0015_bonus_and_refund_requests` is additive. It adds the
    `topup_bonus` kind to `balance_entries_kind_check`, `topups.bonus_cents`
    and `bonus_reversed_cents` (default 0), `topup_disputes.bonus_cents`,
    and the table `refund_requests` (at most one open per top-up). It is
    applied by `store.Migrate` at boot: each file in its own transaction,
    under an advisory lock, recorded in `schema_migrations`.
- `11f6a81`: tests only (mail goldens moved, browser-check expects the Bonus
  column).

`skus.json`, `platform.json`, env vars and Terraform are unchanged.

Site `ed5de01a…` → `a8281c78…` (`792b4ea`): `billing.html` /
`pages/billing.ts` gain a Bonus column, a Refund column (request, withdraw,
declined), the bonus tier note, a credit preview and the request dialog,
with strings in all ten catalogues.

## Before / after

- Before (status run 42676972208): `b7474c94…` / `ed5de01a…`, matching
  the 2026-11-09 sglang-templates record. IMDS lockdown present. Schema at
  `0014_low_balance`, 0 top-ups.
- Backup `/data/backups/20261109T185352Z-42678010483` (compute 29 tables,
  console 59), copied to
  `s3://unify-compute-staging-backups-871376691052/staging/releases/20261109T185352Z-42678010483/`.
- After `up -d` and after 30 s, all six containers had 0 restarts. Smoke
  passed in full, and `/healthz` reports build `792b4ea…`.
- `0015_bonus_and_refund_requests` was applied at 18:53:56Z.
  PREVIOUS_RELEASE is `b7474c94…` / `ed5de01a…`.

## What was exercised

Everything below ran against `app.staging.compute.unifyai.us`. The
signed-in session is a new throwaway, **account 29** / user 29
(`success+cicd-bonus-1109@simulator.amazonses.com`, owner). It signed up
through `POST /dashboard/v1/signup` (202). Its verify code was recovered
offline from the `email_codes` hash (read-only over SSM). The browser was
headless Chrome over CDP from the dev Mac.

1. **Schema and admin CLI (pass).** `schema_migrations` newest is
   `0015_bonus_and_refund_requests`. `topups.bonus_cents`,
   `bonus_reversed_cents` and `topup_disputes.bonus_cents` exist, and
   `refund_requests` has 0 rows. `compute-api admin refund-request list`
   inside the container printed `no open refund requests`, exit 0.
2. **Billing with top-ups disabled, no top-ups (pass).** `GET /topups`
   answers `topups_enabled: false`, `data: []` and both `bonus_tiers`. On
   `/billing`, Chrome logged no console errors or exceptions. The top-ups
   section, `#bonus-note`, `#topup` and "Add credit" are hidden, there are no
   refund buttons, and the "Adding credit / Contact us" card shows. No
   horizontal scroll at 1280.
3. **Refund request on a seeded top-up (pass), staging only.** Under the ops
   lock, I **inserted** two `topups` rows for account 29, with no balance
   entries:
   - `tu_01M70000000000000000SEED01`: credited, 5000 cents, bonus 150,
     session `cs_test_cicd_seed_bonus_1109_01`.
   - `tu_01M70000000000000000SEED02`: expired, 1000 cents, bonus 0,
     session `cs_test_cicd_seed_bonus_1109_02`.

   Then:
   - `GET /topups`: SEED01 `bonus_usd 1.50`, `refundable_usd 50.00`; SEED02
     `0.00` / `0.00`. The page's table (Date, Amount, Bonus, Status,
     Receipt, Refund) read `50.00 | 1.50 | Added | — | Request refund` and
     `10.00 | — | Not completed | — | —`, one refund button in total, and
     the refund note shown. No console errors.
   - POST `amount_usd 50.01` → `400 invalid_amount` ("at most USD 50.00").
     POST on SEED02 → `409 topup_not_refundable`. POST
     `amount_usd 20.00` → `201`, `rr_01M70TTE77GWN456T1QF1WXRZH` open.
     Repeating it → `409 refund_request_open`.
   - The page showed "USD 20.00 requested · Withdraw"; in zh-Hans at 500 px
     (headless Chrome's minimum width) it showed "已申请 USD 20.00 · 撤回",
     without horizontal scroll. No console errors in either.
     `admin refund-request list` showed the request. The operator notice
     (`ops_refund_requested`) was sent to `contactus@felixsphere.com`. Its
     reason reads "CI/CD staging test of #134 on a seeded top-up (no
     money). Ignore."
   - `DELETE /refund-requests/rr_01M70TTE…` → 204. Again → `409
     refund_request_closed`. An unknown id → 404. The top-up then carried
     the request as `cancelled`, the page showed "Request refund" again,
     and `admin refund-request list` was empty.
   - **Removed** under the ops lock: both `topups` rows, the
     `refund_requests` row and its `payment_emails` row. Afterwards:
     `topups` 0, `refund_requests` 0, balance entries for account 29: 0.
     **Kept**: account 29 (compute off, balance 0, no keys, session logged
     out) and its audit rows 112–115 (`signup`, `verify`,
     `refund_requested`, `refund_request_cancelled`).

## What this does not prove

- **No payment, refund or dispute ran against Stripe, in any mode.** Nothing
  checks against Stripe that it accepts the new line-item name, that the
  webhook credits `topup_bonus`, or that a refund reverses the bonus. The
  unit tests run these paths against `stripetest`, not Stripe.
- `admin refund-request decline` and the refund-declined email were not
  run (the request was withdrawn instead). The refund-issued email with a
  bonus line was not sent.
- The customer wording (bonus has no cash value, reduced pro rata) has not
  had legal review (O5).
- Translations were checked for presence and layout, not read by a speaker.
  Nit: the zh-Hans Bonus header is "赠送 (USD)" (ASCII parentheses, with a
  space) next to "金额（USD）".
