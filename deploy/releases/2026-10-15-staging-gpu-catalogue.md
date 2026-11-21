---
environment: staging
action: release
outcome: success
run: 38672690257
commit: 846c2e075ca0abbfd3cdb202d7d3ba9db129b813
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:05ba021ce498d3b55597392f135f6cb70373722984ab8e81d9e466bfa64153ce
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:b14bbf75adee10697de1b1962bae957a4c9ad80b9e87a44f6f07dc3a1b6030dc
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:05ba021ce498d3b55597392f135f6cb70373722984ab8e81d9e466bfa64153ce
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:b3871cfcc7907a453d2051af8e5f44e39137ec12a876e592d5a6961cf50d83bd
hotfix: false
---

# 2026-10-15 — gpu-catalogue on staging; production held

Operator: "先把 PR 115 合并发布". The run succeeded and smoke passed, but
one of the brief's checks failed on staging (375px, below), so **production
was not released**. Production stays on banner-rtl-fix
(`05ba021c…` / `b3871cfc…`).

## What shipped

Site `b14bbf75…`, from main `846c2e0` (publish run 38672373038, tag
`gpu-catalogue-2026-10-15`). From the previous site build (`db3c771`):

- #114 the banner-rtl-fix records and the cicd.md note (docs only).
- #115 the GPU catalogue: a GPUs table at the top of the dashboard's
  Marketplace page (name and id, memory, vCPU · RAM, tier, on-demand or
  spot, USD per hour, Available or Coming soon). The run form's GPU (SKU)
  select now lists every catalogue SKU, with the unsold ones disabled as
  "Coming soon". The public `/marketplace` gets the same table, without
  prices, at `#gpus`. Strings are in ten languages.

compute-api is unchanged (`05ba021c…`, `494f43d`). No compute-api source
changed since that build, so nothing was rebuilt; the dashboard reads the
existing `GET /dashboard/v1/skus`, which already returned all four SKUs
with every field the table uses (checked on both hosts before the cutover).
No `skus.json` or `platform.json` change, so no price changed. No migration,
no Terraform.

**Parity.** Staging was at production's pair before the run (status run
38672402473: `05ba021c…` / `b3871cfc…`, PREVIOUS dashboard-w8-w9).

## Before / after

- Backup: `/data/backups/20261015T024632Z-38672690257`, copied to
  `s3://unify-compute-staging-backups-871376691052/staging/releases/20261015T024632Z-38672690257/`
  (compute 29 tables, console 59 tables).
- Cutover: only `site` was recreated. compute-api, postgres, redis, console
  and skypilot kept their containers. 0 restarts after 30 s. Smoke passed,
  `/healthz` build `494f43d`, app host CSP enforced.
- PREVIOUS_RELEASE on staging is now banner-rtl-fix (`05ba021c…` /
  `b3871cfc…`), i.e. production's pair, so `mode=rollback` on staging goes
  back to exactly what production runs.

## What was exercised

Headless Chrome from outside. Account 7 had no stored session or password
(see the banner-rtl-fix record), so a new throwaway account,
`success+cicd-gpu-1015@simulator.amazonses.com` (account 8, compute off,
balance 0), was signed up through `/signup` and verified through `/verify`.
The code came from its `email_codes` row, read-only over SSM and recovered
offline from the salted hash.

Passed:

- Signed-in `/marketplace` at 1280, English: the GPUs section sits above
  Templates, and the table has four rows. Row 1 is NVIDIA A10G
  `a10g-24gb-x1`, 24 GB, 4 · 16 GB, Hyperscaler, On-demand, `1.20`,
  Available. The other three are Coming soon: A10G spot (Spot, 0.50),
  L4 (0.90) and A100 80GB (Neocloud, 2.00), sorted available first, then by
  price. No console errors.
- "Run this" on `batch-embed` opens the form. GPU (SKU) has
  "NVIDIA A10G · a10g-24gb-x1 · USD 1.20/h" selected and enabled. The other
  three are present and disabled ("… · Coming soon"). The estimate reads
  "a10g-24gb-x1 costs USD 1.20 an hour …". Cancel closed the form, and
  nothing was submitted.
- 简体中文 from the picker: `lang=zh-Hans`, headers GPU / 显存 /
  vCPU · 内存 / 层级 / 计费 / USD / 小时 / 状态. Three rows say 即将推出,
  row 1 says 可用 / 按需, and the tiers read 超大规模云 / 新兴云. In the run
  form the three disabled options end in "即将推出".
- At 375 the GPU table scrolls inside its `.table-wrap` (798 px of
  content in a 343 px box, `overflow-x: auto`) in both languages.
- Public `site.staging.compute.unifyai.us/marketplace#gpus` at 1280 and
  375: the anchor scrolls to "Choose the GPU for the job", four rows (A10G
  Available, three Coming soon), and no price column. No price anywhere:
  the visible text has no USD, `$`, or two-decimal figure, and the HTML
  has no `price_usd` or SKU rate. At 375 the table scrolls in its box and
  the page does not. No console errors.
- Regression: back to English, Overview loads with its H1 and no console
  errors. Log out from the header lands on `/login`, and `/` then
  redirects to `/login`. `/login` is "Log in — Unify Compute", 64 px bar,
  400 px card, one primary button.

**Failed. This is why production was held:**

- **375px, signed-in Marketplace: the page scrolls sideways.** The new
  subtitle under "GPUs" (`marketplace.gpus-sub`, "Pick one when you run a
  template, or pass its id as `sku` in the job API.") renders on one line,
  433 px wide with its right edge at 505 px. The visual viewport is 375 but
  the layout viewport grows to 505 in English and 460 in 简体中文, so the
  whole page pans horizontally. Cause: `dashboard/src/app.css`,
  `.section-head .muted { white-space: nowrap; }`. That rule was written
  for the short labels beside section headings, and #115 is the first to
  put a sentence there. The public page is not affected.
  (An earlier read in this session reported "no horizontal scroll" for
  375. It compared `scrollWidth` with `innerWidth`, and both had grown to
  505; the visual viewport is the number that shows the overflow.)

## What this does not prove

- No job was submitted. Choosing a "Coming soon" SKU is prevented only in
  the form; the job API's own refusal of an unavailable SKU was not
  exercised here (compute-api unchanged).
- Account 8 is left on staging: compute off, balance 0, no key. Its
  password was deleted with the scratch files. Accounts 6 and 7 are also
  still there.

## Next

A fix for the subtitle (let it wrap, or move it out of `.section-head`),
merged to main, then a new site build to staging with this same
compute-api, checked at 375 by **visual-viewport width**, and only then
production. Production's step will be `b3871cfc…` → that build.
