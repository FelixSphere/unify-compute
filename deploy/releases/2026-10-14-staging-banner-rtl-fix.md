---
environment: staging
action: release
outcome: success
run: 38627456906
commit: db3c771b72c8bb2bd6a39138371401136594d9ba
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:05ba021ce498d3b55597392f135f6cb70373722984ab8e81d9e466bfa64153ce
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:b3871cfcc7907a453d2051af8e5f44e39137ec12a876e592d5a6961cf50d83bd
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:05ba021ce498d3b55597392f135f6cb70373722984ab8e81d9e466bfa64153ce
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:8879ce5a17e3ff16929843d421df42bede113c890ce1de4b2dfd4a14fcbecf2d
hotfix: false
---

# 2026-10-14 — banner-rtl-fix on staging (site only)

Operator: "PR 111, PR109 合并，发布". The first attempt
([`2026-10-14-staging-dashboard-w8-w9.md`](2026-10-14-staging-dashboard-w8-w9.md))
held production on two failed staging checks. #113 fixes both. This run
puts that fix on staging. Production followed with the same digests
([`2026-10-14-production-banner-rtl-fix.md`](2026-10-14-production-banner-rtl-fix.md)).

## What shipped

Site `b3871cfc…`, from main `db3c771` (publish run 38626463264, tag
`banner-rtl-fix-2026-10-14`). From the previous site build (`494f43d`):

- #112 the dashboard-w8-w9 staging record and the cicd.md note (docs only).
- #113 `lowBalanceBanner` prints both amounts through `usd()`, and the
  honeypot `.hp` is off the start edge (`inset-inline-start: -10000px`)
  instead of `left: -10000px`, so a right-to-left page cannot scroll to it.
  `be0af52` takes back the 404-page change, so the 404 page is still
  English with no picker (W9b).

compute-api is unchanged (`05ba021c…`, `494f43d`): no compute-api source
changed since that build, so nothing was rebuilt. It kept its container.
No `skus.json` or `platform.json` change, no migration, no Terraform.

**Parity.** Staging was one release ahead of production (dashboard-w8-w9),
not level with it. Production's step is m3 → this pair. Staging already
took the compute-api half of that step, the same digest from m3 parity,
in run 38622405389, with migrations 0011–0014. The site is stateless.
Staging was therefore not rolled back and restored to m3 first.

## Before / after

- Before (status run 38626469273): `05ba021c…` / `8879ce5a…`, PREVIOUS
  m3. Schema `0014_low_balance`.
- Backup: `/data/backups/20261014T195442Z-38627456906`, copied to
  `s3://unify-compute-staging-backups-871376691052/staging/releases/20261014T195442Z-38627456906/`.
- Cutover: only `site` was recreated (`3552ec68…` → `6eb09256…`).
  compute-api, postgres, redis, console and skypilot kept their containers.
  0 restarts after 30 s. Smoke passed, `/healthz` build `494f43d`.
- After: schema `0014_low_balance`. 3 accounts (3, 6, 7), all compute off,
  balance 0, threshold 2500000. No compute-api ERROR line since the cutover.

## What was exercised

Headless Chrome from outside against `app.staging.compute.unifyai.us`.

**Throwaway account 6 could not be used.** It had no live session, its
password was never stored, and a reset link exists only in the email.
Signing in would have needed a database write. A new throwaway account,
`success+cicd-w10-1014@simulator.amazonses.com` (account 7, compute off,
balance 0, default threshold), was signed up through `/signup` and verified
through `/verify`. The 6-digit code came from the `email_codes` row, read
offline (salted SHA-256, read-only).

The two fixes, each read **before** the cutover (the bug present) and after:

- Banner, account 7, before: "Your available balance is USD 0.0000, below
  your low-balance alert of USD 5.00." After, on Overview, Usage, Account,
  API keys and Billing: "Your available balance is USD 0.00, below your
  low-balance alert of USD 5.00." There is a Top up link everywhere except
  Billing, which says "Add credit to keep jobs running."
- Arabic, picked from the picker, on `/login`, `/signup`, `/verify` and
  `/forgot-password`. Before: `scrollWidth` 11280 at 1280 and 10375 at 375,
  `scrollX` reaching -10000 (8 of 8 failing). After: `dir=rtl lang=ar`,
  `scrollWidth` equals the viewport (1280, 375), and `scrollX` stays 0 in
  both directions. The honeypot box sits past the start edge (x=11279 at
  1280). It is `aria-hidden`, `#website` has tabindex -1, and 13–14 Tab
  presses on each page never focus it. Switching back to English gives
  `lang=en dir=ltr`. The same four pages in English at 1280 and 375 have no
  horizontal scroll (honeypot at x=-10000).

Regression:

- `/login` at 1280: 64px top bar with brand, picker and Sign up link; a 400px
  card; one primary button; title "Log in — Unify Compute".
- 简体中文 from the picker reloads `/login` as "登录 — Unify Compute",
  `lang=zh-Hans`.
- Signed in: Overview, Usage, Account, API keys and Billing open with the
  H1 title row. Stat cards: Overview `0.00 / 0.00 / 0`, Usage `0.00 / 0s`,
  Billing `0.00`. No horizontal scroll and no console errors.
- 375px Overview: menu button 44x44, no horizontal scroll.
- Log out from the header lands on `/login`, and `/` then redirects to
  `/login`.
- Marketing site `site.staging.compute.unifyai.us`: the visible text is
  identical to the dashboard-w8-w9 snapshot and the CSS is byte-identical.
  The HTML differs only in build ids and hashed asset names.

## What this does not prove

- The by-day usage bars with real charges, #104's job emails, #108's
  crossing email, #102 Stripe refunds and disputes (staging has no Stripe
  keys), and #107's invite-accept path. None was run, for the reasons in
  the dashboard-w8-w9 record. No GPU job was submitted.
- Account 7 is left on staging: compute off, balance 0, no key, one
  session from a check run left to expire. Its password was deleted with
  the session's scratch files. Account 6 is also still there.
- Staging's `PREVIOUS_RELEASE` is now dashboard-w8-w9 (`05ba021c…` /
  `8879ce5a…`), so a staging rollback goes back to the banner bug, not to m3.
