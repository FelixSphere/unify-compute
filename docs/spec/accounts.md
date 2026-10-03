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
  the Stripe keys, which no environment has yet. Refunds and disputes (B2),
  the top-up receipt email, `admin account limits`, `GET /compute/v1/balance`,
  account export and close do not exist yet.
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
| `status` | `text` | `active`, `suspended`, `closed` |
| `compute_enabled` | `boolean` | Default from `ACCOUNTS_COMPUTE_DEFAULT` (`false` while starting small; ADR-0011 D3) |
| `available` | `bigint` | Quota units. `CHECK (available >= 0 OR status = 'suspended')`; only a chargeback drives it negative |
| `held` | `bigint` | Quota units, `>= 0` |
| `topup_min_cents`, `topup_max_cents`, `balance_max_cents`, `topups_per_day` | `integer` | Defaults 1000, 10000, 20000, 3 |
| `stripe_customer_id` | `text` unique, null | Created on first top-up |
| `suspended_reason` | `text` null | `dispute`, `abuse`, `operator` |
| `created_at`, `closed_at` | `timestamptz` | |

### `users` (a login)

| Column | Type | Notes |
|---|---|---|
| `id` | `bigint` identity PK | |
| `account_id` | `bigint` FK → accounts | Exactly one user per account today |
| `role` | `text` | `owner` only, for now |
| `email` | `text` | Stored NFC-normalised, trimmed, lower-cased; unique. Replaced by `closed+<id>@invalid` at closing |
| `password_hash` | `text` | argon2id PHC string |
| `status` | `text` | `pending`, `active`, `disabled`, `closed` |
| `email_verified_at`, `last_login_at`, `password_changed_at`, `created_at` | `timestamptz` | |

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

Key format: `uck_` followed by 40 characters from `[0-9A-Za-z]`, from
`crypto/rand`. Any other shape is `401 missing_key` without a database read.

### `balance_entries` (**append-only**)

| Column | Type | Notes |
|---|---|---|
| `id` | `text` PK | `be_` + ULID |
| `seq` | `bigint` identity unique | Write order |
| `account_id` | `bigint` | |
| `kind` | `text` | `topup`, `hold`, `extend`, `settle`, `release`, `refund`, `dispute`, `dispute_reversal`, `admin_credit`, `admin_debit`, `writeoff` |
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
| `refunded_cents`, `disputed_cents` | `integer` | B2 |
| `created_at`, `paid_at`, `expires_at` | `timestamptz` | `expires_at` = Checkout's, one hour after creation (`payments.CheckoutTTL`) |

### `stripe_events`

`event_id PK`, `type`, `livemode`, `topup_id`, `outcome`, `payload_sha256`,
`received_at`, `processed_at`. Inserted first in the same transaction as the
effect, so a redelivered event, even one delivered concurrently, is a no-op.
Only compute's own events are stored; see [Stripe webhook](#stripe-webhook-post-dashboardv1stripewebhook).

### `audit_events` (**append-only**)

`id` (`ae_` + ULID), `seq`, `at`, `actor_type` (`user`, `admin`, `system`,
`stripe`), `actor_id`, `account_id`, `action`, `ip`, `user_agent`, `reason`
(required for `admin`), `detail jsonb`. Actions: `signup`, `verify`,
`login`, `login_failed`, `logout`, `reset_requested`, `password_reset`,
`password_changed`, `key_created`, `key_revoked`, `topup_created`,
`topup_credited`, `refund`, `dispute_opened`, `dispute_closed`,
`account_suspended`, `account_reinstated`, `compute_enabled`,
`limits_changed`, `admin_credit`, `admin_debit`, `export`, `account_closed`.

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
- Refunds and disputes (below) are B2. Until B2, their events on a top-up
  are stored (`outcome = recorded`) and logged as errors for the operator,
  and nothing moves.
- `disputed` debits `disputed_cents` immediately; `won` writes a
  `dispute_reversal` entry; `lost` writes nothing more.
- A refund is created by the admin CLI (`refund` entry, top-up →
  `refunded` or `partially_refunded`), confirmed by `charge.refunded`. A
  refund larger than the account's `available` is refused: the customer
  cannot be refunded money already spent on GPUs.

### Account

