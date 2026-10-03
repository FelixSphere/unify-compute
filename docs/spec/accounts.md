# Accounts, keys, balance and top-ups

- **Status:** Spec, nothing built. Decided by
  [ADR-0011](../adr/0011-compute-owns-accounts-and-billing.md) (**Proposed**).
  Until the cutover in that ADR, compute-api authenticates through the console
  bridge ([`console-billing-bridge.md`](console-billing-bridge.md)) and none of
  the routes, tables or emails below exist.
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
| Stripe webhook | `api.compute.unifyai.us` | `/stripe/v1/webhook` | `Stripe-Signature` |
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
  `status` (`open`, `released`, `expired`), `expires_at`, `request` (`jsonb`,
  the original body for idempotency comparison), timestamps.
- `billing_settlements`: `event_id PK`, `hold_id`, `quota_delta`,
  `gpu_seconds`, `final`.
- `billing_extensions`: `extend_id PK`, `hold_id`, `additional_quota`.

Each verb writes its rows and its `balance_entries` row in one transaction.
Expiry is swept hourly: past `expires_at`, the remainder is released and the
hold answers `409 hold_expired` afterwards, as the console's sweeper did.

The existing `holds` table (the ledger's own view of a hold) is unchanged.
After the cutover its `user_id` holds the account id and `token_id` the key id.

### `topups`

| Column | Type | Notes |
|---|---|---|
| `id` | `text` PK | `tu_` + ULID; Checkout `client_reference_id` and `metadata.topup_id` |
| `account_id`, `user_id` | `bigint` | |
| `amount_cents` | `integer` | `currency` is always `usd` |
| `status` | `text` | See [Top-up](#top-up) |
| `stripe_checkout_session_id` | `text` unique | |
| `stripe_payment_intent_id`, `stripe_charge_id` | `text` | From the completed session |
| `receipt_url` | `text` | Stripe's hosted receipt |
| `refunded_cents`, `disputed_cents` | `integer` | |
| `created_at`, `paid_at`, `expires_at` | `timestamptz` | `expires_at` = Checkout's (24 h) |

### `stripe_events`

`event_id PK`, `type`, `received_at`, `processed_at`, `payload_sha256`.
Inserted in the same transaction as the effect, so a redelivered event is a
no-op.

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
          create                webhook: completed, paid
(none) ──────────▶ open ─────────────────────────────▶ credited ──refund──▶ refunded / partially_refunded
                    │                                    │
                    └──webhook: expired (24 h)──▶ expired │ dispute.created
                                                         ▼
                                                     disputed ──closed: won──▶ credited
                                                         └──────closed: lost──▶ lost (terminal)
```

- `credited` is written only by the webhook, never by the return page.
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
| 400 | `invalid_request` | `bot_check_failed` | Turnstile token missing, invalid, expired or reused; or the honeypot field filled |
| 400 | `invalid_request` | `invalid_code` | Verification code wrong, expired, exhausted or superseded (one code for all four) |
| 400 | `invalid_request` | `invalid_token` | Reset link invalid, used or expired |
| 400 | `invalid_request` | `invalid_scope` | Key scopes empty or unknown |
| 400 | `invalid_request` | `invalid_amount` | Top-up not a whole-cent USD amount |
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
| 403 | `permission_error` | `topup_limit_exceeded` | Below the minimum, above the per-top-up maximum, would exceed the balance maximum, or over the daily count. `message` says which and invites contact for a review |
| 403 | `permission_error` | `key_limit_reached` | 10 active keys already |
| 404 | `not_found` | `not_found` | Key, top-up or job not in this account |
| 409 | `conflict` | `topup_open` | A top-up from this account is already `open`; finish or abandon it (returns its id) |
| 429 | `rate_limited` | `rate_limited` | Any throttle; always with `Retry-After` |
| 502 | `payment_provider_error` | `stripe_unavailable` | Stripe did not create the Checkout Session; nothing was charged |
| 503 | `email_unavailable` | `email_unavailable` | SES refused a verification email at signup; the signup is rolled back (Unify Data's behaviour). Never returned by forgot-password (that answers `202` either way) |

`console_unavailable` disappears with the bridge. `billing_unavailable`
stays as a job `error.code` for a database failure during settlement.

## Dashboard API (`/dashboard/v1`)

All bodies JSON. Unauthenticated routes need no CSRF header but do need a
same-origin `Origin`. Every route that sends email or creates something is
throttled (limits in ADR-0011, section 8).

### Public

| Route | Body | Success | Notes |
|---|---|---|---|
| `GET /config` | — | `200 { turnstile_site_key, min_password_length: 12, topup: { min_usd, max_usd } }` | What the signed-out pages need |
| `POST /signup` | `{ email, password, password_confirm, turnstile_token, website }` | `202 { status: "verification_sent" }` | Always `202` for a well-formed request. A new address gets a code; a registered one gets "someone tried to sign up". `website` is the honeypot |
| `POST /verify` | `{ email, code }` | `200 { user, account, csrf_token }` + `Set-Cookie` | Activates and logs in |
| `POST /verify/resend` | `{ email, turnstile_token }` | `202` | Always `202`; sends only to a `pending` user; at most 1 a minute and 5 an hour per address |
| `POST /login` | `{ email, password, turnstile_token }` | `200 { user, account, csrf_token }` + `Set-Cookie` | Rotates any existing session |
| `POST /password/forgot` | `{ email, turnstile_token }` | `202` | Always `202` |
| `POST /password/reset` | `{ token, password, password_confirm }` | `200` | Ends every session; does not log in |

### Session (cookie `__Host-uc_session` + `X-CSRF-Token`)

| Route | Body | Success |
|---|---|---|
| `GET /me` | — | `{ user: { email, created_at }, account: { id, status, compute_enabled }, csrf_token }` |
| `POST /logout` | — | `204`, cookie cleared |
| `POST /password` | `{ current_password, password, password_confirm }` | `204`; other sessions ended |
| `POST /reauth` | `{ password }` | `204`; sets `reauth_at` |
| `GET /balance` | — | `{ available_usd, held_usd, limits: { topup_min_usd, topup_max_usd, balance_max_usd, topups_per_day } }` |
| `GET /transactions?after=&limit=` | — | Cursor list of balance entries, newest first: `{ id, kind, amount_usd, available_after_usd, ref, created_at, detail }` |
| `GET /keys` | — | `[{ id, name, display, scopes, created_at, expires_at, last_used_at, revoked_at }]` |
| `POST /keys` | `{ name, scopes, expires_at? }` | `201 { …key, secret: "uck_…" }`; the secret is never returned again. Needs reauth after 10 min |
| `DELETE /keys/{id}` | — | `204` |
| `GET /jobs?status=&after=&limit=` | — | The job API's list, owner-scoped to the account |
| `GET /jobs/{id}` | — | The Job object (`job-api.md`) |
| `GET /jobs/{id}/events`, `GET /jobs/{id}/logs` | — | As the job API |
| `POST /jobs/{id}/cancel` | — | As the job API |
| `GET /usage?month=YYYY-MM` | — | `{ gpu_usd, storage_usd, egress_usd, total_usd, by_sku: [...] }`, from settle entries |
| `POST /topups` | `{ amount_usd: "20.00" }` | `201 { id, status: "open", checkout_url }` |
| `GET /topups`, `GET /topups/{id}` | — | `{ id, amount_usd, status, created_at, paid_at, receipt_url, refunded_usd }` |
| `GET /account/export` | — | `200` JSON attachment: user, account, keys (metadata), jobs, balance entries, top-ups |
| `POST /account/close` | `{ password }` | `202`; jobs cancelled, keys revoked, artifacts deleted, sessions ended, address tombstoned |

Submitting jobs from the dashboard is not in the first version: customers
submit with a key.

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

## Stripe webhook (`POST /stripe/v1/webhook`)

1. Read the raw body (at most 256 KiB). Parse `Stripe-Signature`
   (`t=…,v1=…`). Compute HMAC-SHA256 of `t + "." + body` with
   `STRIPE_WEBHOOK_SECRET`; constant-time compare with any `v1`; refuse if
   `|now − t| > 300 s`. Failure → `400`, nothing stored.
2. Insert into `stripe_events`; on conflict → `200`, done.
3. Dispatch on `type` (`checkout.session.completed`,
   `checkout.session.expired`, `charge.refunded`, `charge.dispute.created`,
   `charge.dispute.closed`), in the same transaction. Any other type: `200`.
4. For `checkout.session.completed`: require `payment_status = "paid"`,
   `mode = "payment"`, `currency = "usd"`, and that `amount_total` equals the
   top-up's `amount_cents` and `client_reference_id` names an `open` top-up
   of the same account (via `metadata.account_id`). A mismatch is logged,
   alerted, and answered `200` without crediting, so Stripe does not retry
   something that will never be valid.
5. Commit, then send the receipt email (outside the transaction; a failed
   email never un-credits).

A `5xx` from us makes Stripe retry for up to 3 days, which is the retry policy
for database outages.

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
Stripe exists.

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
| `TURNSTILE_SITE_KEY`, `TURNSTILE_SECRET` | Cloudflare; test keys in development |
| `STRIPE_SECRET_KEY` | `sk_test_…` until the operator approves live mode; a restricted key with Checkout Sessions write, Customers write, Refunds write, and read on Charges and Disputes |
| `STRIPE_WEBHOOK_SECRET` | `whsec_…` of the endpoint |

Secrets come from SSM under the stack's prefix, like the existing ones
(`deploy/aws/modules/compute-stack/instance.tf`, `ssm_prefix`).

## Open points

1. Decisions D1–D8 in ADR-0011.
2. Whether `pending` users can be purged earlier than 7 days under abuse.
3. A per-key monthly spend cap (the console's token quota) if a customer asks.
4. Breached-password checking, TOTP, organisations and invitations: later.
