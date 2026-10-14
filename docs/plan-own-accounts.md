# Plan: Unify Compute's own accounts, keys, balance and payments

- **Status:** Plan, nothing started. Implements
  [ADR-0011](adr/0011-compute-owns-accounts-and-billing.md) (**Proposed**)
  against [`spec/accounts.md`](spec/accounts.md). No task starts before the
  operator accepts ADR-0011 (O1).
- Every task is one agent, one worktree, one branch, one PR (AGENTS.md).
  "Touches" lists the paths a task expects to change, so two parallel tasks
  can see whether they collide. Every PR passes the hand-off checks in
  AGENTS.md. Merging is not releasing. The unify-compute CI/CD agent ships,
  staging first.
- Every new `compute-api` dependency is MIT, Apache-2.0 or BSD and recorded in
  `THIRD-PARTY-LICENSES.md` in the same commit. The only one planned is
  `golang.org/x/crypto` (BSD-3-Clause). Nothing is read from
  `unifyapi-console` for reuse (ADR-0002).

## Order

**Wave 1 is the vertical slice:** signup → verify → login → key → manual
credit by the operator → submit a job billed locally, on staging. **Stripe
comes after it** (wave 2), then the production cutover and the bridge's
removal (wave 3).

```
O1 accept ADR ─┬─ A1 schema+accounts ──┬─ A4 auth routes ──┬─ A5 keys ── A5b members ┐
               │                       │                   │                 │
               ├─ A2 mailer ───────────┘                   │                 ├─ A8 staging slice ── C1 prod cutover ── C2 remove bridge
               ├─ A3 SES identity (TF) ── (O4 apply) ──────┤                 │                                         C3 staging console out
               ├─ A6 local biller + admin CLI ─────────────┼─────────────────┤
               └─ A7a app host (TF, Caddy) ── A7b auth pages ── A7c dashboard┘
                                                                     │
                                       O2 Stripe test keys ── B1 top-ups ── B2 refunds/disputes
                                                                     ├─ B3 job + arrears emails
                                                                     └─ B4 account settings, export, close
A9 doc updates: alongside A5/A6 (each PR updates the specs it changes), closed out at A8
C4 marketing Log in / Sign up links: after the CI/CD agent says the app is live
```

Wave 1 parallelism: A1, A2, A3, A6 and A7a can start together once O1 is done.
A6 touches the ledger and A1 touches the store migrations; they agree up front
on migration numbers (A1 takes `0005`, A6 takes `0006`).

## Operator-only steps

| # | Step | Needed by | Notes |
|---|---|---|---|
| O1 | Accept ADR-0011 and answer D1–D8 | everything | Edits ADR-0011's Status; AGENTS.md "What is decided" follows in A9 |
| O2 | **Stripe account**: the existing account Unify API runs production on (decided 2026-10-03: KYC, bank and receipts already done). Create compute's own restricted test key and its own staging webhook endpoint, put both in SSM (`docs/staging.md`, "Stripe top-ups"). Radar rules and the statement descriptor are account-wide, shared with Unify API: change them only with its owner | B1 | Test mode only until O6 |
| O3 | **Turnstile**: create a widget in a Cloudflare account for `app.compute.unifyai.us` and `app.staging.compute.unifyai.us`; put the site key and secret in SSM | A8 (A4 can merge with test keys) | Free. DNS stays in Route53; Turnstile does not need Cloudflare DNS |
| O4 | **SES**: approve applying `deploy/aws/shared/ses-compute` (identity, DKIM, MAIL FROM, DMARC, configuration set). **No sandbox exit**: the account already has production access (checked 2026-10-03: 50,000/24 h, healthy) | A8 | Shares account-level reputation with Unify API and Unify Data |
| O5 | **Legal**: terms of service, acceptable-use policy (no mining), refund policy for prepaid credit, privacy-policy processors (Stripe, Cloudflare, AWS SES), record retention; and **tax**: Malaysian service tax, US sales tax, EU/UK VAT, consumer vs business-only (D2) | O6, and C4 | Before the first live top-up |
| O6 | **Stripe live mode**: swap in live keys and the production webhook secret | after B1, B2 on staging and O5 | The first real money |
| O7 | Turn `compute_enabled` on, by hand, for each account allowed to run jobs (D3) | first real job | `compute-api admin account enable-compute` |
| O8 | Ask the Unify API CI/CD agent to unset `COMPUTE_BRIDGE_SECRET` on `app.unifyapi.ai` in a scheduled console release | after C1 | Check first how that host gets its secrets: a `user_data` change restarts production and rotates its egress IP |