`active ⇄ suspended → closed`. Suspension is automatic on a dispute that
leaves `available < 0`, otherwise an admin action. Reinstatement is always an
admin action.

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
| 400 | `invalid_request` | `invalid_month` | `/usage`: `month` not `YYYY-MM` |
| 400 | `invalid_request` | `invalid_cursor`, `invalid_limit`, `invalid_status` | List routes: `after` not one of this account's entries or jobs, `limit` out of range, unknown job status (`param` names it) |
| 404 | `not_found` | `not_found` | Key or top-up not in this account |
| 404 | `not_found` | `job_not_found` | Job not in this account, on the dashboard as on the job API |
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

### Session (cookie `__Host-uc_session` + `X-CSRF-Token`)

| Route | Body | Success |
|---|---|---|
| `GET /me` | — | `{ user: { email, created_at }, account: { id, status, compute_enabled }, csrf_token }` |
| `POST /logout` | — | `204`, cookie cleared |
| `POST /password` | `{ current_password, password, password_confirm }` | `204`; other sessions ended. `current_password` is the recent-password check and counts as a reauth; wrong is `401 invalid_credentials` with `param: current_password` |
| `POST /reauth` | `{ password }` | `204`; sets `reauth_at` |
| `GET /balance` | — | `{ object: "compute.balance", currency: "USD", available_usd, held_usd, limits: { topup_min_usd, topup_max_usd, balance_max_usd, topups_per_day } }`, from the account row. Built (A7d) |
| `GET /transactions?after=&limit=` | — | `{ object: "list", data, has_more, next? }`, balance entries newest first, `limit` 1–100 (default 20), `after` an entry id: `{ id, object: "compute.balance_entry", kind, amount_usd, available_delta_usd, held_delta_usd, available_after_usd, held_after_usd, job_id, ref, created_at, detail }`. `amount_usd` is what a `settle` took out of held (negative), and for every other kind the change in available. `ref` is `{ type: "job" \| "topup", id }` or `null`: a hold's id is replaced by its job, and an operator adjustment's audit id and reason are never returned. `detail` holds only `sku`. Built (A7d) |
| `GET /keys` | — | `[{ id, name, display, scopes, created_at, expires_at, last_used_at, revoked_at }]`, a bare array, newest first, revoked and expired keys included. Built (A5) |
| `POST /keys` | `{ name, scopes, expires_at? }` | `201 { …key, secret: "uck_…" }`; the secret is never returned again. Needs reauth after 10 min (`401 reauth_required`, checked before the body). Emails "key created". Built (A5) |
| `DELETE /keys/{id}` | — | `204`, also for a key already revoked; another account's key is `404 not_found`. Built (A5) |
| `GET /jobs?status=&after=&limit=` | — | The job API's list, owner-scoped to the account. Built (A7d) |
| `GET /jobs/{id}` | — | The Job object (`job-api.md`). Built (A7d) |
| `GET /jobs/{id}/events`, `GET /jobs/{id}/logs` | — | As the job API (`follow=true` is 501 in both). Built (A7d) |
| `POST /jobs/{id}/cancel` | — | As the job API: `202` with the Job, `409 job_terminal` once ended. Needs the CSRF header. Built (A7d) |
| `GET /jobs/{id}/artifacts` | — | As the job API: the listing and presigned GET URLs. Without it the job page could not offer downloads, because the page has a session and the job API takes only a key. Built (A7d) |
| `GET /usage?month=YYYY-MM` | — | `{ object: "compute.usage", month, currency: "USD", gpu_seconds, gpu_usd, storage_usd, egress_usd, total_usd, by_sku: [{ sku, gpu_seconds, usd }] }` for one UTC month (absent: the current one; malformed: `400 invalid_month`). Totals the settles accepted in the month by SKU, in quota units converted once, so `total_usd` is exactly what the month's settle entries took; `gpu_seconds` counts what each settle added. Usage accrued but not yet settled is not in it. Holds whose SKU is `storage` or `egress` (R5, not built) go to those meters, so both read `0.0000` today. Built (A7d) |
| `POST /topups` | `{ amount_usd: "20.00" }` | `201 { id, object: "compute.topup", status: "open", checkout_url }`. Checks the account is active and the limits, expires an open top-up's session (above), creates the Customer on the first top-up and the Checkout Session: mode `payment`, one USD line item, `payment_method_types[0]=card`, `saved_payment_method_options[payment_method_save]=disabled`, no `setup_future_usage`, `client_reference_id` and idempotency key from the top-up id, `metadata` and `payment_intent_data[metadata]` = `product: unify-compute`, `account_id`, `topup_id`, `environment`, return URLs `APP_ORIGIN/billing?topup=<id>&checkout=success` / `=cancelled`, expiring after 1 h. Writes `topup_created`. Built (B1) |
| `GET /topups` | — | `{ object: "list", data, has_more: false, topups_enabled }`: the 50 newest, newest first, each `{ id, object: "compute.topup", amount_usd, status, created_at, paid_at, receipt_url, refunded_usd }`. `topups_enabled` says whether `POST` can work here, so the page offers the form only then. Built (B1) |
| `GET /topups/{id}` | — | One top-up, as listed; another account's is `404 not_found`. What `/billing` polls after the return from Checkout. Built (B1) |
| `POST /stripe/webhook` | Stripe event | Not for the pages: see [Stripe webhook](#stripe-webhook-post-dashboardv1stripewebhook). Built (B1) |
| `GET /account/export` | — | `200` JSON attachment: user, account, keys (metadata), jobs, balance entries, top-ups |
| `POST /account/close` | `{ password }` | `202`; jobs cancelled, keys revoked, artifacts deleted, sessions ended, address tombstoned |

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
     on a top-up: stored as `recorded` and logged as an error for the
     operator. Applying them is B2.
5. Commit, then look up the PaymentIntent with `latest_charge` expanded and
   store the charge id and `receipt_url` (outside the transaction; a failure
   leaves the top-up credited without a receipt link). The "Top-up
   received" email belongs here and is not built yet.

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
admin topup refund <tu_…> [--usd N] --reason …               # Stripe refund + refund entry
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
operator changes them with SQL and an audit row) and `topup refund` (B2). Suspending does not stop running jobs yet (B2).
The CLI never migrates; the service does at boot.

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
| Top-up received | webhook credits | CTA "View receipt" (Stripe `receipt_url`); details: amount, new balance | `Unify Compute: USD 20.00 added to your balance` |
| Refund issued | admin refund | CTA "View balance" | `Unify Compute: refund of USD 20.00 issued` |
| Account suspended | dispute or admin | CTA "Contact us" (mailto `contactus@felixsphere.com`, per UI-STANDARD) | `Your Unify Compute account is suspended` |
| Compute enabled | admin `enable-compute` | CTA "Create an API key" | `Your Unify Compute account can now run jobs` |
| Job finished / failed / billing stopped | as `notifications.md` | as `notifications.md` | as `notifications.md`; the safety lines' "Unify API console" becomes the Unify Compute dashboard |
| Storage arrears started / artifacts deletion scheduled / deleted | ADR-0008 | CTA "Top up" | `Unify Compute: storage unpaid, artifacts deleted on <date>` (and variants) |
| Operator: top-up review request, dispute opened | limit refusal is not emailed; disputes are | — (plain operator notice to the operator address) | `[Unify Compute ops] dispute opened on tu_…` |

The links open these dashboard pages on `APP_ORIGIN`: `/login`,
`/forgot-password`, `/reset-password#token=…` and `/keys`
(`compute-api/internal/mailer/messages.go`). A7b and A7c build the pages at
those paths or change them there.

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
| `STRIPE_WEBHOOK_SECRET` | `whsec_…` of compute's own endpoint (`https://<app host>/dashboard/v1/stripe/webhook`), never Unify API's |

Secrets come from SSM under the stack's prefix, like the existing ones
(`deploy/aws/modules/compute-stack/instance.tf`, `ssm_prefix`). The Stripe
pair is `/unify-compute-<env>/stripe_secret_key` and
`/unify-compute-<env>/stripe_webhook_secret` (SecureString, written by the
operator; `docs/staging.md`, "Stripe top-ups").

## Open points

1. Decisions D1–D8 in ADR-0011.
2. Whether `pending` users can be purged earlier than 7 days under abuse.
3. A per-key monthly spend cap (the console's token quota) if a customer asks.
4. Breached-password checking, TOTP, organisations and invitations: later.
