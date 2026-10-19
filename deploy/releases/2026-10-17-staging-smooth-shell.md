---
environment: staging
action: release
outcome: success
run: 39032471498
commit: 5838ce5dcfd0877da5677463fb80da335d8c5e92
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fd586e94c7fe65a844ccae5f8ee4f44f39ff15591bd5f5cdfa37df4c67e77ae4
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:bd9edfdbd0a82676db29adb0216a11796d758f4c8ae404bab92b30bb4bc74f12
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fd586e94c7fe65a844ccae5f8ee4f44f39ff15591bd5f5cdfa37df4c67e77ae4
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:c2899e5f4815bf0dd4574ac0cdc3e7d59541575c1c347c87b73080f8ba7a15e2
hotfix: false
---

# 2026-10-17 — smooth-shell on staging

The operator asked for a release ("发布") of what was merged and unshipped on
main (`5838ce5`). Measured against the job-log-archive records (both hosts),
that is #131 alone.

## What shipped

Site `c2899e5f…` (`0e2b694`) → `bd9edfdb…` (`5838ce5`). It was published
by publish-image run 39032314698 (tag `smooth-shell-2026-10-17`,
`sha-5838ce5…`, amd64 + arm64, arm64 asserted on the published index).
`git log e7fe722..5838ce5` shows two commits:

- `2c6461a` #131: the app host (`dashboard/`) gets cross-document view
  transitions (`@view-transition`, header and sidebar named so they stay
  put, content cross-fades over 140 ms, off under prefers-reduced-motion).
  The entry script is now `blocking="render"`, re-added after Vite's
  rewrite, and the build fails if it is missing. Five above-the-fold fonts
  are preloaded. The header email is read from sessionStorage `uc_me`
  before `GET /me` returns. `send()` reuses the page's CSRF token and
  retries once on `403 csrf_failed`. Buttons get a 1 px press and a
  spinner while `aria-busy`. The sidebar gets icons and a Collapse/Expand
  button (`#nav-collapse`, localStorage `uc_nav`, applied before first
  paint); phones keep the menu panel.
- `decb81c`: `deploy/site/smoke.sh` and `browser-check.mjs` follow it.

compute-api is unchanged (`fd586e94…`, `e7fe722`): nothing under
`compute-api/` changed. No price, migration, env var or Terraform change.
The CSRF change is client-side only. The server still requires the token,
and every page still fetches `/me` (and its token) once at load.

## Before / after

- Before (status run 39032317977): `fd586e94…` / `c2899e5f…`, PREVIOUS
  `05ba021c…` / `c2899e5f…`, IMDS lockdown present. This matches the
  job-log-archive record and production (status run 39033744713), so
  staging was at production's version.
- Run 12:25:36–12:27:37Z. Backup `/data/backups/20261017T122621Z-39032471498`,
  copied to
  `s3://unify-compute-staging-backups-871376691052/staging/releases/20261017T122621Z-39032471498/`
  (compute 29 tables, console 59).
- Only `site` was recreated. After 30 s all six containers had 0 restarts.
  Smoke passed, and `/healthz` reports build `e7fe722`.
- PREVIOUS_RELEASE is now `fd586e94…` / `c2899e5f…`, so `mode=rollback`
  is a site-only step back.

## What was exercised

This was headless Chrome 155 driven over CDP from the dev Mac, against
`app.staging.compute.unifyai.us`. A new throwaway, account 26
(`success+cicd-shell-1017@simulator.amazonses.com`), signed up through
`/signup`. Its verify code was recovered offline from its `email_codes`
hash (a read-only query over SSM), then entered on `/verify`.

- **Served HTML**: all 16 app pages carry exactly one entry script, with
  `blocking="render"`; the 404 has none. All 17 (404 included) have 5 font
  preload links. Each of the 5 fonts answers 200 `font/woff2`.
- **Sidebar, 1280 px**: it starts at 216 px with `aria-pressed="false"`.
  After Collapse it is 64 px, labels are hidden, `aria-pressed="true"`,
  the button label reads "Expand sidebar" and `uc_nav` is `collapsed`.
  Links keep `title` tooltips; the native tooltip was not hovered. Going to
  /jobs by its link, the first animation frame of the new page already
  measured 64 px. Expand gives 216 px and `uc_nav=open`.
- **375 px** on /, /jobs, /usage, /billing and /keys: the menu button is
  44×44, the collapse button is not shown, and `scrollWidth` is 375 (no
  horizontal scroll). The opened menu is a full-width panel with no
  collapse button.
- **Language**: after choosing 简体中文, the first frame of /usage (an rAF
  registered at document start, which cannot run before the render-blocking
  entry finishes) had title `用量 — Unify Compute`, `lang=zh-Hans` and h1
  用量. The same held on /jobs (任务). /login after logout showed
  `登录 — Unify Compute`.
- **Identity before /me**: on every page reached by a link, the header
  email was set in the first frame while `GET /me` had not yet resolved.
- **Create a key**: right after the click, the submit button was
  `aria-busy="true"` and disabled, and its `::after` ran `uc-spin`
  (screenshot taken with 1.2 s emulated latency). The network showed
  `POST /keys` and then the list's `GET /keys`; there was **no `GET /me`
  before the POST**. On a second run without throttling: POST 201, then
  GET 200.
- **Revoke**: `DELETE /keys/6` returned 204 at 0.64 s, the list refreshed,
  the status line confirmed it, and the row read 已吊销. (In the first run,
  key 5's row was scraped before its refresh, just after the latency
  emulation was removed, so it still said Active. The API confirms key 5
  was revoked at 12:37:20Z.)
- **Log out from the header**: one `POST /logout`, landing on /login.
  `uc_me` was cleared, and `GET /me` then gave 401.
- **Transitions**: a cross-document view transition ran on every link
  navigation between Overview, Jobs, Usage, Billing and API keys
  (`pagereveal` had a `viewTransition`). A screenshot burst during
  Billing → Usage caught a frame mid-cross-fade: the two pages' content
  was superimposed while the header and sidebar were crisp and not
  doubled. This is a single frame, not a full recording.
- **Marketing site** (`site.staging…`): it carries the new image's
  bundle. Its visible text on / matches production's pre-release text
  (hash `60f9cab0…`). The JS differs only in a per-build UUID and the
  chunk hashes that follow from it. The CSS gained one rule,
  `.collapse{visibility:collapse}`, because Tailwind's source scan saw the
  dashboard's new "collapse". No marketing markup uses the class.

## What this does not prove

- The 140 ms cross-fade and "no flash" were judged from one mid-transition
  frame plus `pagereveal`, not a video. Back/forward (traverse)
  transitions and prefers-reduced-motion were not exercised.
- The `403 csrf_failed` retry path was not triggered (that needs a log-in
  in a second tab between page load and action).
- Only Chrome. Safari and Firefox were not checked; Firefox does not run
  cross-document view transitions, which degrades to a plain navigation.
- Account 26 is left on staging: active, compute off, balance 0, keys 5
  and 6 revoked, no session. Its password, code and browser profile were
  deleted from scratch.