## Tasks

### A1 — Accounts schema and the accounts package

- **Depends on:** O1.
- **Touches:** `compute-api/internal/store/migrations/0005_accounts.sql`,
  new `compute-api/internal/accounts/` (users, accounts, sessions,
  email codes, reset tokens, throttling, password hashing),
  `compute-api/go.mod`, `THIRD-PARTY-LICENSES.md`.
- **Does:** the tables `accounts`, `users`, `sessions`, `email_codes`,
  `auth_tokens`, `auth_attempts`, `audit_events` (append-only trigger) from
  `spec/accounts.md`. argon2id hashing with PHC encoding and a concurrency
  limit of 4; the dummy-hash path for unknown emails; password rules; email
  normalisation; Postgres-backed throttle with per-row windows.
- **Done when:** migration re-runs cleanly (the store's concurrent-migrate
  test passes); unit tests cover hashing round-trip, rehash on parameter
  change, the throttle window semantics (Unify Data's bug: one route's short
  window must not reset another's long one), the audit trigger refusing
  `UPDATE`/`DELETE`; `x/crypto` is in `THIRD-PARTY-LICENSES.md`.

### A2 — Mailer

- **Depends on:** O1.
- **Touches:** new `compute-api/internal/mailer/`, `compute-api/internal/awsv4/`
  (an SES v2 `SendEmail` call next to STS and S3), `compute-api/cmd/compute-api/main.go`
  (config only).
- **Does:** multipart text + HTML from `docs/spec/email/base.html`, HTML
  built from the same content as the text; the account messages in
  `spec/accounts.md` "Emails" (verification code, signup attempt, reset,
  password changed, key created, compute enabled); outbox directory when
  `SES_SENDER` is unset; refuses to start in `local` mode without a sender
  unless `ENVIRONMENT=development`.
- **Done when:** golden-file tests for every message's text and HTML; a test
  against an `awsv4test` fake proves the SigV4-signed SES request and the
  configuration set; a manual send to `success@simulator.amazonses.com` from
  staging (in A8) returns success with the outbox absent.

### A3 — SES identity for compute.unifyai.us (Terraform)

- **Depends on:** O1.
- **Touches:** new `deploy/aws/shared/ses-compute/`,
  `deploy/aws/modules/compute-stack/instance.tf` (instance-role policy:
  `ses:SendEmail` on the identity **and** the configuration-set ARNs),
  `Makefile` (`tf-validate` covers the new stack), `docs/staging.md`.
- **Does:** SES identity `compute.unifyai.us`, Easy DKIM CNAMEs, MAIL FROM
  `bounce.compute.unifyai.us` (MX + SPF TXT), `_dmarc.compute.unifyai.us`
  (`p=none; rua=` the operator address), configuration set `unify-compute`
  as the identity's default with reputation metrics and a CloudWatch event
  destination.
- **Done when:** `make tf-validate` passes; the plan shows only additive
  changes; the apply is handed to the CI/CD agent with O4. **Needs the
  operator** (O4) to apply.

### A4 — Auth routes

- **Depends on:** A1, A2.
- **Touches:** new `compute-api/internal/dashboard/` (the `/dashboard/v1`
  handlers and middleware), `compute-api/internal/api/server.go` (mount,
  Host check), new `compute-api/internal/botcheck/` (Turnstile `siteverify`
  plus honeypot), `compute-api/cmd/compute-api/main.go`.
- **Does:** `GET /config`, `POST /signup`, `/verify`, `/verify/resend`,
  `/login`, `/logout`, `/password/forgot`, `/password/reset`, `GET /me`,
  `POST /password`, `/reauth`. Sessions (`__Host-uc_session`, SameSite=Lax,
  hashed at rest, idle 12 h, absolute 7 d), CSRF header plus Origin check,
  client IP from the **last** `X-Forwarded-For` entry, all throttles,
  enumeration-safe answers, signup rolled back if SES refuses the code,
  audit events.
- **Done when:** tests (with Turnstile's always-pass and always-fail test
  keys) cover every error code in the spec's table for these routes. Also
  covered: signup on a registered address returns `202` and sends the
  signup-attempt email, never `409`; login timing does not depend on whether
  the email exists (same hash call count); a reset ends every session; a
  spoofed first `X-Forwarded-For` entry does not change the throttle key; a
  cross-origin POST is refused; `/dashboard/v1` on the API host is `404`.

### A5 — API keys (`uck_`)

- **Depends on:** A4.
- **Touches:** `compute-api/internal/accounts/` (keys),
  `compute-api/internal/dashboard/` (`GET/POST/DELETE /keys`),
  `compute-api/internal/api/auth.go` (verifier chosen by `ACCOUNTS_BACKEND`;
  `uck_` shape check; scope enforcement), `compute-api/internal/quota/config.go`
  (accept `account:<id>`), `compute-api/internal/ratelimit` (key on key id),
  `docs/spec/job-api.md` and `docs/spec/quotas.md` (authentication and
  owners), `compute-api/internal/api/openapi.json` + `docs/spec/openapi.json`.
- **Done when:** a created key authenticates `/compute/v1/jobs` as
  `account:<id>`; a revoked key is `401 invalid_key` on the next request (no
  cache); a `read` key gets `403 scope_denied` on submit; the 11th key is
  `403 key_limit_reached`; the secret appears in exactly one response; the
  "key created" email is sent; with `ACCOUNTS_BACKEND=console` every existing
  test still passes unchanged.

### A5b — Members and invitations

- **Depends on:** A4, A5. Required before C1 (production cutover): adding
  roles after customers exist means migrating every account's single user to
  `owner` under load; before, it is a default.
- **Touches:** a new migration (`invitations`, `accounts.name`, the role
  check on `users.role`), `compute-api/internal/accounts/` (members,
  invitations, the last-owner rule), `compute-api/internal/dashboard/`
  (`/members*`, `/invitations*`, `PATCH /account`, `role` and `name` in
  `/me`), `compute-api/internal/mailer/messages.go` (three messages),
  `compute-api/internal/accounts` key revocation (admin and owner may revoke
  any key), `docs/spec/accounts.md` (already specifies it), `dashboard/`
  pages `/members` and `/accept-invitation` (A7c or a follow-up).
- **Done when:** an owner invites an address, the invitee accepts without a
  verification code and lands logged in as `member`; the invitee's keys
  authenticate jobs as the same `account:<id>` and draw the same balance;
  a `member` gets `403 permission_error` on `POST /members/invitations` and
  `POST /topups`; demoting or removing the last owner is `409 last_owner`;
  the 21st member is `403 member_limit_reached`; an accepted, expired or
  revoked token is `404 not_found`; an address already on another account is
  `409 email_in_use`; the three emails pass the golden-file tests; every
  member action writes an `audit_events` row with the acting user.

### A6 — Local biller and the admin CLI

- **Depends on:** O1. Agrees migration number with A1.
- **Touches:** new `compute-api/internal/billing/` (balance, local holds),
  `compute-api/internal/store/migrations/0006_billing.sql`,
  `compute-api/internal/ledger/ledger.go` and `resume.go` (depend on a
  `Biller` interface instead of `*console.Client`; no logic change),
  `compute-api/internal/jobs/jobs.go` (wiring; `compute_enabled` and account
  status checks at submit), `compute-api/cmd/compute-api/main.go`
  (`ACCOUNTS_BACKEND`, the `admin` subcommand).
- **Does:** `balance_entries` (append-only), `billing_holds`,
  `billing_settlements`, `billing_extensions`, the hourly expiry sweep;
  `Hold/Settle/Extend/Release` with the bridge's idempotency and refusals;
  `compute-api admin account|balance|audit` verbs (refund waits for B2).
- **Done when:** the console fake's contract tests
  (`internal/console/consoletest` and the ledger's harness) run against the
  local biller with the same expected results; `resume_test.go` passes with
  the local biller; the balance invariant (sum of deltas = columns) holds
  after a randomised sequence of holds, settles, extends and releases;
  `admin balance credit` writes an `admin_credit` entry and an audit event
  with its reason, and refuses without `--reason`.

### A7a — The app host (Terraform and Caddy)

- **Depends on:** O1.
- **Touches:** `deploy/aws/modules/compute-stack/` (`app_domain` variable,
  certificate SAN, Route53 alias, an ALB rule: host = app domain and path =
  `/dashboard/v1/*` → api target group, at a priority above the site rule;
  the host added to the site rule's aliases), `deploy/aws/envs/*/main.tf`,
  `deploy/site/Caddyfile` (a host block for the dashboard export with an
  **enforced** CSP: `'self'` plus `challenges.cloudflare.com` for Turnstile;
  no HubSpot), `deploy/site/Dockerfile`, `deploy/site/smoke.sh`.
- **Done when:** `make tf-validate`; the plan for staging and production
  shows the certificate replaced with `create_before_destroy` and no instance
  replacement; Caddy serves a placeholder page on the app host locally;
  smoke covers both hosts.

### A7b — Log in, Sign up, verify and reset pages

- **Depends on:** A7a; A4 for the real API (it can be built against a mock).
- **Touches:** a new dashboard app in this repository (proposed `dashboard/`,
  same vinext static-export toolchain and `app/unifyai-tokens.css`), the
  `site` image build.
- **Does:** exactly UI-STANDARD's "Log in / Sign up pages": centred 400px
  card, lockup + "Compute", H1s "Log in to Unify Compute" / "Create your Unify
  Compute account", show/hide on every password field (`Eye`/`EyeOff`,
  `aria-pressed`), Confirm password with "Passwords don't match." on blur and
  submit, Turnstile directly above the one amber primary, field errors in
  `--u-danger`, the form-level error box, "Forgot password?" on the password
  label row, the legal line, the switch link, "Check your email" with the
  6-digit mono code field, "Reset your password", `© <year> UnifyAI · Privacy
  · Terms` under the card.
- **Done when:** the brand kit's `check-consistency.sh` passes; screenshots
  at 375px and desktop, light and dark, show no horizontal scroll; keyboard
  and screen-reader labels checked; every API error code renders in the
  right place.

### A7c — Dashboard

- **Depends on:** A7b, A5, A6.
- **Touches:** `dashboard/`, `compute-api/internal/dashboard/` (`/balance`,
  `/transactions`, `/jobs*`, `/usage`).
- **Does:** app shell per UI-STANDARD (64px header, 216px sidebar, focal
  number = available balance), Keys (create with one-time reveal, revoke),
  Jobs (list, detail at `/jobs/{id}`, logs, cancel), Usage (month view), a
  Billing page that says "Top-ups are coming; contact us" until B1.
- **Done when:** a signed-in user sees the balance an `admin balance credit`
  gave them, creates and revokes a key, and sees a job submitted with that
  key, with its hold and settled amounts.
- **Split:** the pages merged first (#62); the compute-api routes they call
  (`/balance`, `/transactions`, `/usage`, `/jobs*` including
  `/jobs/{id}/artifacts`) followed as A7d, with an end-to-end test from
  signup to the dashboard showing a settled job.

### A8 — Staging: the vertical slice

- **Depends on:** A3 applied (O4), O3, A4, A5, A6, A7c. Run by the CI/CD
  agent with the implementing agent.
- **Touches:** `deploy/aws/envs/staging/` (`ACCOUNTS_BACKEND=local`, new SSM
  parameters), `scripts/smoke.sh`, `deploy/releases/<date>-staging-own-accounts.md`.
- **Exercise:** sign up on `app.staging.compute.unifyai.us` with a real
  address; receive the code from SES (not the outbox); verify; log in; create
  a key; `admin account enable-compute`; `admin balance credit --usd 5`;
  submit a job with the key. Today the G-instance quota is 0, so the job
  reaches SkyPilot and ends `capacity_exhausted`. Check that the local hold
  was placed and released in full (balance back to USD 5.0000, entries
  `hold` then `release`) and that no console call was made. Once the quota
  exists, rerun with the reference workload and check a settle entry per
  300 s. Then reset a password and confirm the old session is gone.
- **Done when:** the release record lists each step's result; the hold
  precondition (`holds` in `pending/open/closing` = 0) was checked before the
  switch.

### A9 — Docs follow the behaviour

- **Depends on:** O1; done inside A5, A6 and A8 for the parts each changes,
  closed out here.
- **Touches:** `AGENTS.md` ("What is decided": ADR-0011 replaces ADR-0001;
  "Current status"), `docs/architecture.md`, `docs/product-design.md`,
  `docs/spec/metering.md` (biller is local; R5/R6 no longer wait on the
  console), `docs/adr/0008-zero-balance-policy.md` (Status line only, plus an
  amendment section pointing at ADR-0011), `docs/spec/notifications.md`
  (compute delivers; reason and safety lines; job link per D7),
  `docs/spec/console-billing-bridge.md` (Status: historical after C2),
  `.claude/agents/cicd.md` and `.codex/agents/cicd.toml` (bridge release
  ordering removed).
- **Done when:** no doc on `main` describes the bridge as current after C1,
  and none describes local accounts as running before A8.

### B1 — Stripe Checkout top-ups

- **Depends on:** A8, O2.
- **Touches:** new `compute-api/internal/payments/` (Checkout Session
  create, webhook verify and dispatch), `0008_topups.sql` (`topups`,
  `stripe_events`; 0007 went to API keys), `compute-api/internal/dashboard/`
  (`/topups`, and the webhook at `/dashboard/v1/stripe/webhook` on the app
  host, inside the prefix the ALB already sends to compute-api),
  `dashboard/` (Billing page), `compute-api/internal/mailer` (top-up
  received).
- **Status (2026-10-03):** built on a branch except the top-up received
  email; the Stripe account is the one Unify API already uses, so compute
  has its own restricted keys and endpoint and ignores every event that is
  not its own (`spec/accounts.md`, "Stripe webhook"). Operator steps:
  `docs/staging.md`, "Stripe top-ups".
- **Done when:** in Stripe test mode on staging: a USD 10 top-up with
  `4242…` credits exactly 50,000,000 units once; replaying the webhook (Stripe
  CLI `events resend`) changes nothing; a forged signature is `400`; a
  declined card (`4000 0000 0000 0002`) credits nothing; an abandoned session
  expires; limits refuse 9.99, 100.01, a balance over 200 and a fourth top-up
  in a day with `topup_limit_exceeded`; the return page never credits; the
  receipt email links Stripe's receipt. **Needs the operator** (O2).

### B2 — Refunds and disputes

- **Depends on:** B1.
- **Touches:** `compute-api/internal/payments/`, `compute-api/cmd/compute-api`
  (`admin topup refund`), `compute-api/internal/jobs` (cancel on
  suspension), `compute-api/internal/mailer` (refund issued, account
  suspended, operator dispute notice).
- **Status (2026-10-14):** built on `feat/refunds-and-disputes` (migration
  `0012`), tested against the Stripe fake only; not on staging (no Stripe
  keys there). Differs from the line below in two places, by the
  operator's W3 brief: every dispute suspends the account (not only one that
  leaves it negative), and a won dispute lifts the suspension it caused.
  Jobs are cancelled through the normal cancel path (status `cancelled`),
  not with `balance_exhausted`.
- **Done when:** in test mode: a partial refund debits once and is confirmed
  by `charge.refunded`; a refund above `available` is refused; a test
  dispute (`4000 0000 0000 0259`) debits immediately, suspends the account if
  negative, cancels its running job with `balance_exhausted`, emails the
  operator; a won dispute reinstates the amount and leaves the suspension for
  the operator.

### B3 — Job and arrears emails

- **Depends on:** A2, A7c (the job page exists).
- **Touches:** `compute-api/internal/mailer`, `compute-api/internal/jobs`
  (terminal-state hook), `docs/spec/notifications.md`.
- **Done when:** the three messages in `notifications.md` are sent once per
  job (idempotent on job id + message), with the dashboard job link, golden
  tests for both parts. Arrears messages follow when R5 is built.

### B4 — Account settings, export and closing

- **Depends on:** A7c.
- **Touches:** `compute-api/internal/dashboard/` (`/account/export`,
  `/account/close`), `dashboard/`, `compute-api/internal/artifacts` (delete
  an account's prefixes), `app/privacy/page.tsx` (processors, after O5).
- **Done when:** export contains every table's rows for the account and
  nothing of another account; closing revokes keys, ends sessions, cancels
  jobs, deletes artifacts, tombstones the address, keeps balance entries and
  audit rows, and the address can register again.

### C1 — Production cutover

- **Depends on:** A8 green, B1 and B2 on staging (so production never runs
  accounts without a way to refund), O5 at least for terms and privacy.
- **Touches:** `deploy/aws/envs/production/` (`ACCOUNTS_BACKEND=local`,
  `console_url`/`bridge_enabled` off, new SSM parameters),
  `deploy/releases/<date>-production-own-accounts.md`.
- **Exercise:** the A8 sequence on `app.compute.unifyai.us`, with Stripe still
  in test mode unless O6 is done. Confirm that an `sk-` key from
  app.unifyapi.ai is now `401 missing_key`.
- **Done when:** release record written; O8 requested.
- **Status (2026-10-03): done.** Production switched by the CI/CD agent
  (record `deploy/releases/2026-10-03-production-accounts-live.md`). Two
  deviations, both operator decisions: B2 (refunds) is not on staging, and
  Stripe went straight to live (Unify API's live key and a new endpoint),
  with no test phase. O8 recommended, not done (UnifyAPI side).
  `console_url`/`bridge_enabled` were left as they are in Terraform and
  `.env` (local mode ignores them; kept for rollback).

### C2 — Remove the bridge from compute

- **Depends on:** C1 plus one further clean release on both hosts.
- **Touches:** delete `compute-api/internal/console/` and `consoletest`
  (move any contract tests still needed into `internal/billing`),
  `ACCOUNTS_BACKEND`, `CONSOLE_URL` and `COMPUTE_BRIDGE_SECRET` handling,
  `deploy/aws/modules/compute-stack` (`bridge_enabled`, `console_url`, bridge
  SSM parameter), `THIRD-PARTY-LICENSES.md` (the new-api row),
  `docs/spec/console-billing-bridge.md` (marked historical).
- **Done when:** `git grep -i console` in `compute-api/` finds nothing but
  history; all tests pass.

### C3 — Remove the staging console and Redis

- **Depends on:** A8 (staging no longer calls it).
- **Touches:** `deploy/compose/docker-compose.console.yml`,
  `postgres-init-console.sql`, `deploy/aws/modules/compute-stack`
  (`console` app entry, target group, rule, DNS, `console_image`, Redis and
  session secrets), `deploy/aws/envs/staging/`, `docs/staging.md`.
- **Done when:** staging runs compute-api, site, Postgres and SkyPilot only.
  Staging stays **t4g.medium** (SkyPilot needs it; ADR-0010). The record
  states the memory freed.

### C4 — Marketing site links

- **Depends on:** C1, and the CI/CD agent saying the app is live.
- **Touches:** `app/site-header.tsx`.
- **Done when:** "Log in" and "Sign up" in the header point at
  `app.compute.unifyai.us/login` and `/signup` per UI-STANDARD's marketing
  header, at 375px and desktop.

## Effort and risk

| Wave | Tasks | Risk |
|---|---|---|
| 1 (slice) | A1–A9 (A7 is three) | Medium: auth is well understood, and Unify Data shows the shape. The biller is low risk because it reuses the ledger's tested semantics |
| 2 (money) | B1–B4 | **High: payments are designed fresh.** Nobody in the family has built them outside the AGPL console, which may not be copied. Tax and refund terms are outside engineering (O5) |
| 3 (cutover) | C1–C4 | Low: no customers, no production holds |

## Cost

See ADR-0011, "Cost impact". New fixed infrastructure is under USD 1 a
month. Stripe costs per top-up. Nothing in this plan changes an instance
size.
