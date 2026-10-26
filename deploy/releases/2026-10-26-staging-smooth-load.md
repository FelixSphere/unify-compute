---
environment: staging
action: release
outcome: success
run: 40265141412
commit: 1d5c16ecfbbeab45c81c307e8aef396659d6bea2
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fd586e94c7fe65a844ccae5f8ee4f44f39ff15591bd5f5cdfa37df4c67e77ae4
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:c4d87233b4adbda42b17d55214f4b2d3372caf6ca4f9f0512935af4223775eb4
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fd586e94c7fe65a844ccae5f8ee4f44f39ff15591bd5f5cdfa37df4c67e77ae4
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:bd9edfdbd0a82676db29adb0216a11796d758f4c8ae404bab92b30bb4bc74f12
hotfix: false
---

# 2026-10-26 — smooth-load on staging; failed the exercise, production held

The operator asked for smoother page loads ("现在load页面的速度还是不自然，应该柔顺一些，改善").
#136 delivers that, and the run succeeded and smoke passed. But on staging,
**three sub-lines stay invisible under a shimmering bar after their data
arrives** (below). So production was **not released**, and staging was
rolled back to production's pair
([`2026-10-26-staging-rollback-smooth-load.md`](2026-10-26-staging-rollback-smooth-load.md)).
Production stays on smooth-shell (`fd586e94…` / `bd9edfdb…`).

## What shipped

Site `bd9edfdb…` (`5838ce5`) → `c4d87233…` (`1d5c16e`). It was published
by publish-image run 40264888985 (tag `smooth-load-2026-10-25`,
`sha-1d5c16e…`, amd64 + arm64, arm64 asserted on the published index).
`git log 5838ce5..1d5c16e`:

- `44b9e76` / `07ef4d4` #132: the smooth-shell records and a cicd.md note.
  Docs only.
- `00ac349` #136: on the app host, each page remembers per tab
  (sessionStorage `uc_early:<path>`) the GETs it made in its first 5 s, and
  the next load starts them in `warm()` beside `GET /me`. `get()` takes
  the started request, `send()` drops unclaimed ones, and nothing older
  than 5 s is used. Only paths are stored, never responses. Stat values and
  `common.loading*` placeholders become shimmering bars. Content eases in
  once (`.uc-reveal`, 220 ms). Hovering or focusing a sidebar link appends
  `<link rel="prefetch">` for that page. Reduced motion turns both
  animations off.
- `514191b` #136: one-object reads (`/topups/tu_…`, `/jobs/job_…`) are
  never remembered.

compute-api is unchanged (`fd586e94…`, `e7fe722`). Nothing under
`compute-api/` changed since. No price, migration, env var or Terraform
change.

## Before / after

- Two status runs dispatched together (40264910296, 40264913417) refused
  each other at "No other release is in flight", before touching either
  host. That was operator error (mine), not a fault. They were re-run one
  at a time.
- Before (status run 40264974088): `fd586e94…` / `bd9edfdb…`, PREVIOUS
  `fd586e94…` / `c2899e5f…`, IMDS lockdown present. Production (status run
  40265053048) ran the same pair, so staging was at production's version.
- CI on `1d5c16e` passed.
- Run 03:39:24–03:41:24Z. Backup `/data/backups/20261026T034011Z-40265141412`,
  copied to
  `s3://unify-compute-staging-backups-871376691052/staging/releases/20261026T034011Z-40265141412/`
  (compute 29 tables, console 59). Routine prune removed
  `/data/backups/20261010T025132Z-38018448565`.
- Only `site` was recreated. After 30 s all six containers had 0 restarts.
  Smoke passed, and `/healthz` reports build `e7fe722`.

## What was exercised

Headless Chrome, driven over CDP from the dev Mac, against
`app.staging.compute.unifyai.us`. A new throwaway, account 27
(`success+cicd-load-1025@simulator.amazonses.com`), signed up through
`/signup`. Its verify code was recovered offline from its `email_codes`
hash (a read-only query over SSM), then entered on `/verify`.

