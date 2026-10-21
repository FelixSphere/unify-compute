# Accounts, keys, balance and top-ups

- **Status:** Spec, partly built. Decided by
  [ADR-0011](../adr/0011-compute-owns-accounts-and-billing.md) (**Accepted**).
  Until the cutover in that ADR, compute-api authenticates through the console
  bridge ([`console-billing-bridge.md`](console-billing-bridge.md)). On `main`
  but not deployed anywhere: the account tables and `internal/accounts` (A1),
  the mailer (A2), the public and session auth routes of the dashboard API
  (`internal/dashboard`, A4), and `balance_entries`, the local biller
  (`internal/billing`, migration `0006_billing.sql`) and the admin CLI's
  `account`, `balance` and `audit` verbs (A6), and API keys (A5: migration
  `0007_api_keys.sql`, the dashboard's `/keys` routes, and the job API's
  `uck_` verifier with scopes), and the dashboard's balance, history, usage
  and job routes (A7d), and card top-ups (B1: migration `0008_topups.sql`,
  `internal/payments`, the dashboard's `/topups` routes and the Stripe
  webhook, the Billing page's "Add credit"). All of it is behind
  `ACCOUNTS_BACKEND=local`, which no environment sets, and top-ups also need
  the Stripe keys, which no environment has yet. Refunds and disputes (B2:
  migration `0012_refunds_disputes.sql`, `internal/payments/refunds.go`,
  `admin topup refund`, the refund, suspension and operator emails) are on
  the branch `feat/refunds-and-disputes`, tested against the Stripe fake
  only. Bonus credit on top-ups and customers' refund requests
  ([ADR-0013](../adr/0013-topup-bonus-and-refund-requests.md), migration
  `0015_bonus_and_refund_requests.sql`) are on the branch
  `feat/topup-bonus-refund-requests`, tested against the Stripe fake only.
  `admin account limits`, `GET /compute/v1/balance`, account export and close
  do not exist yet.
- Task breakdown: [`plan-own-accounts.md`](../plan-own-accounts.md).
- Depends on: [`job-api.md`](job-api.md) (error envelope, job routes),
  [`metering.md`](metering.md) (holds H1–H7, settlement R1–R6, the quota unit),
  [`notifications.md`](notifications.md) (job emails), `email/base.html` (the
  shared layout), `~/unifyai-brand-kit/UI-STANDARD.md` (the pages).

## Hosts and surfaces

| Surface | Host | Path | Credential |
|---|---|---|---|
| Dashboard pages (static) | `app.compute.unifyai.us` | everything except `/dashboard/v1/*` | none; served by the `site` container |
| Dashboard API | `app.compute.unifyai.us` | `/dashboard/v1/*` | session cookie + CSRF header |
| Job API | `api.compute.unifyai.us` | `/compute/v1/*` | `Authorization: Bearer uck_…` |
| Stripe webhook | `app.compute.unifyai.us` | `/dashboard/v1/stripe/webhook` | `Stripe-Signature` (no session, no Origin check) |
| Admin | the host, over SSM | `compute-api admin …` (CLI) | host access |

Staging uses `app.staging.compute.unifyai.us` and `staging.compute.unifyai.us`
in the same shape.

compute-api answers `/dashboard/v1/*` only when `Host` is the app host
(`404` otherwise), and refuses a non-GET whose `Origin` is not
`https://<app host>` with `403 csrf_failed`. A session cookie is never
accepted on `/compute/v1/*`, and an API key never on `/dashboard/v1/*`.

## Units

- Money crosses every API as a **decimal string in USD**: 4 places for
  balances and charges (`"12.5000"`), 2 places for top-up amounts (`"20.00"`),
  as `job-api.md` does. Never a float.
- Internally amounts are **quota units, 500000 per USD 1** (`metering.md`).
  One cent is exactly 5000 units. The unit never appears in an API response.
- Timestamps are Unix seconds. Ids are a prefix plus a ULID unless stated.

## Data model

Postgres, compute's own database, one new migration per task that adds
tables. Like `0002_ledger.sql`, every statement is re-runnable. The tables
marked **append-only** get the same refusing trigger as `usage_events`.

### `accounts` (the billing entity)

| Column | Type | Notes |
|---|---|---|
| `id` | `bigint` identity PK | Fits the existing `holds.user_id` / `jobs.owner` scheme; owner string is `account:<id>` |
| `name` | `text` | 1–64 chars, how the tenant appears in the dashboard and in member emails. Defaults to the first user's email at signup; owners and admins rename it (`PATCH /account`) |
| `status` | `text` | `active`, `suspended`, `closed` |
| `compute_enabled` | `boolean` | Default from `ACCOUNTS_COMPUTE_DEFAULT` (`false` while starting small; ADR-0011 D3) |
| `available` | `bigint` | Quota units. May be negative: a dispute or a refund larger than the unused balance drives it below zero (0005's `CHECK (available >= 0 OR status = 'suspended')` was dropped by 0012, because a refund made at Stripe has already returned the money and does not suspend). A negative balance refuses every hold |
| `held` | `bigint` | Quota units, `>= 0` |
| `topup_min_cents`, `topup_max_cents`, `balance_max_cents`, `topups_per_day` | `integer` | Defaults 1000, 10000, 20000, 3 |
| `stripe_customer_id` | `text` unique, null | Created on first top-up |
| `low_balance_quota` | `bigint` | Quota units, `>= 0`, default 2 500 000 (USD 5, operator decision 2026-10-14); `0` is off. Migration 0014. See [Low-balance warning](#low-balance-warning) |
| `low_balance_armed` | `boolean` | The next drop of `available` below `low_balance_quota` is emailed. Migration 0014 |
| `suspended_reason` | `text` null | `dispute`, `abuse`, `operator` |
| `created_at`, `closed_at` | `timestamptz` | |

### `users` (a login)

| Column | Type | Notes |
|---|---|---|
| `id` | `bigint` identity PK | |
| `account_id` | `bigint` FK → accounts | The account is the tenant: one or more users, each with one email (ADR-0011 D9). The user who signs up is its first `owner`; the rest arrive by invitation |
| `role` | `text` | `owner`, `admin`, `member`; see [Members and roles](#members-and-roles) |
| `email` | `text` | Stored NFC-normalised, trimmed, lower-cased; unique. Replaced by `closed+<id>@invalid` at closing |
| `password_hash` | `text` | argon2id PHC string |
| `status` | `text` | `pending`, `active`, `disabled`, `closed` |
| `email_verified_at`, `last_login_at`, `password_changed_at`, `created_at` | `timestamptz` | |

### `invitations`

| Column | Type | Notes |
|---|---|---|
| `id` | `text` PK | `inv_` + ULID |
| `account_id` | `bigint` FK → accounts | |
| `email` | `text` | Normalised like `users.email`. At most one live invitation per `(account_id, email)`; a new one supersedes the old |
| `role` | `text` | `admin` or `member`; `owner` is granted by `PATCH /members/{id}`, never by invitation |
| `token_digest` | `bytea` unique | SHA-256 of a 32-byte random token; the token travels only in the email link's fragment |
| `invited_by` | `bigint` FK → users | |
| `created_at`, `expires_at` (7 days), `accepted_at`, `revoked_at`, `superseded_at` | `timestamptz` | |

### `sessions`

`id_hash bytea PK` (SHA-256 of the cookie value), `user_id`, `csrf_token`
(random, returned to the page; not secret from it), `created_at`,
`last_seen_at`, `reauth_at` (last time the password was entered),
`expires_at` (absolute), `ip`, `user_agent`. Idle timeout 12 h on
`last_seen_at`, written at most once a minute. Expired rows are purged hourly.

### `email_codes` (verification)

`id`, `user_id`, `purpose` (`verify`), `code_hash` (SHA-256 of the 6 digits
salted with the row id), `attempts` (≤ 5), `expires_at` (15 min), `used_at`,
`superseded_at`. At most one live code per user and purpose; issuing a new one
supersedes the old.

### `auth_tokens` (reset links)

`digest PK` (SHA-256 of a 32-byte random token), `user_id`, `purpose`
(`reset`), `expires_at` (1 h), `used_at`, `superseded_at`.

### `auth_attempts` (throttling)

`key text PK` (`bucket:subject`), `count`, `window_start`. Same semantics as
Unify Data's `attempts`: the window opens at the first attempt and is not
extended by later ones, and each row expires against its own window.

### `api_keys`

| Column | Type | Notes |
|---|---|---|
| `id` | `bigint` identity PK | Fits `jobs.token_id`, `holds.token_id`, `webhooks.token_id` |
| `account_id` | `bigint` FK | |
| `name` | `text` | 1–64 chars |
| `display` | `text` | `uck_` + first 8 chars of the secret, e.g. `uck_AbCd1234` |
| `secret_hash` | `bytea` unique | SHA-256 of the whole key |
| `scopes` | `text[]` | subset of `{jobs, read}`, non-empty |
| `created_by` | `bigint` FK → users | |
| `created_at`, `expires_at` (null = never), `last_used_at`, `revoked_at` | `timestamptz` | |
| `monthly_cap` | `bigint` null | Spending cap per UTC calendar month, quota units; null is no cap. The local biller refuses a hold that would take the key's month (open holds at full quota, closed at what they settled) past it: `403 key_cap_exceeded` on submit. Checked under the account lock, so two submits cannot both fit. The console's per-token quota, at the key grain |

Key format: `uck_` followed by 40 characters from `[0-9A-Za-z]`, from
`crypto/rand`. Any other shape is `401 missing_key` without a database read.

### `balance_entries` (**append-only**)

| Column | Type | Notes |
|---|---|---|
| `id` | `text` PK | `be_` + ULID |
| `seq` | `bigint` identity unique | Write order |
| `account_id` | `bigint` | |
| `kind` | `text` | `topup`, `topup_bonus`, `hold`, `extend`, `settle`, `release`, `refund`, `dispute`, `dispute_reversal`, `admin_credit`, `admin_debit`, `writeoff`. `topup_bonus` is a top-up's bonus credit (`topup_bonus:tu_…`), the share a refund or dispute takes back (negative, `topup_bonus_reversal:…`), or what a won dispute restores (`topup_bonus_restore:dp_…`) |
| `available_delta`, `held_delta` | `bigint` | Signed quota units. A `hold` is (−q, +q); `settle` (0, −q); `release` (+q, −q); `topup` (+q, 0) |
| `available_after`, `held_after` | `bigint` | Snapshot, so a balance can be audited from the entries alone |
| `ref_type`, `ref_id` | `text` | `topup`/`tu_…`, `hold`/`hold_…`, `job`/`job_…`, `admin`/audit id |
| `idempotency_key` | `text` unique | Derived from the triggering id (`topup:tu_…`, `settle:ue_…`, `extend:ext_…`, …) |
| `detail` | `jsonb` | e.g. `{ "sku": "a10g-24gb-x1", "gpu_seconds": 1290 }` for a settle |
| `created_at` | `timestamptz` | |

The `accounts` balance columns and the entry are written in one transaction
with `SELECT … FOR UPDATE` on the account row. Invariant, checked by a test
and by `compute-api admin audit`: for every account, the sums of the deltas
equal `available` and `held`.

### Local holds (the biller)

The same three tables the bridge spec gives the console, now ours, so the
ledger sees identical behaviour:

- `billing_holds`: `hold_id PK`, `account_id`, `key_id`, `job_id` (null for
  R5 charges), `sku` (or `storage`/`egress`), `quota`, `settled_quota`,
  `released_quota` (what went back when it closed, reported again by every
  repeated release), `status` (`open`, `settled` when a final settle closed
  it, `released`, `expired`), `expires_at` (null never expires),
  `request` and `response` (`jsonb`, the original body and answer, for
  idempotency), `created_at`, `closed_at`.
- `billing_settlements`: `event_id PK`, `seq`, `hold_id`, `quota_delta`,
  `gpu_seconds`, `final`, `response`. One row per accepted settle, including
  one that moved nothing, so a replayed `event_id` answers what it answered.
- `billing_extensions`: `extend_id PK`, `hold_id`, `additional_quota`.

`internal/billing.Local` implements the bridge's four verbs with the console
types, and answers each refusal with a `*console.Error` carrying the bridge's
status and code, so the ledger (which depends on a `ledger.Biller` interface,
not on the console client) cannot tell the two apart. `ACCOUNTS_BACKEND`
chooses which one `main` wires. The bridge's user id is the account id; there
is no per-key spending limit (open point 3), so `key_id` is recorded, not
checked.

Each verb writes its rows and its `balance_entries` row in one transaction,
under `SELECT … FOR UPDATE` on the account row and then the hold row. Every
verb first expires the touched account's holds past `expires_at` (strictly
after, in whole seconds, as the console does), releasing their remainder,
and that expiry commits even when the verb then refuses. An hourly sweep
covers accounts nothing touches. Afterwards the hold answers
`409 hold_expired`. One difference from the console remains: the console
swept every hold before every request, so a call naming an unknown hold also
expired other accounts' holds; locally that waits for the account's next
call or the sweep. No answer differs.

Tests: the ledger's harness tests and the restart tests run a second time
with the local biller behind every console fake (`consoletest.SetBookFactory`,
`billingtest.UseLocalBooks`), test files unchanged,
and the fake's hold contract runs against the fake, the local biller and the
local biller over HTTP, plus a randomised sequence compared step by step
with the fake (`internal/billing`).

The existing `holds` table (the ledger's own view of a hold) is unchanged.
After the cutover its `user_id` holds the account id and `token_id` the key id.

### `topups`

| Column | Type | Notes |
|---|---|---|
| `id` | `text` PK | `tu_` + ULID; Checkout `client_reference_id`, `metadata.topup_id` and the Checkout idempotency key |
| `account_id`, `user_id` | `bigint` | No foreign key, like `balance_entries` |
| `amount_cents` | `integer` | `currency` is always `usd` |
| `status` | `text` | See [Top-up](#top-up). At most one `open` per account (partial unique index) |
| `stripe_checkout_session_id` | `text` unique | Null only between the insert and Stripe's answer; a top-up whose session could not be created is deleted |
| `stripe_customer_id` | `text` | The account's Customer (also on `accounts`) |
| `stripe_payment_intent_id`, `stripe_charge_id` | `text` | The PaymentIntent from the completed session; the charge and receipt looked up after the credit |
| `receipt_url` | `text` | Stripe's hosted receipt |
| `refunded_cents`, `disputed_cents` | `integer` | Refunded so far (the charge's cumulative `amount_refunded` last applied) and the amount under open disputes (B2) |
| `bonus_cents`, `bonus_reversed_cents` | `integer` | The bonus credit fixed at creation (`payments.BonusFor`: 3% from USD 50, 5% at USD 100; ADR-0013), and how much of it refunds and open disputes have taken back |
| `created_at`, `paid_at`, `expires_at` | `timestamptz` | `expires_at` = Checkout's, one hour after creation (`payments.CheckoutTTL`) |

### `stripe_events`

`event_id PK`, `type`, `livemode`, `topup_id`, `outcome`, `payload_sha256`,
`received_at`, `processed_at`. Inserted first in the same transaction as the
effect, so a redelivered event, even one delivered concurrently, is a no-op.
Only compute's own events are stored; see [Stripe webhook](#stripe-webhook-post-dashboardv1stripewebhook).

### `topup_disputes`

`id` (Stripe's `dp_…`) PK, `topup_id`, `account_id`, `amount_cents`,
`status` (`open` → `won` | `lost`; Stripe's `warning_closed` is recorded as
`won`), `reason`, `suspended_account` (whether this dispute moved the
account from `active` to `suspended`), `opened_event_id`,
`closed_event_id`, `created_at`, `closed_at`. Keyed on the dispute, so a
dispute is debited and reinstated at most once whatever order its events
arrive in. `bonus_cents` is the share of the top-up's bonus this dispute
took back, which a won dispute restores (ADR-0013).

### `refund_requests`

A customer's request (ADR-0013 §3): `id` (`rr_` + ULID), `account_id`,
`user_id`, `topup_id`, `amount_cents`, `reason` (at most 500 characters),
`status` (`open` → `refunded` | `declined` | `cancelled`), `resolution` (the
operator's message on a decline), `refund_event` (the Stripe event whose
refund closed it), `created_at`, `resolved_at`. At most one `open` per
top-up (partial unique index).

### `payment_emails`

`(event_id, kind, recipient)` PK, `account_id`, `detail jsonb`,
`created_at`, `claimed_at`, `sent_at`. The messages a Stripe event causes,
inserted in the event's transaction and sent after it commits; the Stripe
event id is the idempotency key. A send claims the row first (a claim older
than 15 minutes is taken as dead) and marks it sent after, so each message
goes out at most once; a failed one is unclaimed and retried when Stripe
delivers the event again. It is separate from W2's `notifications`
(`internal/notify`) because these messages are rendered from what the
event's transaction saw and retried on redelivery, when that data is no
longer at hand; notify retries only from a later trigger that brings its
own data. Folding them together means storing the message detail in
`notifications`.

### `audit_events` (**append-only**)

`id` (`ae_` + ULID), `seq`, `at`, `actor_type` (`user`, `admin`, `system`,
`stripe`), `actor_id`, `account_id`, `action`, `ip`, `user_agent`, `reason`
(required for `admin`), `detail jsonb`. Actions: `signup`, `verify`,
`login`, `login_failed`, `logout`, `sessions_ended`, `reset_requested`, `password_reset`,
`password_changed`, `key_created`, `key_revoked`, `invited`,
`invite_accepted`, `invite_revoked`, `member_removed`, `role_changed`,
`account_renamed`, `topup_created`,
`topup_credited`, `refund`, `refund_requested`, `refund_request_cancelled`,
`refund_request_declined`, `dispute_opened`, `dispute_closed`,
`account_suspended`, `account_reinstated`, `compute_enabled`,
`limits_changed`, `admin_credit`, `admin_debit`, `export`, `account_closed`,
`low_balance_changed` (detail `low_balance_usd`).

### Changes to existing tables

None in shape. `jobs.owner` takes `account:<id>`. `quota.Config` must accept
that prefix (`internal/quota/config.go` accepts only `user:` / `tenant:`
today), so `quotas.json` overrides can name an account.

## State machines

### User

```
           signup                    correct code
(none) ───────────▶ pending ───────────────────────▶ active ◀──────┐
                       │                               │  ▲          │ operator
                       │ 7 days unverified              │  │ operator │ reinstates
                       ▼                               ▼  │          │
                    (purged)                         disabled ──────┘
                                                       │
                     active / disabled ── close ──▶ closed (terminal)
```

- `pending` can request a new code and cannot log in (`403 email_unverified`
  after a correct password). A reset link also works for it and verifies the
  address, because following the link proves control of it.
- A `pending` user and its account are deleted 7 days after signup if never
  verified, freeing the address.
- Account `status` (`active`/`suspended`/`closed`) is separate: a suspended
  account's user can log in, see the balance and export data, and cannot
  submit, top up or create keys.

### Email verification code

```
issued ──correct──▶ used
  │ ├──5 wrong──▶ exhausted
  │ ├──15 min───▶ expired
  └──resend─────▶ superseded (a new code is issued)
```

Only `issued` verifies. Every refusal is the same `400 invalid_code`, so the
response does not say which state the code is in.

### Reset token

`issued → used | expired | superseded` (a newer request supersedes). Using it
sets the password, marks the email verified if it was not (the link proves
control of the address), and deletes every session of the user.

### API key

`active → revoked` (by the user or at account closing) and
`active → expired` (at `expires_at`). Both are terminal. A revoked or expired
key is `401 invalid_key`, with no cache.

### Top-up

```
          create                webhook: completed + paid, or async_payment_succeeded
(none) ──────────▶ open ─────────────────────────────▶ credited ──refund──▶ refunded / partially_refunded
                    │                                    │
                    ├──webhook: expired (1 h)──▶ expired │ dispute.created
                    ├──a new top-up expires it──▶ expired ▼
                    └──webhook: async_payment_failed──▶ failed
                                                     disputed ──closed: won──▶ credited
                                                         └──────closed: lost──▶ lost (terminal)
```

- `credited` is written only by the webhook, never by the return page.
- Creating a top-up while one is `open` first expires that one's Checkout
  Session at Stripe (`POST /v1/checkout/sessions/{id}/expire`), so a payment
  page left open in another tab can no longer be paid, then marks it
  `expired`. If Stripe refuses because the session is already complete, the
  payment went through and its webhook will credit it: `409 topup_open`.
- `failed` cannot happen with cards only; it is handled because the event
  exists.
- **Refund policy** (operator, 2026-10-14; wording pending legal review):
  unused prepaid balance can be refunded on request; compute already used
  is not refunded.
- A refund is created by `admin topup refund`, which only calls Stripe
  (`POST /v1/refunds`, idempotency key on the top-up, what was refunded so
  far and the amount). It refuses more than is left on the top-up (amount −
  refunded − disputed) and more than the account's `available`. The balance
  moves only when `charge.refunded` arrives, the same path a refund made in
  the Stripe dashboard takes: the debit is the charge's cumulative
  `amount_refunded` minus `refunded_cents`, as a `refund` entry keyed
  `refund:<topup>:<cumulative cents>`, so a duplicate or stale event debits
  nothing. Top-up → `partially_refunded` or `refunded` (left `disputed` or
  `lost` if it is). A refund larger than `available` (made in the Stripe
  dashboard, or racing a settle) is debited in full: the balance goes
  negative, the operator is emailed, the account is **not** suspended and
  running jobs are not cancelled. The customer gets "Refund issued".
- `charge.dispute.created` debits the disputed amount at once (`dispute`
  entry, key `dispute:<dp_…>`), adds it to `disputed_cents`, moves the
  top-up to `disputed`, suspends an `active` account
  (`suspended_reason = dispute`), records a cancel for every live job of the
  account in the same transaction and then carries them out through the
  normal cancel path (`jobs.Service.Cancel`: metering stops at the
  dispute, the hold settles what was used), emails "Account suspended" to
  the account (only if this dispute suspended it) and a notice to the
  operator.
- `charge.dispute.closed` with `won` (or `warning_closed`) writes a
  `dispute_reversal` entry and returns the top-up to `credited` /
  `partially_refunded` / `refunded`. The suspension is lifted only if this
  dispute caused it, the account is still suspended for `dispute`, no other
  dispute on it is open, and `available >= 0`; otherwise it stays for the
  operator. `lost` writes nothing more, the top-up becomes `lost` and the
  account stays suspended until `admin account reinstate`. A close that
  arrives before its open is applied as open-then-close when lost, and only
  recorded when won (no money moved).
- An event that cannot be applied as it is (not USD, a charge amount that
  differs from the top-up, a disputed amount outside it) is stored with
  `outcome = recorded` and logged as an error: the money has moved at
  Stripe, so the operator reconciles it by hand.
- Not handled: a refund that later fails (`charge.refund.updated` with
  `status = failed`) stays debited; Stripe's `charge.dispute.funds_*`
  events are not used.
- **Bonus credit** (ADR-0013). The webhook that credits a top-up credits its
  `bonus_cents` too, as a `topup_bonus` entry in the same transaction. A
  refund or a dispute takes back the same share of it,
  `floor(bonus × (refunded + disputed) / amount)` on running totals (all of
  it once nothing of the payment is left), as a negative `topup_bonus`
  entry next to the `refund` or `dispute` entry; a won dispute restores what
  it took. `admin topup refund` refuses a refund whose debit, refund plus
  bonus share, does not fit in `available`, and names the largest that
  would. "Refund issued" names the bonus removed.
- An applied refund of a top-up closes its open refund request (below) as
  `refunded`, whatever amount was asked.

### Refund request

```
POST /topups/{id}/refund-request
(none) ──────────▶ open ──charge.refunded applied──▶ refunded
                    ├──admin refund-request decline──▶ declined (the message is emailed)
                    └──DELETE /refund-requests/{id}──▶ cancelled
```

Only a top-up with something refundable (`credited` or
`partially_refunded`, `refundable_usd` above zero) can be asked about, for
at most that amount, and only by owners and admins. Asking moves no money;
it queues an operator notice to `OPERATOR_EMAIL` in `payment_emails` under
the request id.

### Account

`active ⇄ suspended → closed`. Suspension is automatic on a dispute
(`suspended_reason = dispute`), otherwise an admin action (`operator`). A
won dispute lifts only the suspension it caused (above); every other
reinstatement is `admin account reinstate`, which refuses a negative
balance.

## Errors

The job API's envelope, unchanged (`job-api.md`, "Errors"):

```json
{ "error": { "type": "invalid_request", "code": "weak_password", "message": "Use a password of at least 12 characters.", "param": "password" } }
```

`message` is a sentence for a person. The dashboard shows it under the field
named by `param`, or in the form-level box when `param` is absent
(UI-STANDARD, "Errors").

| HTTP | `type` | `code` | When |
|---|---|---|---|
| 400 | `invalid_request` | `invalid_email` | Not an address (≤ 254 chars, one `@`, a dotted domain). Unify Data's `bad_email`, renamed to our `invalid_*` style |
| 400 | `invalid_request` | `weak_password` | Under 12 or over 128 chars, equal to the email or its local part, or fewer than 5 distinct characters |
| 400 | `invalid_request` | `password_mismatch` | `password` ≠ `password_confirm` (the page checks first; the server checks too) |
| 400 | `invalid_request` | `bot_check_failed` | The honeypot field (`website`) filled. While Turnstile is off (ADR-0011 D5) that is the only cause; with Turnstile, also a missing, invalid, expired or reused token |
| 400 | `invalid_request` | `invalid_code` | Verification code wrong, expired, exhausted or superseded (one code for all four) |
| 400 | `invalid_request` | `invalid_token` | Reset link invalid, used or expired |
| 400 | `invalid_request` | `invalid_name` | Key name empty after trimming, over 64 characters, or containing a control character, a Unicode format character (bidi override, zero-width) or `<` `>` |
| 400 | `invalid_request` | `invalid_scope` | Key scopes empty or unknown |
| 400 | `invalid_request` | `invalid_expires_at` | Key `expires_at` (Unix seconds) not in the future |
| 400 | `invalid_request` | `invalid_amount` | Top-up not a whole-cent USD amount (`param: amount_usd`) |
| 400 | `invalid_request` | `invalid_signature` | Stripe webhook: `Stripe-Signature` missing, malformed, wrong or stale. Nothing stored |
| 401 | `authentication_error` | `invalid_credentials` | Login: unknown email or wrong password (indistinguishable) |
| 401 | `authentication_error` | `session_required` | Dashboard route without a valid session |
| 401 | `authentication_error` | `reauth_required` | Sensitive action more than 10 min after the password was last entered |
| 401 | `authentication_error` | `missing_key` | Job API: no key, or not shaped `uck_` + 40 |
| 401 | `authentication_error` | `invalid_key` | Job API: unknown, revoked or expired key |
| 402 | `insufficient_balance` | `insufficient_balance` | Hold or extend refused (unchanged meaning, now local) |
| 403 | `authentication_error` | `email_unverified` | Login with the correct password, address not verified |
| 403 | `authentication_error` | `account_disabled` | User disabled (kept from the bridge era) |
| 403 | `authentication_error` | `account_suspended` | Account suspended: no submit, top-up or key creation |
| 403 | `permission_error` | `compute_not_enabled` | Submit while `compute_enabled` is off |
| 403 | `permission_error` | `scope_denied` | Key lacks the scope (`read` key on submit or cancel) |
| 403 | `permission_error` | `csrf_failed` | Missing or wrong `X-CSRF-Token`, or foreign `Origin` |
| 403 | `permission_error` | `topup_limit_exceeded` | Below the minimum, above the per-top-up maximum, would exceed the balance maximum (`param: amount_usd` for these three), or over the daily count (no `param`). `message` says which and invites contact for a review |
| 403 | `permission_error` | `key_limit_reached` | 10 active keys already |
| 403 | `permission_error` | `role_required` | The user's role does not allow the action ([Members and roles](#members-and-roles)): a member topping up, inviting, renaming, or revoking a key another user created; an admin inviting an admin or removing a non-member; a non-owner changing roles |
| 403 | `permission_error` | `member_limit_reached` | Members plus live invitations would exceed the account's `member_limit` |
| 400 | `invalid_request` | `invalid_role` | Invitation role not `admin`/`member`, or member role not `owner`/`admin`/`member` |
| 400 | `invalid_request` | `invalid_month` | `/usage`: `month` not `YYYY-MM` |
| 400 | `invalid_request` | `invalid_cursor`, `invalid_limit`, `invalid_status` | List routes: `after` not one of this account's entries or jobs, `limit` out of range, unknown job status (`param` names it) |
| 404 | `not_found` | `not_found` | Key or top-up not in this account |
| 404 | `not_found` | `job_not_found` | Job not in this account, on the dashboard as on the job API |
| 409 | `conflict` | `already_member` | Inviting an address that is already a user of this account (`param: email`) |
| 409 | `conflict` | `last_owner` | Demoting or removing the account's last active owner |
| 409 | `conflict` | `email_in_use` | Accepting an invitation for an address that already has a login, in this or another account |
| 409 | `conflict` | `topup_open` | The account's `open` top-up was already paid at Stripe and its webhook has not arrived; `error.topup_id` names it. An unpaid open top-up never blocks: it is expired and replaced |
| 429 | `rate_limited` | `rate_limited` | Any throttle; always with `Retry-After` |
| 502 | `payment_provider_error` | `stripe_unavailable` | Stripe did not create the Checkout Session (or the Customer, or did not answer the expiry of an open session); nothing was charged and no top-up is kept |
| 503 | `topups_unavailable` | `topups_unavailable` | No Stripe keys in this deployment, or keys refused for its environment (see Configuration). Listing still works |
| 503 | `email_unavailable` | `email_unavailable` | SES refused a verification email at signup; the signup is rolled back (Unify Data's behaviour). Never returned by forgot-password (that answers `202` either way) |

`console_unavailable` disappears with the bridge. `billing_unavailable`
stays as a job `error.code` for a database failure during settlement.

## Dashboard API (`/dashboard/v1`)

All bodies JSON. Unauthenticated routes need no CSRF header but do need a
same-origin `Origin`. Every route that sends email or creates something is
throttled (limits in ADR-0011, section 8). The built routes are documented,
with every status and error code, in
[`dashboard-openapi.json`](dashboard-openapi.json), which a test holds against
the registered routes. A malformed body is `400 malformed_body`, as in the job
API.

While Turnstile is off (ADR-0011 D5) there is no `turnstile_token`; a page may
still send it and it is ignored. The honeypot `website` is accepted on signup,
resend, login and forgot-password. Throttles, as built:

| Rule | Limit | Counts |
|---|---|---|
| Login per IP | 20 / 10 min | every attempt, success included: the limit bounds the argon2id work one client can cause, and a success costs the same hash |
| Login per address | 50 / 10 min | failures only (wrong password), keyed on the normalised email whether or not it is registered, so a `429` is no oracle. Wrong current passwords on `POST /password` and `/reauth` count here too |
| Signup per IP | 5 / h | well-formed requests (validation errors do not count) |
| Verify per IP | 20 / h | wrong codes only |
| Resend per address | 1 / min and 5 / h | every accepted request, unknown addresses included |
| Resend per IP | 20 / h | every request |
| Forgot per IP, per address | 5 / h each | every request, unknown addresses included |
| Key creation per account | 20 / h | every well-formed request after reauth, refusals for the key limit included: each creation sends an email |
| Top-up creation per account | 10 / h | every request with a well-formed amount, limit refusals included: bounds Checkout Sessions (and card attempts inside them), since abandoned top-ups do not count against the daily 3 |
| Reads per account | 600 / min | every `GET` of `/balance`, `/transactions`, `/usage` and `/jobs…`, as the job API's 600 reads a minute per key; in process memory, per replica. Cancel is not a read |

The client IP is the **last** `X-Forwarded-For` entry (the ALB appends it),
never the first. Resend and forgot-password look the address up and send
after the response, so neither the status nor the time depends on it.

### Public

| Route | Body | Success | Notes |
|---|---|---|---|
| `GET /config` | — | `200 { turnstile_site_key, min_password_length: 12, topup: { min_usd, max_usd } }` | What the signed-out pages need. `turnstile_site_key` is `null` while Turnstile is off |
| `POST /signup` | `{ email, password, password_confirm, website }` | `202 { status: "verification_sent" }` | Always `202` for a well-formed request. A new address gets a code; a registered one gets "someone tried to sign up". `website` is the honeypot |
| `POST /verify` | `{ email, code }` | `200 { user, account, csrf_token }` + `Set-Cookie` | Activates and logs in |
| `POST /verify/resend` | `{ email, website }` | `202 { status: "verification_sent" }` | Always `202`; sends only to a `pending` user; at most 1 a minute and 5 an hour per address |
| `POST /login` | `{ email, password, website }` | `200 { user, account, csrf_token }` + `Set-Cookie` | Rotates any existing session |
| `POST /password/forgot` | `{ email, website }` | `202 { status: "reset_requested" }` | Always `202` |
| `POST /password/reset` | `{ token, password, password_confirm }` | `200 { status: "password_reset" }`, cookie cleared | Ends every session; does not log in |
| `POST /invitations/lookup` | `{ token }` | `200 { account_name, email, role, invited_by_email, expires_at }` | What the accept page shows, without using the token. Expired, withdrawn, superseded, used or unknown is `404 not_found`. A POST body, not the `GET /invitations/{token}` first specified here: a token in a path lands in access logs and is enough to join the account. Built (A5b) |
| `POST /invitations/accept` | `{ token, password, password_confirm }` | `200 { user, account, csrf_token }` + `Set-Cookie` | Creates an `active` user (the link proves the address) and logs in. An address that already belongs to a user is `409 email_in_use`: a user belongs to exactly one account, and the person logs in instead. Counts against the signup throttles. Emails the inviter. Built (A5b) |

### Session (cookie `__Host-uc_session` + `X-CSRF-Token`)

| Route | Body | Success |
|---|---|---|
| `GET /me` | — | `{ user: { email, role, created_at }, account: { id, name, status, compute_enabled }, csrf_token }`. `role` and `name` built (A5b) |
| `POST /logout` | — | `204`, cookie cleared |
| `POST /password` | `{ current_password, password, password_confirm }` | `204`; other sessions ended. `current_password` is the recent-password check and counts as a reauth; wrong is `401 invalid_credentials` with `param: current_password` |
| `POST /reauth` | `{ password }` | `204`; sets `reauth_at` |
| `GET /sessions` | — | `{ other_sessions }`: the user's live sessions other than this one (not idle, not past the absolute lifetime). Read-limited. Built (W7) |
| `POST /sessions/end-others` | — | `200 { ended }`: every other session of the user is deleted and this one kept; `ended` counts the live ones. No reauth (it only removes access). Audited as `sessions_ended`. Built (W7) |
| `GET /balance` | — | `{ object: "compute.balance", currency: "USD", available_usd, held_usd, low_balance_usd, limits: { topup_min_usd, topup_max_usd, balance_max_usd, topups_per_day } }`, from the account row. `low_balance_usd` has 2 places, `"0.00"` when off. Built (A7d; `low_balance_usd` W6) |
| `PATCH /balance` | `{ low_balance_usd }` (dollars, two decimals, 0 to 10000) | `200`, the balance as `GET`; `400 invalid_low_balance` (`param: low_balance_usd`). No reauth: it controls a banner and an email, not spending. Re-arms the warning exactly when `available` is at or above the new value. Writes `low_balance_changed`. Built (W6) |
| `GET /transactions?after=&limit=&kind=` | — | `kind` repeatable, any of the entry kinds (`400 invalid_kind` otherwise); absent is every kind.  `{ object: "list", data, has_more, next? }`, balance entries newest first, `limit` 1–100 (default 20), `after` an entry id: `{ id, object: "compute.balance_entry", kind, amount_usd, available_delta_usd, held_delta_usd, available_after_usd, held_after_usd, job_id, ref, created_at, detail }`. `amount_usd` is what a `settle` took out of held (negative), and for every other kind the change in available. `ref` is `{ type: "job" \| "topup", id }` or `null`: a hold's id is replaced by its job, and an operator adjustment's audit id and reason are never returned. `detail` holds only `sku`. Built (A7d) |
| `GET /keys` | — | `[{ id, name, display, scopes, created_at, expires_at, last_used_at, revoked_at, monthly_cap_usd, spent_this_month_usd }]`, a bare array, newest first, revoked and expired keys included. Built (A5) |
| `POST /keys` | `{ name, scopes, expires_at?, monthly_cap_usd? }` | `201 { …key, secret: "uck_…" }`; the secret is never returned again. Needs reauth after 10 min (`401 reauth_required`, checked before the body). Emails "key created". Built (A5) |
| `PATCH /keys/{id}` | `{ monthly_cap_usd }` (dollars, two decimals, or null) | `200 { …key }`; needs reauth after 10 min; a revoked or foreign key is `404 not_found`; `400 invalid_cap` |
| `DELETE /keys/{id}` | — | `204`, also for a key already revoked; another account's key is `404 not_found`. Built (A5) |
| `GET /templates` | — | The job API's template list (`GET /compute/v1/templates`) for the session: platform templates plus approved account listings where served, each with `publisher`. Read-limited. Built (M3) |
| `GET /skus` | — | The job API's SKU catalogue, for the run form's SKU choice. Read-limited. Built (M3) |
| `POST /jobs` | job-api.md's submit body | The job API's submit for the account: same validation, template resolution, quota, hold and launch, same codes (`403 compute_not_enabled`, `402 insufficient_balance`, …). Needs the CSRF header. No key: no key cap, `token_id` null on the job, and the hold's key is 0, so `/usage` `by_key` does not list dashboard runs. Built (M3) |
| `GET /jobs?status=&after=&limit=` | — | The job API's list, owner-scoped to the account. Built (A7d) |
| `GET /jobs/{id}` | — | The Job object (`job-api.md`). Built (A7d) |
| `GET /jobs/{id}/events`, `GET /jobs/{id}/logs` | — | As the job API (`follow=true` is 501 in both). Built (A7d) |
| `POST /jobs/{id}/cancel` | — | As the job API: `202` with the Job, `409 job_terminal` once ended. Needs the CSRF header. Built (A7d) |
| `GET /jobs/{id}/artifacts` | — | As the job API: the listing and presigned GET URLs. Without it the job page could not offer downloads, because the page has a session and the job API takes only a key. Built (A7d) |
| `GET /usage?month=YYYY-MM` or `?from=YYYY-MM-DD&to=YYYY-MM-DD` | — | `{ object: "compute.usage", month? (calendar months only), from, to (inclusive dates), currency: "USD", jobs, gpu_seconds, gpu_usd, storage_usd, egress_usd, total_usd, by_sku: [{ sku, gpu_seconds, usd }], by_day: [{ date, gpu_seconds, usd }] (every day of the month, zeros included), by_key: [{ key: { id, name, display }, jobs, gpu_seconds, usd }] (largest first) }` for one UTC month (absent: the current one; malformed: `400 invalid_month`) or an inclusive date range of at most 366 days (`400 invalid_period` for month with from/to, a malformed date, to before from, or a longer span). Totals the settles accepted in the month by SKU, in quota units converted once, so `total_usd` is exactly what the month's settle entries took; `gpu_seconds` counts what each settle added. Usage accrued but not yet settled is not in it. Holds whose SKU is `storage` or `egress` (R5, not built) go to those meters, so both read `0.0000` today. Built (A7d) |
| `POST /topups` | `{ amount_usd: "20.00" }` | `201 { id, object: "compute.topup", status: "open", checkout_url }`. Checks the account is active and the limits, expires an open top-up's session (above), creates the Customer on the first top-up and the Checkout Session: mode `payment`, one USD line item, `payment_method_types[0]=card`, `saved_payment_method_options[payment_method_save]=disabled`, no `setup_future_usage`, `client_reference_id` and idempotency key from the top-up id, `metadata` and `payment_intent_data[metadata]` = `product: unify-compute`, `account_id`, `topup_id`, `environment`, return URLs `APP_ORIGIN/billing?topup=<id>&checkout=success` / `=cancelled`, expiring after 1 h. Writes `topup_created`. Built (B1) |
| `GET /topups` | — | `{ object: "list", data, has_more: false, topups_enabled, bonus_tiers }`: the 50 newest, newest first, each `{ id, object: "compute.topup", amount_usd, bonus_usd, status, created_at, paid_at, receipt_url, refunded_usd, refundable_usd, refund_request }` (`refund_request`: the open one, else the newest, else null). `topups_enabled` says whether `POST` can work here, so the page offers the form only then; `bonus_tiers` (`[{ min_usd, percent }]`, highest first) lets it show the credit before payment. Built (B1, ADR-0013) |
| `GET /topups/{id}` | — | One top-up, as listed; another account's is `404 not_found`. What `/billing` polls after the return from Checkout. Built (B1) |
| `POST /topups/{id}/refund-request` | `{ amount_usd?, reason? }` | `201 { id, object: "compute.refund_request", topup_id, amount_usd, status: "open", resolution, created_at, resolved_at }`. See [Refund request](#refund-request). Owners and admins (`403 role_required`). `amount_usd` defaults to all of `refundable_usd`. Errors: `400 invalid_amount`, `400 invalid_reason`, `404 not_found`, `409 refund_request_open`, `409 topup_not_refundable`, `429` (5 per account per 24 h). Writes `refund_requested`. ADR-0013 |
| `DELETE /refund-requests/{id}` | — | `204`; the account's own open request, owners and admins (`404 not_found`, `409 refund_request_closed`). Writes `refund_request_cancelled`. ADR-0013 |
| `POST /stripe/webhook` | Stripe event | Not for the pages: see [Stripe webhook](#stripe-webhook-post-dashboardv1stripewebhook). Built (B1) |
| `GET /usage/jobs?month=|from=&to=&sku=&key=&after=&limit=` | — | The per-job view of the same settles: `{ object: "list", data: [{ object: "compute.job_usage", job_id, sku, status, key: { id, name, display }, started_at, finished_at, settled_at, gpu_seconds, usd }], has_more, next? }`, newest settle first, keyset on `(settled_at, job_id)`, `limit` 1–100 (default 20). `sku` and `key` (an API key id) filter; the page's `usd` values sum to `/usage`'s `gpu_usd` when unfiltered. Errors: `invalid_month`, `invalid_key`, `invalid_limit`, `invalid_cursor`. The console's per-request log, at the job grain |
| `GET /usage/export?month=|from=&to=` | — | The same rows as `/usage/jobs`, unfiltered, as a CSV attachment named `unify-compute-usage-<from>-<to>.csv` with FOCUS 1.0 column names (`BillingAccountId`, `BillingPeriodStart/End`, `ChargePeriodStart/End`, `ChargeCategory`, `ServiceName`, `ResourceId`, `SkuId`, `ConsumedQuantity`, `ConsumedUnit`, `BilledCost`, `EffectiveCost`, …) plus `x_ApiKeyId`, `x_ApiKeyName`, `x_JobStatus`. At most 10 000 rows. Product-design C10 |
| `PATCH /account` | `{ name }` | `200 { account }`; owner or admin; needs reauth after 10 min; `400 invalid_name` for 0 or over 64 characters, a control, format or angle-bracket character. Built (A5b) |
| `GET /members` | — | `[{ user_id, email, role, status, created_at, last_login_at }]`, oldest first, closed users left out; any role may read. Built (A5b) |
| `GET /members/invitations` | — | `[{ id, email, role, status, invited_by_email, created_at, expires_at }]`, newest first: invitations not accepted, withdrawn or superseded, live (`status: pending`) or expired within the last 30 days (`status: expired`, kept so the page can offer "Send again"); owner or admin. Built (A5b) |
| `POST /members/invitations` | `{ email, role }` | `201 { id, email, role, expires_at }`; owner or admin; `role` is `admin` or `member`. Needs reauth after 10 min. Emails the invitation. An address that is already a member is `409 already_member`; the 21st member (live invitations counted; the account's `member_limit`, 20 by default) is `403 member_limit_reached`. An admin may invite only `member` (`403 role_required`). A new invitation for an address supersedes its open one. 20 invitations, resends included, per account per 24 h. The email is sent before the answer: if SES refuses it the invitation is kept and the answer is `503 email_unavailable`. Built (A5b) |
| `POST /members/invitations/{id}/resend` | — | `201 { …invitation }` with a new id: "Send again". The open invitation, live or expired, is superseded and a new one for the same address and role, with a new link valid 7 days, is emailed. Same roles, reauth, cap and throttle as inviting. Built (A5b) |
| `DELETE /members/invitations/{id}` | — | `204`; owner or admin (an admin withdraws member invitations only); live or expired. No reauth. Built (A5b) |
| `PATCH /members/{user_id}` | `{ role }` | `200`; owner only. Demoting the last active `owner` is `409 last_owner`. Needs reauth after 10 min. Built (A5b) |
| `DELETE /members/{user_id}` | — | `204`; owner removes anyone, admin removes members, anyone removes themself. Removing the last `owner` is `409 last_owner`. The user's sessions end and the user is `closed` with its address tombstoned; keys it created stay, because they belong to the account. Emails the removed address. Needs reauth after 10 min. Built (A5b) |
| `GET /listings` | — | `{ object: "list", data: [listing…], open }`: the account's marketplace listings (`marketplace.md` M2), oldest first |
| `POST /listings` | `{ template }` (one templates.json entry) | `201` pending listing; needs `compute_enabled`; `400 invalid_listing` / `listing_id_taken`, `409 listing_exists`; 20 writes an hour per account |
| `PATCH /listings/{id}` | `{ template }` | `200`; relisted and pending again; `400 listing_id_fixed` if the id changes |
| `POST /listings/{id}/unlist` | — | `200`; never refused for the account's own listing |
| `GET /account/export` | — | `200` JSON attachment: account, members (metadata), keys (metadata), jobs, balance entries, top-ups; owner or admin |
| `POST /account/close` | `{ password }` | `202`; owner only. Jobs cancelled, keys revoked, artifacts deleted, every member's sessions ended and every address tombstoned |

Submitting jobs from the dashboard is not in the first version: customers
submit with a key.

The pages (A7c, `dashboard/`) call every route above except export and
close, and every one they call is built. `/billing` sends the browser to
`checkout_url` with a full navigation (no Stripe script on our pages, so the
CSP is unchanged and PCI scope stays SAQ-A), and on the return cleans the
URL, says "cancelled" or polls `GET /topups/{id}` until the webhook has
moved it out of `open`. The job routes are
the job API's own handlers (`api.Server.AccountJobs`) run for the session's
account, so bodies, pagination and errors are the job API's; another
account's job answers `404 job_not_found` exactly as a missing one does.
A deployment without a job service or ledger answers those routes `501
not_implemented` (main always wires both with `ACCOUNTS_BACKEND=local`). A
page section still says what will appear there when its route answers `404
not_found` or `501`, so the pages also work against an older compute-api.

## Members and roles

The account is the tenant (ADR-0011 D9). Everything that costs or earns
money belongs to it, never to a user: the balance, top-ups, API keys, jobs,
artifacts, quotas. Users are logins with a role in exactly one account.

| Can | owner | admin | member |
|---|---|---|---|
| See balance, transactions, usage, jobs, logs, artifacts, members | yes | yes | yes |
| Create keys; revoke keys they created | yes | yes | yes |
| Revoke any key | yes | yes | no |
| Invite `admin`/`member`, revoke invitations, remove members | yes | yes (members only) | no |
| Rename the account | yes | yes | no |
| Top up | yes | yes | no |
| Change roles, grant or take `owner` | yes | no | no |
| Export, close the account | yes (export: admin too) | export only | no |

Rules:

- Signing up creates an account with its first `owner`. There is always at
  least one `owner`; the last one cannot be demoted or removed (`409
  last_owner`). Ownership changes hands by promoting another member first.
- A member cap of 20 per account (live invitations included), stored as
  `accounts.member_limit` and raised by the operator on request like the
  top-up limits. `admin account limits` is not built for either yet; until
  it is, the operator raises it in SQL with an audit row by hand.
- An email belongs to at most one user, and a user to exactly one account.
  Membership in several accounts, and organisations above accounts, are not
  planned; a person who needs two tenants uses two addresses.
- Invitations expire after 7 days, are single-use, and carry the role they
  were sent with. The accept link proves the address, so no verification
  code is sent. Throttle: 20 invitations per account per day.
- Member actions (invite, role change, removal, rename) require a reauth
  within 10 minutes, like key creation, and are audited with the acting
  user as `actor_id`.
- Job API keys carry no user identity: the job's owner is `account:<id>`
  and `created_by` on the key is for the dashboard's key list and the audit
  log. Per-user attribution of spend is not in scope.

Built in A5b (migration `0013_members.sql`, `internal/accounts/members.go`,
`internal/dashboard/members.go`, pages `/members` and `/accept-invitation`).
The role checks on existing routes: `POST /topups` and `PATCH /balance` (the
low-balance threshold) are owner or admin, and a
member's `DELETE /keys/{id}` succeeds only for keys it created (`403
role_required` otherwise). The last-owner rule counts active owners. Two
choices beyond the text above: an admin invites, sends again and withdraws
`member` invitations only, and `GET /keys` does not say who created a key,
so the keys page still offers Revoke to a member and the server refuses it.

## Job API changes (`/compute/v1`)

- `Authorization: Bearer uck_…`. The shape check moves from `sk-` to `uck_`.
  The key is looked up by hash on every request; `last_used_at` is written at
  most once a minute.
- Owner = `account:<account_id>`. Everything owner-scoped (`quotas.md`,
  `events.md`, idempotency) works unchanged on that string.
- Submit additionally checks account `status = active` (`403
  account_suspended`), `compute_enabled` (`403 compute_not_enabled`) and the
  key's `jobs` scope (`403 scope_denied`), before quota and the hold.
- New: `GET /compute/v1/balance` (scope `read` or `jobs`) →
  `{ available_usd, held_usd }`, so scripts can check before submitting.
- Per-key rate limits (`quotas.md`) key on the key id instead of the raw key.

## Stripe webhook (`POST /dashboard/v1/stripe/webhook`)

On the app host, inside `/dashboard/v1` so the ALB rule that already sends
that prefix to compute-api covers it. It is the one dashboard route with no
session, no CSRF header and no Origin check; the Host check still applies.

The Stripe account is the one Unify API already runs production on. Compute
has its own restricted keys and its own endpoint and must not act on, or
fail on, anything of Unify API's. Every Stripe object compute creates
(Customer, Checkout Session, its inline Product, and through `payment_intent_data` the
PaymentIntent and its charge) carries `metadata.product = unify-compute`,
`account_id` and `environment`.

1. Read the raw body (at most 256 KiB). Parse `Stripe-Signature`
   (`t=…,v1=…[,v1=…]`). Compute HMAC-SHA256 of `t + "." + body` with
   `STRIPE_WEBHOOK_SECRET`; constant-time compare with every `v1`; refuse if
   `|now − t| > 300 s`. Failure → `400 invalid_signature`, nothing stored.
2. Decide whether the event is compute's. Not compute's → `200`
   (`outcome: ignored`), nothing stored, so Stripe neither retries it nor
   marks the endpoint failing:
   - `livemode` differs from the key's mode;
   - a `checkout.session.*` event whose session lacks
     `metadata.product = unify-compute` or carries another `environment`
     (staging and a developer's machine share test mode);
   - a `charge.refunded` or `charge.dispute.*` event whose `payment_intent`
     is not a top-up's;
   - any other type.
3. Insert into `stripe_events`; on conflict → `200` (`duplicate`), done.
4. Dispatch, in the same transaction:
   - `checkout.session.completed` with `payment_status = "paid"`, and
     `checkout.session.async_payment_succeeded`: credit. Requires that
     `client_reference_id` names a top-up, that its stored session id is the
     event's, that `metadata.topup_id` and `metadata.account_id` match it,
     `mode = "payment"`, `currency = "usd"` and `amount_total =
     amount_cents`. A mismatch, or a payment for a top-up already `expired`
     or `failed`, is logged as an error and answered `200` (`rejected`)
     without crediting, so Stripe does not retry something that will never
     be valid. Otherwise: the top-up moves to `credited` (`paid_at`, the
     PaymentIntent), a `topup` balance entry with idempotency key
     `topup:<id>` is written under the account lock, and a `topup_credited`
     audit row (actor `stripe`, the event id). A second event for an
     already credited top-up is `already_credited`. `completed` with
     `payment_status = "unpaid"` changes nothing (`awaiting_payment`).
   - `checkout.session.expired`: `open` → `expired`.
   - `checkout.session.async_payment_failed`: `open` → `failed`.
   - `charge.refunded`, `charge.dispute.created`, `charge.dispute.closed`
     on a top-up: applied as [Top-up](#top-up) describes, with outcomes
     `refunded`, `disputed`, `dispute_won`, `dispute_lost`, `no_change`
     (already applied), or `recorded` (could not be applied; logged as an
     error).
5. Commit, then look up the PaymentIntent with `latest_charge` expanded and
   store the charge id and `receipt_url` (outside the transaction; a failure
   leaves the top-up credited without a receipt link). Then the "Top-up
   received" email goes to the account's owners, once per top-up
   (`notifications.md`, "Sent once"), with the receipt link if the lookup
   found one. Both run in the background after the response; neither can
   undo the credit or change the webhook's answer. Built (W2). After a refund or
   dispute: carry out the recorded job cancels, then send the event's queued
   emails (`payment_emails`; also on a duplicate delivery, which retries any
   that failed).

The response body is `{ received: true, outcome }`, so Stripe's delivery log
says what happened. A `5xx` from us makes Stripe retry for up to 3 days,
which is the retry policy for database outages. Events are parsed in API
version `2024-06-20` (`payments.APIVersion`, also sent as `Stripe-Version`
on every request); the endpoint is created with that version.

## Admin CLI (`compute-api admin`)

Run on the host over SSM, with the same environment as the service. Every
mutating verb requires `--reason` and writes `audit_events`.

```
admin account show <email|account_id>
admin account enable-compute|disable-compute <id> --reason …
admin account suspend|reinstate <id> --reason …
admin account limits <id> [--topup-max-usd N] [--balance-max-usd N] [--topups-per-day N] --reason …
admin balance credit|debit <id> --usd 12.50 --reason …      # admin_credit / admin_debit entries
admin topup refund <tu_…> [--usd N] --reason …               # Stripe refund; the webhook writes the refund entry
admin refund-request list                                     # open customer requests, oldest first
admin refund-request decline <rr_…> --message … --reason …    # --message is emailed to the customer
admin audit [--account <id>]                                  # checks the balance invariant
```

`balance credit` is how the first vertical slice funds an account before
Stripe exists. It refuses an account with no verified user, because
`PurgeUnverified` would delete it with its balance. The balance entry's
`ref_id` and idempotency key name the audit event (`admin:ae_…`).
`enable-compute` emails the account's verified users ("Compute enabled")
after the change commits; a failed email is reported and does not undo it.

Built (A6): `account show|enable-compute|disable-compute|suspend|reinstate`,
`balance show|credit|debit`, `audit`. Not yet: `account limits` (the per-
account top-up columns exist and are read; until the verb exists the
operator changes them with SQL and an audit row). `topup refund` (B2) is built: it creates the refund at Stripe (`--amount` is accepted
for `--usd`; without either, everything left on the top-up), writes a
`refund` audit row with `stage: requested`, and changes no balance. An
operator's `account suspend` still does not stop running jobs; only a
dispute cancels them.
The CLI never migrates; the service does at boot.

## Low-balance warning

Built (W6). Each account has a threshold, `low_balance_quota`, default USD 5
and set on the Billing page (`PATCH /balance`); `0` turns the warning off.

- **Banner.** Every signed-in page shows a warning box first in its content
  while `available_usd` is below `low_balance_usd`, with a "Top up" link to
  `/billing` (on Billing itself, a line pointing at "Add credit"). It reads
  `GET /balance`, so it appears and goes away with the balance, whatever
  the email has done.
- **Email.** One to the account's active owners each time `available`
  crosses the threshold downward. The local biller decides it in the
  transaction that moves the balance, under the account lock: the account
  must be *armed* and the movement must leave `available` below the
  threshold. It then disarms the account and, after the commit, hands the
  crossing to `billing.LowBalanceMailer`, which sends through `notify` with
  subject `balance` and the crossing's balance entry id as `subject_id`
  (`notifications.md`, "Sent once"). The send runs in the background; a mail
  failure never fails the hold or extend.
- **Re-arming.** A credit (`topup`, `admin_credit`, `dispute_reversal`) that leaves `available`
  at or above the threshold arms the account again. A hold released or
  expired at a job's end does not: holds take `available` down at every
  submit and give the unused part back at every end, and re-arming there
  would mail an account near its threshold once per job. Changing the
  threshold sets armed to `available >= new value`, so raising it over the
  balance sends nothing at that moment (the banner shows) and waits for the
  next top-up.
- **What crosses.** Holds and extends, the movements that take `available`
  down in the biller's own transactions. A settle moves held, not
  `available`. An operator debit (`admin_debit`) runs in the admin CLI's
  transaction, so it is not reported and leaves the account armed: the next
  hold or extend below the threshold sends the email. A Stripe refund or
  dispute (`ReverseTx`, in the webhook's transaction) behaves the same way:
  the refund and suspension emails already tell the owner, and the
  low-balance email follows on the next hold or extend.
- **Not built.** Console accounts get nothing (no addresses here). The
  threshold is in USD only, per account, not per member.

## Emails

All from `Unify Compute <no-reply@compute.unifyai.us>`, multipart plain text
plus HTML in `email/base.html`. Shared slots as in `notifications.md` except
`{{footer_reason}}`, which is per message below. Amber appears only on the one
focal element. Subjects name the product and the action. The preheader adds
something the subject does not say.

| Message | Trigger | Focal element | Subject |
|---|---|---|---|
| Verification code | signup, resend | code block (6 digits) | `Your Unify Compute verification code` |
| Signup attempt on a registered address | signup with a known email | CTA "Log in" (`/login`) and "Forgot password?" as text | `Someone tried to sign up with your address` |
| Password reset | forgot-password for an `active` or `pending` user | CTA "Reset password" (link with token in the fragment) | `Reset your Unify Compute password` |
| Password changed | change or reset | none (no CTA; safety line says what to do) | `Your Unify Compute password was changed` |
| API key created | `POST /keys` | CTA "Review keys" | `A new Unify Compute API key was created` |
| Top-up received | webhook credits | CTA "View receipt" (Stripe `receipt_url`), or "View billing" (`/billing`) when the receipt lookup failed; details: amount, new balance, top-up id, paid. Built (W2) | `Unify Compute: USD 20.00 added to your balance` |
| Low balance | `available` crosses below `low_balance_quota` (see [Low-balance warning](#low-balance-warning)) | CTA "Top up" (`/billing`); details: available balance, held by running jobs, alert below. Built (W6) | `Unify Compute: your balance is below USD 5.00` |
| Refund issued | `charge.refunded` applied (from the CLI or the Stripe dashboard); states the refund policy and, when there is one, the bonus credit removed (ADR-0013) | CTA "View balance" (`/billing`) | `Unify Compute: refund of USD 20.00 issued` |
| Refund declined | `admin refund-request decline` (to the requester); the operator's message is the intro | CTA "View billing" (`/billing`) | `About your Unify Compute refund request`. ADR-0013 |
| Account suspended | a dispute that suspends the account (not sent for an admin suspension yet) | CTA "Contact us" (mailto `contactus@felixsphere.com`, per UI-STANDARD) | `Your Unify Compute account is suspended` |
| Compute enabled | admin `enable-compute` | CTA "Create an API key" | `Your Unify Compute account can now run jobs` |
| Invitation | `POST /members/invitations` | CTA "Accept invitation" (link with token in the fragment); details: account name, inviter, role | `<inviter> invited you to <account> on Unify Compute` |
| Invitation accepted | `POST /invitations/accept` (to the inviter) | CTA "View members" | `<email> joined <account> on Unify Compute` |
| Removed from account | `DELETE /members/{id}` (to the removed address) | none (safety line names the account and who to contact) | `You were removed from <account> on Unify Compute` |
| Job finished / failed / billing stopped | as `notifications.md` | as `notifications.md` | as `notifications.md`; the safety lines' "Unify API console" becomes the Unify Compute dashboard. Built (W2) |
| Storage arrears started / artifacts deletion scheduled / deleted | ADR-0008 | CTA "Top up" | `Unify Compute: storage unpaid, artifacts deleted on <date>` (and variants) |
| Operator: dispute opened, dispute won/lost, refund left a balance negative, refund requested | B2 webhook; `POST /topups/{id}/refund-request` (ADR-0013); limit refusals are not emailed | — (plain operator notice to `OPERATOR_EMAIL`) | `[Unify Compute ops] dispute opened on tu_…` |

The links open these dashboard pages on `APP_ORIGIN`: `/login`,
`/forgot-password`, `/reset-password#token=…`, `/keys`, `/members` and
`/accept-invitation#token=…`
(`compute-api/internal/mailer/messages.go`), and `/jobs/{id}` and `/billing`
(`messages_jobs.go`, `messages_balance.go`). A7b and A7c build the pages at those paths or change
them there.

`{{footer_reason}}` for account messages: "You're receiving this because
this address is registered for a Unify Compute account." For the
signup-attempt message: "You're receiving this because someone entered this
address on the Unify Compute sign-up page."

The plain-text part follows the kit: eyebrow, headline, intro, the code or the
CTA label with the full URL on its own line, detail rows as `Label: value`,
the safety line, the footer. Codes and links state their expiry in the safety
line.

## Configuration

| Variable | Meaning |
|---|---|
| `ACCOUNTS_BACKEND` | `console` (today) or `local`. Removed after the cutover |
| `APP_ORIGIN` | `https://app.compute.unifyai.us`: Host/Origin checks, links in emails |
| `ACCOUNTS_COMPUTE_DEFAULT` | `compute_enabled` for new accounts; `false` while starting small |
| `SES_SENDER` | `'Unify Compute <no-reply@compute.unifyai.us>'`, quoted. With `local` and no sender, compute-api refuses to start unless `ENVIRONMENT=development` |
| `SES_CONFIGURATION_SET` | `unify-compute` (also the identity default; set for clarity) |
| `MAIL_OUTBOX_DIR` | Only with `ENVIRONMENT=development` and no `SES_SENDER`: where messages are written as `.eml` files instead of sent (default `outbox`) |
| `TURNSTILE_SITE_KEY`, `TURNSTILE_SECRET` | Cloudflare; test keys in development |
| `STRIPE_SECRET_KEY` | Compute's own restricted key on the shared Stripe account: `rk_test_…` with any `ENVIRONMENT` but `production`, `rk_live_…` only with `ENVIRONMENT=production` (and only after O5/O6). B1 needs Checkout Sessions write, Customers write, PaymentIntents read and Charges read (the receipt lookup expands `latest_charge`); B2 adds Refunds write and Disputes read. A key in the wrong environment, a malformed one, or one without its webhook secret is logged as an error and top-ups answer `503 topups_unavailable`; compute-api still boots |
| `OPERATOR_EMAIL` | Where operator notices go (disputes, a refund that leaves a negative balance). Unset: `contactus@felixsphere.com` |
| `STRIPE_WEBHOOK_SECRET` | `whsec_…` of compute's own endpoint (`https://<app host>/dashboard/v1/stripe/webhook`), never Unify API's |

Secrets come from SSM under the stack's prefix, like the existing ones
(`deploy/aws/modules/compute-stack/instance.tf`, `ssm_prefix`). The Stripe
pair is `/unify-compute-<env>/stripe_secret_key` and
`/unify-compute-<env>/stripe_webhook_secret` (SecureString, written by the
operator; `docs/staging.md`, "Stripe top-ups").

## Terms and Conditions

On 2026-10-03 the operator decided that Unify Compute uses FelixSphere's
existing Terms and Conditions at https://felixsphere.com/legal/terms, since
FelixSphere LLC is the contracting entity (ADR-0011 D2). The sign-up consent
line and the footers of the marketing site and the app host link there; the
URL is `TERMS_URL` in `app/links.ts`, which `dashboard/vite.config.ts` also
imports. compute.unifyai.us has no `/terms` page.

Two known gaps, left for the legal review before the first live top-up
(ADR-0011 D6; `plan-own-accounts.md` O5):

1. **Refunds.** The document is a generic template whose refund clause is
   written for physical goods (returns, shipping), not for prepaid compute
   credit that is spent per second. The operator decided the policy on
   2026-10-14 (unused prepaid balance can be refunded on request; compute
   already used is not), and the refund email states it; the terms and the
   email's wording still need the legal review. Bonus credit (ADR-0013: no
   cash value, reduced pro rata on a refund) is not in the terms either.
2. **Acceptable use.** It has no acceptable-use policy: nothing forbids
   cryptocurrency mining, abuse of GPUs, or the other uses the upstream GPU
   providers' own terms require us to prohibit and enforce.

The privacy policy (`app/privacy/page.tsx`) names FelixSphere LLC as the
Company and data controller, and says "Unify Compute is a UnifyAI product
operated by FelixSphere LLC, 6 Karen Ct, CA 94010, United States." On
2026-10-03 the operator made FelixSphere LLC (US) the legal entity for terms,
privacy, payments and email sender across all five UnifyAI products,
replacing the Malaysian company line of 2026-10-02; the brand stays UnifyAI
(`~/unifyai-brand-kit/UI-STANDARD.md`, "Company and legal facts"). So:

- Page footers, on the marketing site and the app host, read only
  "© <year> UnifyAI".
- The privacy page's footer adds "Operated by FelixSphere LLC · 6 Karen Ct,
  CA 94010, United States".
- Email footers end with "© UnifyAI · Operated by FelixSphere LLC · 6 Karen
  Ct, CA 94010, United States" (`docs/spec/email/base.html`).

The address has no city; it is the one FelixSphere publishes on its legal
pages, and the brand owner has asked the operator for the city line before
the first paid invoice. The standard now allows "FelixSphere LLC" in that
entity line and the Terms link, so `check-consistency.sh` no longer flags
them.

## Open points

1. Decisions D1–D8 in ADR-0011.
2. Whether `pending` users can be purged earlier than 7 days under abuse.
3. ~~A per-key monthly spend cap~~ Built: `api_keys.monthly_cap`, `PATCH /keys/{id}`, `403 key_cap_exceeded`.
4. Breached-password checking and TOTP: later. Organisations above accounts:
   not planned; the account is the tenant and invitations are specified in
   [Members and roles](#members-and-roles) (D9).
5. The Terms and Conditions' refund and acceptable-use gaps (above).
