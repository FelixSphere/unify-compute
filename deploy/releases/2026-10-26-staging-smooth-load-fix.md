---
environment: staging
action: release
outcome: success
run: 40267687668
commit: 2c7669f647307ad36b011744802683141eb27cf9
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fd586e94c7fe65a844ccae5f8ee4f44f39ff15591bd5f5cdfa37df4c67e77ae4
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:26d2e66c320a6ca0b60373ea61dfd0e4fc18f30f48f6bd5ccc744e40dd18268f
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fd586e94c7fe65a844ccae5f8ee4f44f39ff15591bd5f5cdfa37df4c67e77ae4
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:bd9edfdbd0a82676db29adb0216a11796d758f4c8ae404bab92b30bb4bc74f12
hotfix: false
---

# 2026-10-26 — smooth-load, with the skeleton fix, on staging

The second attempt at the operator's smoother page loads. The first
(`1d5c16e`, site `c4d87233…`) was held because filled loading lines kept
their skeleton, and staging was rolled back
([`2026-10-26-staging-smooth-load.md`](2026-10-26-staging-smooth-load.md),
[`2026-10-26-staging-rollback-smooth-load.md`](2026-10-26-staging-rollback-smooth-load.md)).
This release takes staging from production's pair to #136 + #138, the same
step production will take.

## What shipped

Site `bd9edfdb…` (`5838ce5`) → `26d2e66c…` (`2c7669f`), published by
publish-image run 40267340817 (tag `smooth-load-2026-10-26`,
`sha-2c7669f…`, amd64 + arm64). `git log --first-parent 5838ce5..2c7669f`:

- `07ef4d4` #132, `1fdfd1f` #137: release records and cicd.md notes. Docs only.
- `1d5c16e` #136: early reads beside `/me` (per-tab `uc_early:<path>`,
  paths only, 5 s window, one-object paths never remembered), shimmering
  skeletons for stat values and `common.loading*` placeholders, a 220 ms
  ease-in, and `<link rel="prefetch">` on sidebar hover/focus. Reduced
  motion turns both animations off. Described in full in the held record.
- `2c7669f` #138: every `[data-i18n^='common.loading']` rule in
  `dashboard/src/app.css` now requires `:not([data-loaded])`, the
  reduced-motion block included. `browser-check.mjs` asserts that nothing
  under `main` runs `uc-shimmer` once Overview's data is in (CI run
  40267074984 printed `ok overview: no skeleton left once loaded`).

compute-api is unchanged (`fd586e94…`, `e7fe722`). Nothing under
`compute-api/` changed. No price, migration, env var or Terraform change.

## Before / after

- CI on `2c7669f` passed (run 40267074984).
- Before (status run 40267515771): `fd586e94…` / `bd9edfdb…`, PREVIOUS
  `fd586e94…` / `c4d87233…` (the held build), IMDS lockdown present.
  Production (status run 40267593940): the same pair, PREVIOUS
  `fd586e94…` / `c2899e5f…`, IMDS present. So staging was at production's
  version.
- Run 04:10:28–04:12:31Z. Provenance: both digests carry linux/arm64 and
  were built from commits on main with CI green. Backup
  `/data/backups/20261026T041116Z-40267687668`, copied to
  `s3://unify-compute-staging-backups-871376691052/staging/releases/20261026T041116Z-40267687668/`
  (compute 29 tables, console 59). Routine prune removed
  `/data/backups/20261014T132712Z-38571762314`.
- Only `site` was recreated. All six containers had 0 restarts before,
  after `up -d` and after 30 s. Smoke passed, and `/healthz` reports build
  `e7fe722`.
- After: `fd586e94…` / `26d2e66c…`. PREVIOUS is now `fd586e94…` /
  `bd9edfdb…`, so `mode=rollback` on staging is safe again (it no longer
  points at the broken `c4d87233…`).
- The served stylesheet `app-p-RzbA1F.css` has `:not([data-loaded])` on all
  four `common.loading` rules.

## What was exercised

Headless Chrome over CDP from the dev Mac, against
`app.staging.compute.unifyai.us`. A new throwaway, account 28
(`success+cicd-load-1026@simulator.amazonses.com`), signed up through
`/signup` (202). Its verify code was recovered offline from its
`email_codes` hash (a read-only query over SSM), then entered on `/verify`.

