---
environment: staging
action: release
outcome: success
run: 38622405389
commit: 494f43dbb1dd4d2de3418f9e64419a5cdb71ccf7
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:05ba021ce498d3b55597392f135f6cb70373722984ab8e81d9e466bfa64153ce
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:8879ce5a17e3ff16929843d421df42bede113c890ce1de4b2dfd4a14fcbecf2d
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:de7ce94deae0aaa3b38bc46b1a855f1665d83e71a63bbb09b4c69e4fa056e057
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:b9da4d906abd0fe20277e2645449dc9cae7761b89818ecdf48d5186903347a12
hotfix: false
---

# 2026-10-14 — dashboard-w8-w9 on staging; production held

Operator: "PR 111, PR109 合并，发布". The run succeeded and the host is
healthy, but two of the acceptance checks failed on staging (below), so
**production was not released** and still runs marketplace-m3
(`de7ce94d…` / `b9da4d90…`, schema `0010_listings`).

## What shipped

compute-api `05ba021c…` and site `8879ce5a…`, both from main `494f43d`
(publish runs 38620748362 and 38620754943). From the previous builds
(both `d066488`, marketplace-m3), every merge to main since:

- #100 dashboard.ts split into one module per page (no behaviour change).
- #101 AGENTS.md brought in line with what runs (docs only).
- #102 W3/B2 Stripe refunds and disputes applied from the webhook, and
  `admin topup refund`. **Money:** migration 0012 drops 0005's
  `CHECK (available >= 0 OR status = 'suspended')`, because a refund larger
  than `available` now leaves a negative balance without suspending.
- #103 W1 getting-started checklist and spend cards on the Overview.
- #104 W2 job-outcome and top-up-received emails, sent once (migration
  0011 `notifications`).
- #106 W7 key expiry, log out other sessions, Help menu, run-form rate and
  hold.
- #107 W4 members, roles and invitations (migration 0013: `accounts.name`
  backfilled from the owner's email, `member_limit`, `users_role_check`
  now owner/admin/member, `invitations`).
- #108 W6 low-balance threshold, banner and one email per crossing
  (migration 0014, default USD 5).
- #109 W8 signed-in pages composed like Unify Data's (title row, stat
  cards, two-decimal amounts, empty-state blocks, Account rows, seven-column
  keys table, Usage toolbar, by-day chart fix).
- #111 W9 ten languages, language picker, header identity and Log out,
  64px top bar on the signed-out pages.

No `skus.json` or `platform.json` change, no Terraform. PR #110 (site i18n)
is open and not included.

## Before / after

- Before (status run 38620669680): the marketplace-m3 digests above, the
  same as production. Schema `0010_listings`; 1 account (id 3), 1 user,
  0 sessions, 0 keys, 2 jobs, 0 balance entries, 0 top-ups, 6 audit rows.
- Backup: `/data/backups/20261014T191823Z-38622405389`, copied to
  `s3://unify-compute-staging-backups-871376691052/staging/releases/20261014T191823Z-38622405389/`.
- Cutover: compute-api and site replaced, 0 restarts after 30 s, other
  containers untouched. Smoke 21 ok, `/healthz` build `494f43d`.
- After: `schema_migrations` 0001–0014, the row counts above unchanged,
  `notifications`, `topup_disputes`, `payment_emails`, `invitations` exist
  and are empty; account 3 named from its owner's email, `member_limit`
  20, threshold 2500000 (USD 5), not armed (available 0). `admin audit`:
  balance invariant holds. 0 ERROR lines in compute-api's log.

## What was exercised

Headless Chrome from outside against `app.staging.compute.unifyai.us`.
For a signed-in session, a throwaway account
`success+cicd-w8w9-1014@simulator.amazonses.com` (account 6, compute off,
balance 0) was signed up through `/signup` and verified through `/verify`.
The 6-digit code was recovered offline from the `email_codes` row (salted
SHA-256, read-only), so the verify path itself ran as a user would.

Passed:

- `/login`, `/signup` at 1280: 64px top bar with the brand, the picker and
  the other auth page as a text link; a 400px card under it; one primary
  button. `/verify`, `/forgot-password`, `/reset-password` carry the bar
  and picker.
- 简体中文 from the picker on `/login`: reload, title `登录 — Unify Compute`,
  `lang=zh-Hans`, `uc_locale=zh-Hans`; `/signup` stays Chinese
  (`注册 — Unify Compute`), and so does a new tab. العربية: `<html dir="rtl"
  lang="ar">`, header and form mirrored. Back to English: `lang=en`,
  `dir=ltr`.
