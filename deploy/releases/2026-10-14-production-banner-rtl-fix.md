---
environment: production
action: release
outcome: success
run: 38628621483
commit: db3c771b72c8bb2bd6a39138371401136594d9ba
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:05ba021ce498d3b55597392f135f6cb70373722984ab8e81d9e466bfa64153ce
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:b3871cfcc7907a453d2051af8e5f44e39137ec12a876e592d5a6961cf50d83bd
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:de7ce94deae0aaa3b38bc46b1a855f1665d83e71a63bbb09b4c69e4fa056e057
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:b9da4d906abd0fe20277e2645449dc9cae7761b89818ecdf48d5186903347a12
hotfix: false
---

# 2026-10-14 — dashboard W1–W9 and the banner/RTL fix on production

The digests of
[`2026-10-14-staging-banner-rtl-fix.md`](2026-10-14-staging-banner-rtl-fix.md),
released after staging's run 38627456906 and its checks passed. The gate
found the staging manifest (`release-manifest-staging-05ba021ce498-b3871cfcc790`).

## What shipped

Production came from marketplace-m3 (`de7ce94d…` / `b9da4d90…`, both
`d066488`), so this one step carries:

- compute-api `05ba021c…` (`494f43d`): everything listed in
  [`2026-10-14-staging-dashboard-w8-w9.md`](2026-10-14-staging-dashboard-w8-w9.md),
  #100–#111. That includes migrations 0011–0014. **Money:** 0012 drops
  0005's `CHECK (available >= 0 OR status = 'suspended')`, so a refund
  larger than `available` leaves a negative balance without suspending.
- site `b3871cfc…` (`db3c771`): the same, plus #113 (the two-decimal banner
  and the RTL-safe honeypot).

No `skus.json` or `platform.json` change, no Terraform.

## Before / after

- Before (status run 38628456983 and a read-only query): m3, PREVIOUS
  `d70b1423…` / `186c4d44…`. Schema `0010_listings`. 1 account (3, active,
  compute off, available 0, held 0), 1 user, 2 sessions, 1 key, 0 jobs,
  0 balance entries, 0 top-ups, 17 audit rows, 0 listings.
- Backup: `/data/backups/20261014T200253Z-38628621483`, copied to
  `s3://unify-compute-production-backups-871376691052/production/releases/20261014T200253Z-38628621483/`.
- Cutover: compute-api and site recreated, postgres untouched, 0 restarts
  after 30 s. Smoke passed, `/healthz` build `494f43d`.
  `compute.unifyai.us` resolves to the ALB and serves the site container.
- After: `schema_migrations` 0001–0014. Every row count above is
  unchanged. `notifications`, `topup_disputes`, `payment_emails` and
  `invitations` exist and are empty. Account 3 is named from its owner's
  email, with `member_limit` 20, threshold 2500000 (USD 5), not armed.
  The user's role is `owner`. `admin audit`: balance invariant holds.
  `stripe top-ups enabled livemode=true`. 0 ERROR lines in compute-api's log.

## What was exercised

Headless Chrome from outside against `app.compute.unifyai.us`:

- Arabic on `/login`, `/signup`, `/verify` and `/forgot-password` at 1280
  and 375: `dir=rtl`, `scrollWidth` equals the viewport, `scrollX` stays 0
  both ways. The honeypot is `aria-hidden`, tabindex -1, and never focused
  by Tab. Back to English gives `lang=en dir=ltr`. The English pages have no
  horizontal scroll at either width.
- `/login`: 64px top bar, brand, picker, Sign up link, 400px card, one
  primary button. 简体中文 reloads as "登录 — Unify Compute".
- **The app host is byte-identical to staging's.** All 16 pages
  (`/`, `/login` … `/accept-invitation`) and both bundles they reference
  match `app.staging.compute.unifyai.us`, where the signed-in checks ran.
  The bundle prints `USD ${usd(available_usd)}` and `USD ${usd(low_balance_usd)}`,
  and the CSS has `.hp{inset-inline-start:-10000px…}`.
- Marketing site `compute.unifyai.us`: the title is unchanged and the
  visible text is identical before and after. The HTML equals staging's
  apart from build ids. The CSS gained only `.visible` and `.rounded`,
  which the dashboard-w8-w9 record predicted and no marketing element uses.
  `/`, `/privacy`, `/start`, `/marketplace` and `/icon-v4.svg` give 200.
  `/terms` is 404, as on staging; nothing links to it.

## What this does not prove

- **No signed-in session on production.** The one account is the
  operator's, and no throwaway account was created in production's
  customer data. The banner reading "USD 0.00 … USD 5.00", and the
  signed-in pages, were seen on staging only. Production is covered by the
  identical bytes above and by the same banner inputs: account 3 has
  available 0 and threshold 2500000, as staging's account 7 does, and the
  same compute-api digest serves them. The operator will see the banner
  on every signed-in page until the balance reaches USD 5 or the alert is
  turned off.
- The migration ran here against an account with no balance entries, top-ups
  or jobs. #102's refund/dispute path, #104/#108's emails, #107's
  invitations and the by-day bars with charges have not run on any host.
  No GPU job was submitted (production has no SkyPilot).
- Rollback: `mode=rollback` goes to m3 (`de7ce94d…` / `b9da4d90…`), but m3
  never read schema 0011–0014. Restore the dump in this run's backup first
  (docs/staging.md, "Rolling back").