- **Early reads (pass)**. Overview was loaded twice in one new tab. Times
  are ms from navigation start, request start → response end.
  - Load 1: `/me` 1907→2132. Then `/balance` 2676, `/usage` 2694, both
    `/jobs?…` 2703, `/keys` 2705, all ending by 2959. DCL 1910, load 2551.
    The first load includes Chrome's connection setup, so its absolute
    numbers are not comparable with load 2.
  - Load 2: `/balance` 314→537, `/usage` 315→547, `/jobs?limit=5`
    315→537, `/jobs?status=…&limit=10` 316→547, `/keys` 316→549, then
    `/me` 376→598. DCL 388, load 709. The reads started 60 ms **before**
    `/me`, not after it.
  - Each path was requested exactly once per load (CDP network log), and
    `uc_early:/` held those five paths.
- **Skeletons before data (pass)**. With 1.5 s emulated latency, 400 ms
  after DOMContentLoaded, Overview had 6 elements running `uc-shimmer` and
  Billing had 3. Screenshots were taken. Under
  `prefers-reduced-motion: reduce`, both the skeleton and `.uc-reveal`
  computed `animation-name: none`.
- **Nothing left shimmering (FAIL)**. The brief's selector
  (`.stat-value[id]:not([data-loaded]), .head-stat-value[id]:not([data-loaded])`)
  counted 0 after load on Overview, Usage, Billing and Jobs. But elements
  that carry `data-i18n="common.loading…"` themselves keep the skeleton
  after `setText` fills them:
  - Overview `#balance-sub` "Nothing is held for running jobs.";
  - Overview `#spend-sub` "Nothing charged in October 2026 yet. Usage";
  - Usage `#usage-sub` "2026-10-01 to 2026-10-31, UTC.";
  - Billing `#balance-sub`.

  Each had `data-loaded="1"` and its real text, but computed
  `color: rgba(0,0,0,0)` and `animation-name: uc-shimmer`. A user sees a
  permanent grey bar where the sentence was, with only the "Usage" link
  showing through (screenshot of the loaded Overview). The same happened
  in 简体中文 at 375 px. The cause is in `dashboard/src/app.css`: the
  `body.app [data-i18n^='common.loading']` rules have no
  `:not([data-loaded])`, unlike the two stat rules beside them. A
  placeholder `<p>` *inside* a container that `fill()` replaces is fine;
  one that *is* the target of `setText` is not.
- **Key create, then revoke (pass)**. On /keys: `POST /keys` then
  `GET /keys`, and the list showed `cicd-load-1025` Active. Revoke:
  `DELETE /keys/7` then `GET /keys`, and the row read Revoked. Revisiting
  /keys started `/balance` and `/keys` early (before `/me`), and the row
  still read Revoked.
- **Billing cancelled return (pass)**.
  `/billing?topup=tu_01M5ZZZZZZZZZZZZZZZZZZZZZZ&checkout=cancelled` made
  `/me`, `/balance`, `/topups` and `/transactions?limit=25`. There was no
  request to `/topups/tu_…`. It showed "Payment cancelled. Nothing was
  charged." and the URL became `/billing`. Also run: the same id with
  `checkout=success` polled `/topups/tu_…` once (404, stop). That path was
  then **not** in `uc_early:/billing`, and a reload did not replay it
  (514191b).
- **Hover prefetch (pass)**. A real pointer move onto the /jobs link added
  `link[rel=prefetch][href="/jobs"]`, and Chrome fetched `/jobs`. The same
  held for /usage. Hovering the current page's link added nothing.
- **375 px, 简体中文**: `scrollWidth` 375 on /, /jobs, /usage, /billing,
  /keys, /account, /marketplace and /listings. /members is 394, the known
  issue from 2026-10-15 (its table has no `.table-wrap`), not from this
  release.
- **Marketing site (pass)**. On `site.staging…`, `/` and `/privacy` have
  the same visible text as before the release (hashes `60f9cab0…`,
  `267f52b9…`; production has the same). The CSS file
  `index.DGyjs4uY.css` is identical. Only the per-build JS chunk names
  changed.

## What this does not prove

- Only Chrome. Timings are from one throwaway account with no jobs, from
  Nanjing, so a single measurement, not a distribution.
- The `403 csrf_failed` retry and any write racing an early read inside
  its 5 s window were not exercised beyond key create/revoke.

## Next

Fix the CSS so the loading rules stop once an element is filled (for
example `body.app [data-i18n^='common.loading']:not([data-loaded])` in all
four places, the reduced-motion block included). Then re-run this
exercise. A browser-check assertion would have caught it: after load, no
visible element computes `animation-name: uc-shimmer`. Account 27 remains
on staging: compute off, balance 0, key 7 revoked, signed out. Its
password, code and browser profile were deleted from scratch.
