# ADR-0011 — Unify Compute owns its accounts, keys, balance and payments

- **Status:** Accepted (operator, 2026-10-03; decisions recorded under "Operator decisions" below)
- **Date:** 2026-10-03
- **Supersedes, once accepted:** [ADR-0001](0001-unifyapi-owns-accounts-and-billing.md)
  (Unify API owns accounts, keys and the balance).
- **Amends, once accepted:** [ADR-0008](0008-zero-balance-policy.md) (the
  balance becomes ours; see [What changes in ADR-0008 and metering.md](#what-changes-in-adr-0008-and-meteringmd)).
  [ADR-0002](0002-compute-service-is-separate-and-proprietary.md) stands. Its
  licence rules matter more now, not less, because accounts and top-ups are
  exactly the code that would be tempting to "adapt" from the console.
- **Related:** [`spec/accounts.md`](../spec/accounts.md) (the contract this ADR
  decides), [`plan-own-accounts.md`](../plan-own-accounts.md) (the task
  breakdown), [`spec/console-billing-bridge.md`](../spec/console-billing-bridge.md)
  (retired by this ADR), [`spec/metering.md`](../spec/metering.md),
  [`spec/notifications.md`](../spec/notifications.md), ADR-0009 (AWS hosting),
  ADR-0010 (GPU workers, start small).

Nothing in this ADR is built. Every statement about compute's own accounts is a
decision or a plan, not a description of running code.

## Context

On 2026-10-03 the operator decided that **Unify Compute gets its own accounts,
registration, balance and payments, as Unify Data does**, instead of reusing
Unify API's. ADR-0001 (2026-09-10) had decided the opposite: one key, one
balance, one bill across Unify API and Unify Compute.

What exists on 2026-10-03, so the cost of changing course is concrete:

- compute-api authenticates every keyed route through the console bridge's
  `verify` (`internal/api/auth.go`, `internal/console`). The owner of a job is
  `user:<id>` or `tenant:<id>` in the console (`console.Identity.Owner`).
- The GPU ledger (`internal/ledger`) places, settles, extends and releases
  holds on the console (`e.console.Hold/Settle/Extend/Release`, five call
  sites in `ledger.go` and `resume.go`). Amounts are console quota units,
  500000 per USD 1.
- Production compute-api calls the live bridge on `app.unifyapi.ai` since
  2026-10-03 (`deploy/releases/2026-10-03-production-console-bridge.md`).
  GPUs are off in production, so no production hold has ever been placed.
- Staging runs its own console container (UnifyAPI trial build) and Redis
  only so the bridge has something to talk to.
- **There are no customers.** No balance, key or job has to be migrated.
- Unify Data has its own accounts (email + password, verification, reset,
  sessions, SES email, a bot check). **It has no payments.** It has not
  connected Stripe. The only production payment flow in the UnifyAI family is
  the Unify API console's top-up, which is AGPL and may not be copied
  (ADR-0002). Compute's payments are therefore **designed fresh**; nobody in
  the family has built this outside the console.
- The brand standard (`~/unifyai-brand-kit/UI-STANDARD.md`) already lists Unify
  Compute's Log in and Sign up as "its own app.compute.unifyai.us log-in",
  "own account system since 2026-10-03".

## Why

The operator's reasons, as given: Unify Compute stands as its own product with
its own brand, like Unify Data, rather than as a feature of Unify API.

The trade-off against keeping ADR-0001:

| | Keep ADR-0001 (shared console) | This ADR (own accounts) |
|---|---|---|
| Signup, login, reset | Already built (console) | We build them: one more login for a customer of both products |
| Payments | Console top-ups exist and are live | We build Stripe Checkout top-ups from nothing (no in-family reference outside AGPL code) |
| Invoices, tenant reporting, consume-log usage view | Console's, free | We get Stripe receipts; usage view and admin reporting are ours to build |
| Organisations (tenants) | Console's, free | Individual accounts first; organisations later (below) |
| Coupling | Every console release can break compute submits and, past ~50 min, running jobs (ADR-0008, `billing_unavailable`). Bridge changes are two-repo, two-CI/CD-agent releases | None. A console outage is no longer a compute outage |
| Licence surface | Bridge server is AGPL code in the console that we must keep in step | No AGPL code on the path at all |
| Brand | Customer signs up at Unify API to use Unify Compute | Customer signs up at Unify Compute, per UI-STANDARD |
| Balance contention | Token traffic and GPU holds share one balance; a GPU hold can starve token traffic (metering.md's reason for the rolling hold) | Separate balances; a customer of both tops up twice |
| Email | Compute cannot email anyone (console has the addresses; ADR-0008 open point 1, notifications.md "Delivery") | Compute has the address and sends its own mail; that open point closes |

**What we lose:** Unify API's payment integration, its invoices, its tenant
(organisation) model, its consume-log usage view and its operator reporting.
And the "one key, one balance" story for customers of both products.

**What we gain:** independence from console releases and outages, no bridge to
version across two repositories, the ability to email our own customers, a
balance the zero-balance policy can actually reason about (ADR-0008 had to
work around not seeing it), and the product's own brand at signup.

**What it costs:** roughly ten agent-sized tasks before Stripe and five after
(`plan-own-accounts.md`), a Stripe account with KYC, legal review of terms,
refunds and tax, and permanent ownership of a security-sensitive surface
(passwords, sessions, payments) that the console used to carry.

## Decision

### 1. Identity: individual accounts now, organisations later, but shaped for them

- **Email + password.** No third-party sign-in (Google, GitHub) in the first
  version. UI-STANDARD allows it below an "or" divider later.
- **Two tables from day one: `accounts` and `users`.** An account is the
  billing entity: it owns the balance, the keys, the jobs, the top-ups. A user
  is a login. Today every account has exactly one user, with role `owner`.
  This is the one thing worth copying from Unify Data's history: Data started
  with "a tenant owns rows, a user belongs to one tenant" (its ADR-0003) and
  could later add organisations above workspaces (its ADR-0009) without moving
  any row. Collapsing account and user into one table now would make
  organisations a data migration later. Keeping them apart costs one join.
  Invitations, more users per account, and roles beyond `owner` are **not**
  built now.
- **Verification by emailed 6-digit code.** UI-STANDARD's verify screen has a
  code field ("Check your email", IBM Plex Mono 24px), and the email kit's
  focal element can be a code block. Signup leads straight to that screen.
  Entering the right code activates the account and logs the user in, in the
  same browser. A code lives 15 minutes, allows 5 wrong attempts, and a resend
  invalidates the previous one.
- **Reset by emailed link**, single use, 1 hour, the token in the URL
  **fragment** so it never reaches a server log (copied from Unify Data). A
  reset ends every session of that user (copied from Unify Data).
- **Sessions for the dashboard, API keys for the API, never crossed.** The
  dashboard (`/dashboard/v1/*`, `app.compute.unifyai.us`) accepts only the
  session cookie; the job API (`/compute/v1/*`, `api.compute.unifyai.us`)
  accepts only `Authorization: Bearer uck_…`. That keeps CSRF off the public
  API and keeps a leaked key from reaching billing or key management.
- **Bot check: Cloudflare Turnstile plus a honeypot field.** Turnstile sits
  directly above the primary button on signup, login, resend and forgot-password
  (UI-STANDARD). The honeypot (a field no person sees; filled means a script
  filled it) is copied from Unify Data's `botcheck.py`. Unify Data chose a
  proof-of-work instead of Turnstile so that nothing goes to a third party; we
  follow UI-STANDARD and the operator, which name Turnstile, and the privacy
  policy names Cloudflare as a processor. Turnstile is verified server-side
  (`siteverify`) with the stdlib HTTP client. Development and tests use
  Cloudflare's documented always-pass test keys.
- **Who may spend, while we start small.** The platform runs one GPU job at a
  time across all accounts (ADR-0010). An open signup could take that slot
  away from the reference workload. So every account carries a
  `compute_enabled` flag, **default off during start-small**, that the operator
  turns on by hand. Signup, keys and top-ups work without it. A submit without
  it is `403 compute_not_enabled`. This is Unify Data's "a super admin admits
  the person" gate, moved from login to spending. Operator decision D3 may
  instead default it on.

### 2. API keys

- Format: `uck_` + 40 base62 characters (≈ 238 bits). The prefix is distinct
  from Unify API's `sk-` on purpose. A key pasted into the wrong product fails
  at the shape check, and secret scanners can tell the two apart (a GitHub
  secret-scanning partner registration is possible later).
- Stored as SHA-256 of the whole key. A slow hash is unnecessary for a
  238-bit random secret, and a fast one keeps every API request cheap. The
  first 8 characters after the prefix are stored in clear as the display id
  (`uck_AbCd1234…`), with nothing else.
- Shown **once**, at creation. Named by the user. Revocable, effective at once
  (local lookup, no 60-second bridge cache). Optional expiry. `last_used_at`
  is written at most once a minute per key.
- **Scopes:** `jobs` (submit, cancel, read jobs, events, logs, artifacts) and
  `read` (read only). Billing and key management are never reachable with a
  key. At most 10 active keys per account.
- Creating a key emails the account ("A new API key was created"), so a
  hijacked session that mints a key is noticed.

### 3. Balance: a prepaid USD balance in compute's own Postgres

- The balance lives on the `accounts` row (`available`, `held`) and every
  movement is a row in `balance_entries`. **That table is append-only,**
  enforced by a trigger exactly like `usage_events`. Balance columns and the
  entry are written in one transaction. `available` may not go below zero,
  except through a chargeback (section 4).
- **The internal unit stays the console's quota unit, 500000 per USD 1**
  (USD 0.000002). Every existing metering rule, `sku.EstimateQuota`, the
  ledger's arithmetic and its tests keep working unchanged, and one US cent is
  exactly 5000 units, so Stripe amounts convert without rounding. The unit
  never leaves compute; customers see USD with 4 decimals, as today.
- **Holds become local, with the bridge's semantics.** A new package
  implements `Hold`, `Settle`, `Extend`, `Release` with the same request
  shapes, idempotency rules (`hold_id`, `event_id`, `extend_id` unique and
  replayable) and refusals (`402 insufficient_balance`, `409 hold_exceeded`,
  `hold_closed`, `sku_mismatch`, `cumulative_quota_regressed`,
  `idempotency_conflict`) as `console-billing-bridge.md`. The ledger depends on
  an interface both the console client and the local biller satisfy. **The
  ledger's own logic (H1–H7, R1–R6, RS1–RS7) does not change.** The fake
  console's contract tests run against the local biller too, so "same
  semantics" is tested, not asserted.
- Per-key spend limits (the console's token `remain_quota`) are not carried
  over. A per-key monthly cap is a later, separate change if anyone asks.

#### What changes in ADR-0008 and metering.md

- **H5 / `billing_unavailable` almost disappears.** The biller is our own
  Postgres, and the job store is on the same database, so "billing unreachable
  while the job store works" is no longer a real state. The code path stays
  (a database error during settle is still possible); the "console outage of
  ~50 minutes stops running jobs" consequence of ADR-0008 goes away.
- **H1 and H3 stay as they are.** The rolling hold was sized partly for
  console outages and partly so a long job does not lock the whole balance.
  The second reason still holds. Shrinking the runway is a later metering
  change, not part of this one.
- **R5 and R6 become implementable without anyone else's change.** They
  waited on the console accepting a hold with `token_id: 0` and no `job_id`
  (metering.md, "Proposed changes to the bridge"). A local hold against the
  account needs no key.
- **ADR-0008 open point 1 closes:** compute has the customer's email address
  and sends arrears and deletion notices itself.
- **"No card-on-file distinction" stays.** Top-ups are one-off Checkout
  payments; we do not store payment methods.
- The consume-log entries the console wrote (`model_name = compute/<sku>`)
  become `balance_entries` rows of kind `settle`, with the job, SKU and GPU
  seconds as references. The dashboard's usage view reads those.

### 4. Payments: Stripe Checkout top-ups, designed fresh

**Provider: Stripe Checkout** (hosted payment page, `mode=payment`, one-off,
cards only).

- **PCI scope is SAQ-A**, the smallest. Card data never touches our pages or
  servers. We redirect to `checkout.stripe.com`, so no Stripe.js and no card
  field is embedded in our pages.
- No Stripe SDK is needed. Creating a Checkout Session is one form-encoded
  POST, and verifying a webhook is HMAC-SHA256 over `timestamp.payload`, both
  stdlib. (`stripe-go` is MIT and allowed if the surface grows.)
- **Merchant of record compared.** Paddle (5% + USD 0.50 per transaction,
  published 2026-10-03) and Lemon Squeezy (similar pricing; owned by Stripe
  since 2024) act as merchant of record. They collect and remit VAT and sales
  tax worldwide and issue the tax invoices. That removes most of the tax work
  below, at about 2% more per transaction than Stripe US and somewhat more than
  Stripe Malaysia, and with less control over the checkout. Whether either
  accepts **prepaid usage credit for GPU compute** has to be confirmed with
  them before choosing; MoRs commonly restrict stored-value and resale-prone
  products. Stripe's own merchant-of-record offering (Managed Payments) is
  worth asking about in the same conversation. **Recommendation: Stripe
  Checkout now, with tax handled as in "Taxes" below. Revisit MoR if legal
  advice says the tax burden of selling to consumers abroad is too high for
  us to carry.** Operator decision D1.

**Flow.**

1. The dashboard posts an amount. compute-api checks the limits below, creates
   a `topups` row (`tu_…`, state `open`), creates the Checkout Session with
   that id as `client_reference_id` and in `metadata`, an idempotency key
   derived from the top-up id, `payment_method_types=card`, the account's
   Stripe Customer (created on first top-up), and returns the session URL.
2. The browser goes to Stripe and back to
   `app.compute.unifyai.us/billing?topup=tu_…`. **The return page never
   credits anything.** It polls the top-up's state.
3. **Only the webhook credits.** `checkout.session.completed` with
   `payment_status = paid` credits the balance in one transaction. That
   transaction inserts the `stripe_events` row (unique on the Stripe event id),
   moves the top-up to `credited` (unique on the Checkout Session id), and
   writes a `topup` balance entry whose idempotency key is the top-up id. A
   redelivered event, or a second event for the same session, changes nothing
   and answers `200`. A bad signature, or a timestamp more than 5 minutes off,
   is `400` and nothing is recorded.
4. A receipt email goes out from compute (section 6) with Stripe's hosted
   `receipt_url`. That URL is also in the dashboard.

**Events handled:** `checkout.session.completed`, `checkout.session.expired`
(top-up → `expired`), `charge.refunded`, `charge.dispute.created`,
`charge.dispute.closed`. Cards only, so the asynchronous-payment events
(`checkout.session.async_payment_*`) cannot occur. Everything else is
acknowledged and ignored.

**Failed payments** never create a balance entry. Stripe's page handles
declines and retries; an abandoned session expires after 24 h.

**Refunds** are operator-initiated (admin CLI), of unused balance only, at
most the top-up's amount, to the original card through Stripe's refund API.
The debit is written when the refund is created and confirmed by
`charge.refunded`. Customer-facing refund terms are a legal item (D6).

**Disputes (chargebacks).** On `charge.dispute.created` the disputed amount is
debited at once, because Stripe takes it at once. If that drives `available`
below zero, the account is **suspended**: running jobs are cancelled
(`balance_exhausted`), new submits and top-ups refused, and the operator is
emailed. A dispute won (`charge.dispute.closed`, status `won`) reinstates the
amount; the suspension is lifted by the operator, not automatically. A
negative balance is the only way `available` goes below zero.

**Limits (start small).**

| Limit | Value | Why |
|---|---|---|
| Minimum top-up | USD 10 | Below that, Stripe's fixed fee is over 3% of the payment |
| Maximum per top-up | USD 100 | The whole platform's GPU budget is USD 50 a month (ADR-0010); nobody can spend more than that yet |
| Maximum balance (available + held) | USD 200 per account | Bounds what a stolen card can turn into credit, and what we would refund |
| Top-ups per account per day | 3 | Card-testing velocity |
| Email verified | required before any top-up | |

All are per-account columns with these defaults; raising one for an account
is an admin-CLI action with a reason, written to the audit log. **That is the
"manual review before large top-ups":** a request over a limit is refused with
`topup_limit_exceeded` and a message to contact us, and the operator decides.
**No auto-recharge, no saved cards, no postpaid, no invoicing terms.**

**Fraud risk with GPU resale.** Stolen cards buying GPU time (for mining or
resale) is the expected attack on a prepaid GPU service. Defences, in order:
prepaid only (we never extend credit), cards only (no bank debits that reverse
days later), the limits above, `compute_enabled` off by default while starting
small, Stripe Radar's default rules plus "block if CVC or postal code check
fails" and 3-D Secure requested when Radar asks for it, one Stripe Customer per
account, and an acceptable-use policy that forbids mining (legal, D6). The
platform's one-job, 4-hour, USD 50-a-month GPU caps (ADR-0010) bound the damage
even if all of these fail.

**Receipts and invoices.** Stripe receipts (hosted `receipt_url`) and our
own receipt email. Stripe's post-payment invoices on Checkout
(`invoice_creation`) cost 0.4% (cap USD 2) per invoice on a US account and
are **off** until a business customer needs a tax invoice.

**Taxes: flagged for legal and accounting, not decided here (D2).** The
contracting entity named in UI-STANDARD is **UnifyAI, Petaling Jaya,
Malaysia**, with governing law California. Customers will mostly be in the
US, and possibly the EU and Malaysia. Open questions for an accountant:
Malaysian service tax on digital or IT services sold by a Malaysian entity,
domestically and abroad; US state sales tax on IaaS/compute, and economic
nexus thresholds; EU and UK VAT on electronically supplied services to
consumers, which applies from the first sale for a non-EU supplier; and
whether to sell to **businesses only** at first, which moves most of the EU
VAT to the buyer by reverse charge. Stripe Tax (0.5% per transaction where
registered) can calculate and collect once registrations exist. The answer
also decides **which entity owns the Stripe account**. A Malaysian Stripe
account charges 3% + RM 1.00 per domestic card, +1% for international cards
and +2% when currency conversion is needed (published 2026-10-03). A US
account charges 2.9% + USD 0.30, +1.5% international, +1% conversion.

### 5. Where the UI lives

**`app.compute.unifyai.us`**, as UI-STANDARD already says. The routes are
signup, login, verify, forgot and reset password, and a dashboard with
balance, top-up, keys, jobs and usage. All of it is built to UI-STANDARD's "Log
in / Sign up pages" and "App shell" sections: a centred 400px card, the
lockup plus "Compute" descriptor, one amber primary, password show/hide on
every password field, a confirm-password field with "Passwords don't match."
under it, Turnstile directly above the primary, errors in `--u-danger` under
the field, and form-level errors in a bordered box.

**Recommended shape: a second static export, served by the existing `site`
container, with the dashboard API on compute-api, joined at the ALB.**

- The dashboard is a client-rendered app built from this repository with the
  same toolchain and tokens as the marketing site (vinext static export,
  `app/unifyai-tokens.css`, the generated shadcn components), into the same
  `site` image. Caddy serves it on the `app.compute.unifyai.us` host.
- One new ALB listener rule: host `app.compute.unifyai.us` **and** path
  `/dashboard/v1/*` → the existing compute-api target group. Everything else
  on that host → the existing site target group. One new DNS alias and one
  new certificate SAN (`create_before_destroy` already covers replacing the
  certificate).
- So the dashboard and its API are **one origin**: no CORS, and a host-only
  `__Host-` cookie. compute-api refuses `/dashboard/v1/*` unless the `Host` is
  the app host, and refuses state-changing requests whose `Origin` is not it.
- **No new container, no new process, no new instance size.** A separate
  Next.js server was rejected: it is another Node process on a 2 GB host, for
  server rendering nothing here needs. Go `html/template` pages in compute-api
  were rejected: they would duplicate the brand standard outside the
  component library this repository already uses.
- The marketing site's "Log in" / "Sign up" links point here only once the
  CI/CD agent says it is live (AGENTS.md: site copy promises nothing unbuilt).
- notifications.md's job link `https://compute.unifyai.us/jobs/{job_id}`
  should become `https://app.compute.unifyai.us/jobs/{job_id}`, since that is
  where a logged-in customer is. That is a change to an operator decision of
  2026-10-01 (D7).

### 6. Email

- **Amazon SES, its own identity `compute.unifyai.us`**, Easy DKIM (three
  CNAMEs in the `unifyai.us` Route53 zone), a custom MAIL FROM
  (`bounce.compute.unifyai.us`) so SPF aligns, and a DMARC record
  (`_dmarc.compute.unifyai.us`, `p=none` with reports first, then
  `quarantine`). Its own configuration set, `unify-compute`, as the
  identity's **default**, with reputation metrics and an event destination,
  as Unify Data does. **The instance role needs `ses:SendEmail` on both the
  identity ARN and the configuration-set ARN.** Unify Data learned that
  granting the identity alone fails every send.
- **No sandbox exit is needed.** SES production access is per account and
  region. Account 871376691052 in us-east-1 already has it: checked
  2026-10-03, `ProductionAccessEnabled: true`, 50,000 messages per 24 h,
  14/s, status `HEALTHY`. The consequence Unify Data records applies to us
  too: a suspension earned by any product in the account stops our signup
  codes. The configuration set gives per-product metrics and a kill switch,
  not a separate blast radius.
- **Sender:** `Unify Compute <no-reply@compute.unifyai.us>` (notifications.md,
  email kit). The value lives in the host env, quoted (copied from Unify
  Data: `.env` is sourced by a shell, so a value with a space or `<` must be
  single-quoted).
- **Sending without an SDK.** SES v2 `SendEmail` is one SigV4-signed JSON
  POST. compute-api already signs STS and S3 calls with stdlib
  (`internal/awsv4`), so the mailer adds no dependency.
- **Every message is multipart: plain text plus HTML** in
  `docs/spec/email/base.html`, with the HTML built from the same content so
  the two cannot drift (copied from Unify Data). English only; one layout for
  any later locale.
- **Development writes to an outbox directory instead of sending** (copied
  from Unify Data). **Unlike Unify Data, a deployment with local accounts and
  no `SES_SENDER` refuses to start unless `ENVIRONMENT=development`.** Data's
  outbox fallback in production means signups that silently never verify.
- The message list is in `spec/accounts.md`, "Emails".

### 7. Migration off the bridge

- compute-api gains **`ACCOUNTS_BACKEND=console|local`**. `console` is today's
  behaviour; `local` uses compute's own keys, accounts and biller. The ledger
  and the job service take the biller interface; `auth` picks the verifier by
  the flag. No mixed mode. With no customers, accepting both `sk-` and `uck_`
  keys at once would be complexity for nobody.
- **Order:** build and merge behind the flag (default `console`) → switch
  **staging** to `local` and run the vertical slice there → switch
  **production** → after one clean release on both, **remove the bridge**
  from compute: `internal/console`, `consoletest`, `CONSOLE_URL`,
  `COMPUTE_BRIDGE_SECRET`, the bridge SSM parameters, and the flag itself.
- **Precondition for each switch:** no hold in a non-terminal state on that
  host (`SELECT count(*) FROM holds WHERE state IN ('pending','open','closing')`
  is 0). Production has never placed one. Staging's console-era jobs keep
  their `user:`/`tenant:` owners and become unreachable; staging data is
  disposable by design (ADR-0009), so the switch may also start staging's
  database empty.
- **The staging console container and Redis are removed** (the
  `docker-compose.console.yml` overlay, the console target group, ALB rule and
  DNS record, `console_image`, and the console's SSM parameters). That frees
  memory on staging. **It does not allow t4g.small again:** staging is
  t4g.medium because of the SkyPilot API server (ADR-0010, "Fixed cost"), not
  the console. Production already runs no console, so nothing changes there.
- **The bridge code in `unifyapi-console` is left dark, not deleted.** Its
  routes answer a bodiless `404` once `COMPUTE_BRIDGE_SECRET` is unset on
  `app.unifyapi.ai`. Unsetting it is the **UnifyAPI CI/CD agent's** release,
  scheduled with them. How that console host takes its secrets has to be
  checked first, since a change to its `user_data` restarts production and
  rotates its egress IP. Deleting the files from the fork is optional and
  later. They are new files, so they cost little at upstream merges.

### 8. Security

- **Password hashing: argon2id** through `golang.org/x/crypto/argon2`
  (BSD-3-Clause, allowed by ADR-0002, recorded in `THIRD-PARTY-LICENSES.md`
  in the same commit). Parameters: OWASP's minimum, m = 19 MiB, t = 2,
  p = 1, encoded in PHC string form so they can be raised and old hashes
  rehashed at next login. At most 4 hashes run at once, so a burst of logins
  cannot take the host's memory (19 MiB each, on a 2 GB host). Rejected:
  stdlib `crypto/pbkdf2` (Go 1.24; not memory-hard) and scrypt at Unify Data's
  `n=16384` (below current guidance). Password rules are copied from Unify
  Data: at least 12 and at most 128 characters, not the email or its local
  part, at least 5 distinct characters. A breached-password check (HIBP
  k-anonymity) is later and optional.
- **Unknown email at login takes as long as a wrong password:** a hash is
  verified against a fixed dummy hash, as the gateway's local auth does.
- **No account enumeration.** Signup always answers `202` "check your email".
  If the address is already registered, that address gets a "someone tried to
  sign up with your address" email instead of a code. **Unify Data answers
  `409 email_taken` here; we do not copy that.** Login answers
  `invalid_credentials` for unknown email and wrong password alike, and
  `email_unverified` only after a correct password. Forgot-password always
  answers `202`.
- **Rate limits**, counted in Postgres (`auth_attempts`) so they survive
  restarts and rolling releases, keyed on the client IP and on the account.
  compute-api sits directly behind the ALB, which **appends** the connecting
  address to `X-Forwarded-For`, so the client is the **last** entry, never the
  first (the first is whatever the client sent). This is Unify Data's
  proxy-IP lesson. Login: 20 per IP and 50 per account per 10 minutes. The
  account limit is only a backstop, so a stranger cannot lock the owner out;
  there is no hard lockout. Signup and forgot-password: 5 per IP per hour.
  Verify-code: 5 wrong per code, 20 per IP per hour.
- **Session cookie:** `__Host-uc_session`, random 32 bytes, stored as SHA-256
  (as Unify Data does), `Secure; HttpOnly; Path=/; SameSite=Lax`. **Lax, not
  Unify Data's Strict:** with Strict, the browser drops the cookie on the
  top-level redirect back from `checkout.stripe.com`, and the customer lands
  on the billing page logged out. Idle timeout 12 h, absolute 7 days. Sessions
  rotate at login and at every privilege change. Changing the password ends
  every other session.
- **CSRF:** every non-GET dashboard request carries `X-CSRF-Token`, which must
  match the session's token (Unify Data's synchronizer pattern), **and**
  passes an `Origin` check against the app host. Lax plus both checks.
- **Recent password required** for changing password or email, closing the
  account, and creating a key more than 10 minutes after login.
- **Audit log:** `audit_events`, append-only by trigger, for every login
  (success and failure), signup, verification, reset, password change, key
  created or revoked, top-up, refund, dispute, suspension and admin action.
  Admin actions require a reason (Unify Data's `staff_actions`).
- **Admin surface: a CLI, not a web page.**
  `compute-api admin <verb>`, run on the host over SSM (as the CI/CD agent
  already reaches it). It credits and debits with a reason, enables compute,
  raises limits, suspends, and refunds. No admin passwords and no admin web
  routes exist to attack. An admin UI is later.
- **Data export and deletion (GDPR-style).** The dashboard offers a JSON export
  of the account: profile, keys' metadata, jobs, balance entries, top-ups.
  Closing an account revokes keys, ends sessions, cancels jobs, deletes
  artifacts and replaces the email with a tombstone. Balance entries,
  top-ups and the audit log are **kept** for the statutory accounting period
  (legal to confirm; D6). Unused balance at closing is refunded per the
  refund terms.
- **Privacy policy and terms.** The existing `/privacy` page must add Stripe
  (payments), Cloudflare (Turnstile) and AWS SES (email) as processors, and
  what account data compute now holds. Terms of service, an acceptable-use
  policy and a refund policy for prepaid credit are new. All of these need
  legal review before the first real top-up (D6).
- **MFA** (TOTP) is not in the first version. It is a precondition for
  raising any limit above the start-small defaults for an account.

### 9. Start small

Restated in one place: no auto-recharge, no saved cards, no postpaid; top-ups
USD 10–100, balance at most USD 200, 3 top-ups a day, raised only by the
operator; `compute_enabled` off by default while the platform cap is one job;
Stripe in test mode until legal review and the operator's go-ahead; the GPU
caps of ADR-0010 unchanged.

## Cost impact

| Piece | Monthly cost at start-small volume | Notes |
|---|---|---|
| SES | ~USD 0.10 | USD 0.10 per 1,000 messages; fewer than 1,000 a month expected. No dedicated IP |
| SES configuration set and event destination | cents | CloudWatch metrics |
| Route53 records (DKIM ×3, MAIL FROM MX/TXT, DMARC, `app.` alias) | USD 0 | Existing zone; alias queries free |
| Turnstile | USD 0 | Free plan |
| ALB rule, certificate SAN | USD 0 | Within the existing ALB's included rules and LCUs; ACM is free |
| New container or instance | USD 0 | None; the dashboard is static files in the existing `site` container |
| Postgres tables | USD 0 | Existing data volume |
| Staging console + Redis removed | USD 0 saved | Frees memory; staging stays t4g.medium for SkyPilot |
| Stripe fixed fees | USD 0 | No monthly fee for Payments or Checkout |
| **Stripe per top-up** (US account) | 2.9% + USD 0.30; +1.5% international card; +1% conversion | USD 20 domestic top-up: USD 0.88 |
| **Stripe per top-up** (Malaysian account) | 3% + RM 1.00; +1% international card; +2% conversion | USD 20 from a US card: ~USD 1.02, ~USD 1.42 if converted |
| Stripe dispute | USD 15 (US) / RM 90 (MY) each | Plus the disputed amount if lost |
| Stripe Radar | included (US Radar Lite); MY lists "from RM 0.23 per screened transaction" | Confirm for the chosen account |
| Stripe Tax, if enabled | 0.5% per transaction | Only once registered somewhere |
| Stripe Invoicing, if enabled | 0.4%, cap USD 2 per invoice | Off at start |
| Paddle, for comparison | 5% + USD 0.50 | USD 20 top-up: USD 1.50, tax handled |

Fees are the published list prices read on 2026-10-03. At start-small volume
(a handful of top-ups a month, capped at USD 100 each) Stripe's cost is a few
dollars a month. **The new fixed infrastructure cost is under USD 1 a month.**
The real cost is engineering time and the legal and tax review.

## Consequences

- ADR-0001 is superseded once this is accepted. AGENTS.md's "What is
  decided" list, `architecture.md`, `product-design.md`, `job-api.md`
  (authentication), `quotas.md` (owners become `account:<id>`),
  `notifications.md` (compute delivers; the footer reason and safety lines
  stop mentioning the Unify API console), `metering.md` and ADR-0008 are
  updated by the tasks that change the behaviour they describe
  (`plan-own-accounts.md`, task A9). **Until then they describe the bridge,
  which is what runs.**
- A customer of both Unify API and Unify Compute has two logins, two
  balances and two sets of receipts.
- Compute owns a security- and money-sensitive surface for good: password
  storage, sessions, payment webhooks, refunds, disputes. Mistakes there are
  ours, and a payment bug is a money bug.
- **Payments are the riskiest item in the plan,** and no one in the family has
  built them outside the AGPL console. Nothing from the console's top-up code
  may be read for reuse; its behaviour may be studied from outside or from
  its documentation. Stripe stays in test mode until the operator approves.
- The bridge contract (`console-billing-bridge.md`) becomes historical after
  the cutover. It is marked so, not deleted, because the ledger's semantics
  are defined against it.
- Organisations, invitations, third-party sign-in, MFA, an admin web UI, saved
  cards, auto-recharge and invoices are explicitly later.

## Decisions the operator must make

| # | Decision | Recommendation |
|---|---|---|
| D1 | Stripe direct, or a merchant of record (Paddle / Lemon Squeezy / Stripe Managed Payments) | Stripe Checkout, unless D2's answer makes consumer tax abroad too heavy |
| D2 | Which entity contracts with customers and owns the payment account (UnifyAI Malaysia, or another), where it registers for tax, and whether to sell to businesses only at first | Ask an accountant before Stripe live mode; consider businesses-only at first |
| D3 | `compute_enabled` default while the platform runs one GPU job at a time | Off; enable accounts by hand |
| D4 | Top-up limits (USD 10 min, 100 max, balance 200, 3 a day) | As proposed |
| D5 | Turnstile (UI-STANDARD) or Unify Data's proof-of-work, which sends nothing to a third party | Turnstile plus honeypot |
| D6 | Terms, acceptable use (no mining), refund policy for prepaid credit, privacy-policy processors, record retention | Legal review before the first live top-up |
| D7 | Job link in emails moves to `app.compute.unifyai.us/jobs/{id}` | Yes |
| D8 | Session lifetime: 12 h idle, 7 days absolute | As proposed |


## Operator decisions (2026-10-03)

Recorded from the operator's answers in the coordinating session. Where they
differ from the recommendations above, these win.

| # | Decision |
|---|---|
| D1 | **Stripe directly** (Checkout), not a merchant of record. |
| D2 | **FelixSphere LLC (US)** contracts with customers and owns the Stripe account. Where it registers for tax, and business-only vs consumers, are still for an accountant before live mode (O5). Consequence: Compute's terms and privacy policy must name FelixSphere LLC as the contracting entity and data controller, which differs from the "UnifyAI · Petaling Jaya" company line chosen for the brand on 2026-10-02 — reconcile the privacy page and footer wording before the first live top-up (A9/O5). |
| D3 | `compute_enabled` **off by default**; enabled by hand per account. |
| D4 | Top-up limits **as proposed** (USD 10–100 per top-up, balance cap 200, 3 a day). |
| D5 | **No Turnstile for now.** Bot defence at launch is the honeypot field, per-IP and per-email rate limits, and the emailed verification code; signups cannot spend until D3's switch is turned on by hand. Revisit (Turnstile or proof-of-work) before self-serve signup is opened publicly. The privacy policy does not list Cloudflare as a processor while Turnstile is off. This departs from UI-STANDARD's "Turnstile above the primary"; the brand owner has been told. |
| D6 | Legal review still required before the first live top-up (unchanged). |
| D7 | Job links in emails move to `app.compute.unifyai.us/jobs/{id}` (as proposed). |
| D8 | Sessions: 12 h idle, 7 days absolute (as proposed). |