- **Nothing left shimmering (the check that failed; pass, with one hidden
  element explained).** Overview, Usage, Billing and Jobs, in English and
  简体中文, at 1280 and 375 px: 16 loads. Each was measured after the network
  had been quiet for 1.2 s, plus 600 ms.
  - Overview, Billing, Jobs: `[...document.querySelectorAll('main *')]
    .filter(e => getComputedStyle(e).animationName === 'uc-shimmer')` is
    empty in all 12.
  - Usage: that filter returns one element in all 4: the
    "Loading usage…" `<p>` inside `#usage-breakdown`. For a period with no
    charges, `usage.ts` hides `#usage-detail` and returns before filling
    the breakdown, so the placeholder stays inside a `hidden`
    (`display: none`) subtree. `getComputedStyle` still reports the
    animation name there, but the element is not visible
    (`checkVisibility()` false, height 0), and
    `document.getAnimations()` holds no running `uc-shimmer` on Usage, or
    on any of the four pages. That measure does see skeletons: with 1.5 s
    emulated latency, 400 ms after DOMContentLoaded, 6 were running on
    Overview, 4 on Usage, 3 on Billing. After load, 0 on each.
  - The previously broken lines now read normally:
    `#balance-sub` "Nothing is held for running jobs.",
    `#spend-sub` "Nothing charged in October 2026 yet. Usage",
    `#usage-sub` "2026-10-01 to 2026-10-31, UTC.", Billing `#balance-sub`.
    Each computes `color: rgb(58, 58, 55)` and `animation-name: uc-reveal`.
    The same in 简体中文 ("没有为运行中的任务预扣的金额。",
    "2026年10月 尚未产生扣费。 用量", "2026-10-01 到 2026-10-31，UTC。").
    No visible leaf element under `main` has transparent text.
    Screenshots of the loaded Overview (en 1280, zh-Hans 375) show the
    balance, spend and running lines.
  - `scrollWidth` equals the viewport on all 16 loads.
- **Early reads (pass).** Overview loaded twice in one tab (ms from
  navigation start). Load 1: `/me` 467→696, then `/balance`, `/usage`,
  both `/jobs?…`, `/keys` from 899. Load 2: those five at 279–282, `/me`
  at 305. Each path once per load. `uc_early:/` held the five paths.
- **Key create, then revoke (pass).** The first `POST /keys` was
  `401 reauth_required`, because the session's password entry was over
  10 minutes old (`ReauthWindow`), which is by design. My script does not
  answer the re-auth dialog. After signing in again: `POST /keys` 201 then
  `GET /keys`, with the row `cicd-load-1026` Active and the `uck_` secret
  shown. `DELETE /keys/8` 204 then `GET /keys`, and the row read Revoked.
  Revisiting /keys started `/balance` and `/keys` (320 ms) before `/me`
  (384 ms), and the row still read Revoked.
- **Billing cancelled return (pass).**
  `/billing?topup=tu_01M5ZZZZZZZZZZZZZZZZZZZZZZ&checkout=cancelled` made
  `/me`, `/balance`, `/topups` and `/transactions?limit=25`, and no
  `/topups/tu_…`. "Payment cancelled. Nothing was charged." was shown and
  the URL became `/billing`.
- **Hover prefetch (pass).** Real pointer moves: onto /jobs added
  `link[rel=prefetch][href="/jobs"]` and Chrome fetched `/jobs` (200). The
  same for /usage. Hovering Overview's own link added nothing.
- **Marketing site (pass).** `site.staging…` `/` and `/privacy` have the
  same length as before the release (41917, 71717 bytes) and the same
  visible-text hash. They are byte-identical once per-build JS chunk hashes
  and the build UUID are normalised. `index.DGyjs4uY.css` is unchanged and
  identical to production's.

## What this does not prove

- Usage was only seen for an account with no charges, where the breakdown
  is hidden. With charges, `fill('usage-breakdown', …)` replaces the
  placeholder. That was read in the code, not run.
- Only Chrome, from Nanjing, one throwaway account with no jobs.

## Next

Production, with the same digests. Account 28 remains on staging: compute
off, balance 0, key 8 revoked. Follow-up, not blocking: the hidden Usage
placeholder makes the brief's computed-style selector a false positive on
an empty period. Either the check should use `document.getAnimations()`
(or skip hidden elements), or `usage.ts` should clear the placeholder when
it hides the detail. Otherwise a browser-check that adds Usage will fail on
an empty account.
