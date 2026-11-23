---
environment: staging
action: release
outcome: success
run: 45118606281
commit: 80610133dd3a28c7e16aca10182a5b6b8ce5136d
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:577de6b8515cd3ef3778fdb37959df49baf9b4f4ee58ea59bae1a66ce30b0988
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:b0347e7d086c4d736463919a630f54a72d5f5c8416df1bae4756f7e286b164c1
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:577de6b8515cd3ef3778fdb37959df49baf9b4f4ee58ea59bae1a66ce30b0988
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:196535d29f4e09cded19b2e1f956ba744176e223246cbeafeb3f7724467bde38
hotfix: false
---

# 2026-11-23: links to the other UnifyAI products (#164), on staging

## What shipped

Site only. `196535d2…` (2ca4c4f) → `b0347e7d…` (8061013), published by
publish-image run 45118254692 (tag `cross-product-links-2026-11-23`, built
from `sha-80610133…`). compute-api `577de6b8…` (72a4d30) unchanged.

- ec15cb6 feat(site): link the other UnifyAI products from every page. A
  "UnifyAI products" block in the footer of every public page, and a
  homepage section 06 with the six products as cards (UI-STANDARD,
  "Cross-product links"). All ten locales translated; `deploy/site/smoke.sh`
  asserts the links.
- 69e9fe6 fix(site): quote the smoke test's locale prefixes for shellcheck.

The rest of 2ca4c4f..8061013 is the 2026-11-21 release records, cicd.md and
AGENTS.md. No migration (schema 0016), no change to `skus.json`,
`platform.json`, compose, Terraform, env vars or SSM.

## Before / after

- Before (status run 45118258845): `577de6b8…` / `196535d2…`, matching
  `2026-11-21-staging-site-motion-redesign.md`. IMDS lockdown present.
- A first dispatch, run 45118420841, was refused in `plan` before touching
  the host: CI on main for 8061013 was still running, so the provenance gate
  ("on main with CI green") failed. It was re-dispatched after CI run
  45118167258 succeeded.
- Backup `/data/backups/20261123T053505Z-45118606281` (compute 32 tables,
  console 59 tables), copied to
  `s3://unify-compute-staging-backups-871376691052/staging/releases/20261123T053505Z-45118606281/`.
- Only the site container was recreated. All containers showed 0 restarts
  after `up -d` and after 30 s. Smoke passed in full.
- PREVIOUS_RELEASE is `577de6b8…` / `196535d2…`, so `mode=rollback` is a
  site-only step with no dump restore.

## What was exercised

From outside, against `https://site.staging.compute.unifyai.us`
(`staging.compute.unifyai.us` is compute-api and answers 404 for `/`):

1. **Homepage section 06** (curl): the last section before the footer is
   `06 / ALL PRODUCTS`, "Six products. One UnifyAI.", six cards. Five are
   `<a class="product-card" href=…>` to `https://www.unifyapi.ai/`,
   `https://www.gateway.unifyai.us/`, `https://data.unifyai.us/`,
   `https://grid.unifyai.us/`, `https://unifyai.us/` (Unify RSI); Unify
   Compute is `<div class="product-card" aria-current="page">` with the
   "This site" badge and no link.
2. **Footer** of `/`, `/start`, `/privacy`, `/marketplace`, `/zh/start`,
   `/ar/privacy`: five `<a class="product-entry">` and one
   `<span class="product-entry" aria-current="page"><strong>Unify Compute</strong>`.
3. **Language kept**: on `/zh` and `/zh/start` all five hrefs end in `/zh`;
   on `/ar` and `/ar/privacy` in `/ar`. No product link carries `?` or
   `target=`.
4. **`deploy/site/smoke.sh`** against staging: no product-link failure.
   Against production (still on `196535d2…`) the same block failed on every
   page (no product links, no current-site entry), the expected "before".
   The script is written for a single container (`http://127.0.0.1:8080`)
   that also answers the app host; against the public site host its
   app-host checks (`/login`, `/signup`, `/verify`, `app.*` footers) fail
   identically on both hosts, so they are not from this release and were
   excluded from the comparison.
5. **Nothing else moved**: the visible text of `/` (tags stripped) differs
   from production's previous build only by the two added blocks (section
   06 and the footer list); header, hero and sections 01–05 are identical.
6. **Headless Chrome** (playwright-core 1.58.2, system Chrome) on `/`,
   `/zh`, `/ar`, `/privacy` at 1440×900 and 375×812: 6 cards on the home
   pages, 6 footer entries everywhere, none outside the viewport, none at
   opacity 0, `scrollWidth == clientWidth` (no sideways scroll), `/ar` is
   `dir="rtl"`, no page or console errors. This check ran after the
   production release (checks 1–5 were the gate for it); the same check
   on production gave the same results.