- Signed in, on Overview, Jobs, API keys, Usage, Billing, Account (and
  Members, Marketplace, My listings): 64px header with the picker, `SC` +
  the email linking to `/account`, and Log out. Each page opens with its
  H1 in `.page-head`; the primary action is on the title row at the right
  (Create key, Contact us, New listing). Stat values are 40px/32px, and no
  text is 48px or larger. Empty lists are titled blocks; selects are
  styled (`appearance: none`).
- `/usage` for October 2026, which has no charges: one block, "Nothing
  charged in October 2026". Period controls in a toolbar.
- `/account`: profile head, rows Email / Password / Other sessions;
  Change password opens the form under its row; a wrong current password
  gives "Your current password is incorrect." inline.
- `/keys`: seven columns (Key, Access, This month / cap (USD), Expires,
  Last used, Status, actions). Create key with an expiry 3 days out
  revealed a `uck_` key once, and the row shows `Oct 17, 2026, 11:59 PM`.
- #106: with a second browser context logged in, "Log out other sessions"
  ends the others (`sessions_ended {"ended": 3}`), the other context lands
  on `/login`, and this one stays. Help menu: Quick start, API reference,
  Contact us.
- Log out from the Overview and from `/account` both land on `/login`, and
  `/` then redirects to `/login`.
- #103: a fresh account shows "Get started, 1 of 5 done" and no
  Compute access card; after the key, step 4 ticks (2 of 5).
- #108: Billing shows the alert at 5.00; `1.234` is refused inline; 0
  turns the banner off on every page; 7.5 saves as 7.50 and brings it
  back; then 5.00 again. Three `low_balance_changed` audit rows.
- 375px: `/login` has no horizontal scroll and the picker is in view; the
  Overview has a 44x44 menu button that opens the nav, no horizontal
  scroll, the picker and Log out in the header, and the email hidden.
- Signed in, Arabic on the Overview: sidebar on the right, cards mirrored,
  no horizontal scroll.
- Marketing site (`site.staging.compute.unifyai.us`): English, the same
  title. Markup and JS are identical to before apart from content-hashed
  file names. The CSS gained two Tailwind utilities (`.rounded`,
  `.visible`), picked up from the dashboard's sources, which no marketing
  element uses.

Failed. **These are why production was held:**

1. **The low-balance banner prints four decimals.** On every signed-in
   page: "Your available balance is USD 0.0000, below your low-balance
   alert of USD 5.00." `lowBalanceBanner` in `dashboard/src/shell.ts`
   prints `available_usd` as the API returns it (four places). W8's
   two-decimal rule missed it. It shows for any account below its
   threshold. Production's one account has 0 available, so it would see
   it on every page.
2. **Arabic auth pages scroll about 10,000px sideways.** The honeypot
   `.hp` (`dashboard/src/app.css`) is `position: absolute; left: -10000px`.
   Under `dir="rtl"` overflow to the left is scrollable, so `/login`,
   `/signup`, `/verify` and `/forgot-password` have `scrollWidth` 11280 at
   1280 and 10375 at 375. The 375 checks pass in English only. Since
   `pick()` follows `navigator.languages`, a browser set to Arabic gets
   this without choosing it. An off-screen rule that holds in both
   directions (`inset-inline-start`, or clip-based hiding) would fix it.

Also seen, not blocking:

- The 404 page has the top bar but no picker and is never translated: it
  ships no script, so `#locale` stays empty.
- `/members` uses the account name as its H1 under a "MEMBERS" eyebrow,
  and `/listings`' head wraps to two lines. Neither page was in W8's list.
- The account's `login_failed` audit row came from the wrong current
  password on `/account`.

## What this does not prove

- **The by-day usage bars for a month with charges:** no account on staging
  has a charge, and making one means running a GPU job. The fix is held by
  W8's tests and its browser check against the mock, not by a host.
- **No job was submitted.** The account's G/VT quota is now **4 vCPU**
  (`L-DB2E81BA`), no longer 0, so a staging job could launch a real g5.
  Under "Start small" that was not done for a UI release. So #104's job
  emails and #108's crossing email did not run on a host.
- #102 refunds and disputes: staging has no Stripe keys (webhook `503`).
  Not exercised anywhere against Stripe.
- #107 invitations: the accept link travels only in the email, so the
  invite → accept → member path was not run. `/members` shows the owner
  and Rename and Invite someone.
- Throwaway account 6 is left on staging with compute off, balance 0 and
  one unused key. Its password is not stored anywhere.

## Next

A fix for 1 and 2 goes through review, then a release of the new digests
to staging and production. Staging already runs 0011–0014. A fix release
from here tests the delta from this build, while production would take
m3 → fix in one step. This run proved the migration part from m3 parity,
and the fix delta is UI only. If the fix touches anything else, restore
staging to this run's dump first.
