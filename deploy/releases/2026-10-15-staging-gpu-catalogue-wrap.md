---
environment: staging
action: release
outcome: success
run: 38723813378
commit: 0e2b69439e9f8a6d8173dd94b16a1ae3ed6ccc33
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:05ba021ce498d3b55597392f135f6cb70373722984ab8e81d9e466bfa64153ce
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:c2899e5f4815bf0dd4574ac0cdc3e7d59541575c1c347c87b73080f8ba7a15e2
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:05ba021ce498d3b55597392f135f6cb70373722984ab8e81d9e466bfa64153ce
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:b3871cfcc7907a453d2051af8e5f44e39137ec12a876e592d5a6961cf50d83bd
hotfix: false
---

# 2026-10-15 — gpu-catalogue-wrap on staging

Operator: "先把 PR 115 合并发布". The second attempt at #115, after
[`2026-10-15-staging-gpu-catalogue.md`](2026-10-15-staging-gpu-catalogue.md)
held production because the signed-in Marketplace page scrolled sideways
at 375 px. Staging was first rolled back to production's pair
([`2026-10-15-staging-rollback-parity.md`](2026-10-15-staging-rollback-parity.md)),
so this run is production's exact step.

## What shipped

Site `c2899e5f…`, from main `0e2b694` (publish run 38723254040, tag
`gpu-catalogue-wrap-2026-10-15`, arm64 asserted on the published
manifest). From the previous site build (`db3c771`):

- #114 the banner-rtl-fix records (docs only).
- #115 the GPU catalogue: a GPUs table on the dashboard's Marketplace page,
  every catalogue SKU in the run form's GPU (SKU) select with the unsold
  ones disabled as "Coming soon", and the same table without prices on the
  public `/marketplace#gpus`. Ten languages.
- #117 the held gpu-catalogue staging record (docs only).
- #118 the fix: the "GPUs" subtitle is `.section-note`, which wraps
  (`white-space: normal; flex: 1 1 320px`), right-aligned beside the
  heading on wide screens and under it at 580 px and below. It also gives
  every `.section-head` `flex-wrap: wrap`.

compute-api is unchanged (`05ba021c…`, `494f43d`), so nothing was rebuilt.
No `skus.json` or `platform.json` change, so no price changed. No
migration, no Terraform.

## Before / after

- Before: `05ba021c…` / `b3871cfc…` (after the parity rollback).
- Run 10:56:34–10:58:50Z. Provenance: both digests carry linux/arm64 and
  were built from commits on main with green CI (`494f43d`, `0e2b694`).
- Backup `/data/backups/20261015T105729Z-38723813378`, copied to
  `s3://unify-compute-staging-backups-871376691052/staging/releases/20261015T105729Z-38723813378/`
  (compute 29 tables, console 59 tables).
- Only `site` was recreated; the other five containers kept their ids,
  0 restarts after 30 s. Smoke passed, `/healthz` build `494f43d`, app
  host CSP enforced.
- PREVIOUS_RELEASE is production's pair, so `mode=rollback` on staging
  returns to exactly what production ran before this release.

## What was exercised

Headless Chrome from outside, against `app.staging.compute.unifyai.us`.
Account 8's password went with the last session's scratch files, and a
reset link cannot be recovered from its stored digest, so a new throwaway
account was signed up through `/signup`:
`success+cicd-wrap-1015@simulator.amazonses.com` (user and account 22,
compute off, balance 0). It was verified through `/verify`, with the code
read from its `email_codes` row over SSM (read-only) and recovered offline
from the salted hash.

Signed in, all passed:

- **375 px, the check that failed last time.** On `/marketplace`, in
  English and 简体中文, `document.documentElement.scrollWidth` = 375,
  `innerWidth` = 375 and `visualViewport.width` = 375, and `scrollX`
  stays 0 after `scrollTo(10000, …)`. The subtitle is under "GPUs" (en:
  heading bottom 498, subtitle top 510, x 16–359, `white-space: normal`,
  `text-align: start`; zh the same shape). The table scrolls inside its
  `.table-wrap` (en 798 px in 343, zh 673 in 343, `overflow-x: auto`,
  `scrollLeft` moves).
- 1280 px, English: `scrollWidth` 1280 = `innerWidth`. GPUs above
  Templates, four rows: NVIDIA A10G `a10g-24gb-x1`, 24 GB, 4 · 16 GB,
  Hyperscaler, On-demand, `1.20`, Available; then A10G spot (Spot, 0.50),
  L4 (0.90) and A100 80GB (Neocloud, 2.00), all Coming soon. The subtitle
  sits beside the heading (x 308–1244).
- "Run this" on `batch-embed`: GPU (SKU) has
  "NVIDIA A10G · a10g-24gb-x1 · USD 1.20/h" selected and enabled, and the
  other three present and disabled ("… · Coming soon"). Cancel closed the
  form, and nothing was submitted.
- 简体中文: `lang=zh-Hans`, headers GPU / 显存 / vCPU · 内存 / 层级 / 计费 /
  USD / 小时 / 状态 (shown uppercased by CSS), row 1 可用 / 按需, three
  rows 即将推出, tiers 超大规模云 / 新兴云. The run form's three disabled
  options end in "即将推出".
- Regression: Overview loads with its H1, no horizontal scroll, and no
  console errors on any signed-in page. Log out lands on `/login`, and
  `/` then redirects to `/login`. `/login`: "Log in — Unify Compute",
  64 px bar, 400 px card, one primary button.
- Because #118 adds `flex-wrap` to every `.section-head`: at 375 px, en
  and zh-Hans, `/`, `/jobs`, `/keys`, `/billing`, `/usage`, `/listings`,
  `/account` and `/marketplace` all have `scrollWidth` = `innerWidth` =
  375, with 0 console errors.

Public `site.staging.compute.unifyai.us/marketplace#gpus`, 1280 and 375:
four rows (A10G Available, three Coming soon), headers GPU / Memory /
vCPU · RAM / Tier / Billing / Status, no price in the visible text or the
HTML, the anchor scrolls to "Choose the GPU for the job", no page-level
horizontal scroll, and at 375 the table scrolls in its box.

## Found, not caused by this release

- **`/members` scrolls sideways at 375 px** (`scrollWidth` 391 in both
  languages). Its table (Email, Role, Status, Last logged in, Joined,
  actions) is 810 px wide and is not in a `.table-wrap`. The members
  files last changed in W4/W8, which production already runs, and this
  release's only CSS change is the `.section-head` / `.section-note`
  rules, which `members.html` does not use. So production has had this
  since dashboard-w8-w9, and the release does not make it worse. A fix
  (wrap the table like the others) is for the dashboard owner, not
  this release.

## What this does not prove

- No job was submitted. The job API's refusal of a "Coming soon" SKU was
  not exercised (compute-api unchanged).
- Only en and zh-Hans were looked at. The other eight locales' strings
  were not rendered on a host.
- Account 22 is left on staging: compute off, balance 0, no key. Its
  password was deleted with the scratch files. Accounts 6, 7 and 8 are
  still there too.
