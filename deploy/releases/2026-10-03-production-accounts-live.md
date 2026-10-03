---
environment: production
action: release
outcome: success
run: 37145149308
commit: ec3fb28d3859d9163ced3d2aee82c08ef4b73f2c
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:d5344254507f4a39eec78076e948d21c98d8446696650da393824006d98701da
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:48aa37ffeca00fcbd85889d3a8d7b780eeea3d15071606ab4bf7c9bc02736146
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:40e08796c20704762896ecef877a2039ce8e2de9524df443db94a366556e5a7f
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:48aa37ffeca00fcbd85889d3a8d7b780eeea3d15071606ab4bf7c9bc02736146
hotfix: false
---

# 2026-10-03 — production on Compute's own accounts (C1); sign-up and login open

Operator decision: "production 也开放注册，login" (open sign-up and login on production too). Plan task C1. Production
has no customers. The site update that links to the app is
[2026-10-03-production-accounts-site](2026-10-03-production-accounts-site.md).

## Host change before the release (production, over SSM, ops lock)

`/data/backups/20261003T183839Z-manual-prod-own-accounts-env/` holds the
previous `.env` and compose file. Steps:

- Installed 6c1d796's `docker-compose.yml` (the #67 and #69 pass-throughs).
- Set `ACCOUNTS_BACKEND=local`, `APP_ORIGIN=https://app.compute.unifyai.us`,
  `SES_SENDER='Unify Compute <no-reply@compute.unifyai.us>'`,
  `SES_CONFIGURATION_SET=unify-compute` and `ENVIRONMENT=production`.
  `CONSOLE_URL` and the bridge secret are left in place: local mode ignores them, and keeping them makes rollback one
  step.
- No user_data change, no instance stop or start. The GitHub production
  variable `APP_ORIGIN` is set, so release smoke probes the app host.

## The release

Gate: `staging ran exactly these digests` (run 37145018753). Backup
`/data/backups/20261003T184209Z-37145149308` (S3 copy). Smoke, including the
app host, and `served-by-aws.sh` passed. Boot log: `accounts_backend local`,
`mailer sends through SES`, `/dashboard/v1 served on the app host`,
`CONSOLE_URL is ignored`, `SKYPILOT_API_SERVER_URL unset` (GPUs off).
Migrations 0001 to 0008 ran on production's database.

## Stripe, live, added right after (operator decision, no test phase)

unifyai-cicd wrote `/unify-compute-production/stripe_secret_key` (Unify API's
live `sk_live_` key, shared) and `stripe_webhook_secret` (`whsec_…`) for the
new live endpoint `we_1UMY6T08R23kVnSzvjZa1tSI` →
`https://app.compute.unifyai.us/dashboard/v1/stripe/webhook`. Stripe
delivers account-wide events there, Unify API's included.

- Before enabling: read the handler. A checkout event whose metadata is not
  `product`=compute and `environment`=production, a refund or dispute for a
  payment that is not a compute top-up, and an event from the other mode all
  get **200 `ignored`, stored nothing**. Only a bad signature gets 400.
- Set both in production's `.env`, read on the host with the instance role,
  never printed, prefixes checked (`sk_live_`, `whsec_`). Backup
  `/data/backups/20261003T185838Z-manual-prod-stripe-env/`. Recreated
  compute-api only, under the lock. The boot log now shows `stripe top-ups
  enabled livemode=true environment=production`.
- The endpoint was created at 18:56:13Z, and production had its secret from
  18:58:40Z. No event occurred in between, and Stripe lists 0 undelivered
  events in 24 h.
- Signed test (with the real secret, posted to the live endpoint): a foreign
  `checkout.session.expired` got 200 `{"outcome":"ignored"}`; a wrong
  signature got 400 `invalid_signature`.
- End to end through Stripe: created an unpaid live Checkout session (USD 1,
  no compute metadata, `metadata.purpose=compute-webhook-verification-2026-10-03`)
  and expired it at once (`cs_live_a1t1EIxzLFfveNf2…`, `payment_status:
  unpaid`). No money moved, and it is not a top-up. Event
  `evt_1UMYA508R23kVnSzSiPXNMuO`: `pending_webhooks=0` (every endpoint
  answered 2xx). Compute logged `POST /dashboard/v1/stripe/webhook 200` at
  18:59:57Z, and `stripe_events` and `topups` both stayed at 0 rows.
- Unify API received the same event. Per the coordinator's reading of its
  handler (`controller/topup_stripe.go`, `model/topup.go` Recharge), it logs
  "充值订单不存在" (order not found) and returns 200. Expect that line on
  app.unifyapi.ai for this event and for every future compute checkout; it
  cannot credit Unify API.
- No real top-up was run. The operator does the first one.

## What was exercised on production (from outside unless noted)

- `https://api.compute.unifyai.us/healthz`: build 6c1d796.
- App pages `/`, `/login`, `/signup`, `/verify`, `/forgot-password`,
  `/reset-password`, `/keys`, `/billing`, `/usage`: 200, all with the enforced
  `default-src 'none'` CSP. Headless Chrome: `/signup`, `/login` and `/verify`
  made **no third-party requests** and logged no console errors.
- `/dashboard/v1/config`: 200 (`turnstile_site_key: null`, top-up USD 10–100).
- Sign-up to `success@simulator.amazonses.com`: 202 `verification_sent`. Again
  with the same address: 202, identical. SES `Send` on the `unify-compute`
  configuration set recorded 2 at 18:43Z. **No 6-digit code appears in the
  compute-api log.**
- A wrong `Origin` got 403 `csrf_failed`. `/dashboard/v1/me` on the API host
  got 404. `POST /dashboard/v1/topups` without a session got 401
  `session_required`.
- A made-up `uck_` key got 401 `invalid_key`. An `sk-` key got 401
  `missing_key` (expected: Unify API keys no longer work on compute
  production).
- On the host: `admin audit` "balance invariant holds"; `check-imds.sh`
  `CHECK_IMDS_OK`.

## Rollback

docs/staging.md, "Production on Compute's own accounts": restore
`env.before` from the C1 backup and recreate compute-api under the lock
(one step), then unset the GitHub `APP_ORIGIN`. Accounts and balances stay
in the database.

## Not verified (needs the operator)

- A real sign-up with a code read from an inbox, then login, reauth, key,
  and `403 compute_not_enabled` on submit.
- The first real top-up (live card), and its credit in the balance.
- `admin account enable-compute` and `admin balance credit` on a real
  account. A submit would then reach `503 upstream_unavailable`, because
  production has no SkyPilot.

## Known gap

User-data renders none of the accounts or Stripe settings. A rebuild of the
host comes back on console accounts with no Stripe. See docs/staging.md.
