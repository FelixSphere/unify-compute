---
environment: staging
action: release
outcome: success
run: 44958477501
commit: 2ca4c4f2696d89218780103531a9cb5c44ecadd9
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:577de6b8515cd3ef3778fdb37959df49baf9b4f4ee58ea59bae1a66ce30b0988
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:196535d29f4e09cded19b2e1f956ba744176e223246cbeafeb3f7724467bde38
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:577de6b8515cd3ef3778fdb37959df49baf9b4f4ee58ea59bae1a66ce30b0988
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:297a79748d5e7d09642a3e664c1416e10453b4208510292e4e4c4f76aa660ac7
hotfix: false
---

# 2026-11-21: homepage motion redesign (#161), on staging

## What shipped

Site only. `297a7974…` (72a4d30) → `196535d2…` (2ca4c4f), published by
publish-image run 44958130200 (tag `site-motion-redesign-2026-11-21`).
compute-api `577de6b8…` (72a4d30) unchanged.

- 47d6a41 feat(site): a looping panel in every homepage section, in the
  unifyapi.ai style. Adds new sections 03 Placement and 04 Marketplace, and
  renumbers consumption to 05. All ten locales are translated.
- e04572d test(site): every animation stops under prefers-reduced-motion
  (`npm test`, in the CI site job).

The rest of 72a4d30..2ca4c4f is the staging support-conversations record and
cicd.md. The dashboard (app host) does not import `app/globals.css` and is
unaffected. No migration (schema 0016), no change to `skus.json`,
`platform.json`, compose, Terraform, env vars or SSM.

## Before / after

- Before (status run 44958405786): `577de6b8…` / `297a7974…`, matching
  `2026-11-21-staging-support-conversations.md`. IMDS lockdown present.
- Backup `/data/backups/20261121T224727Z-44958477501` (compute 32 tables,
  console 59 tables), copied to
  `s3://unify-compute-staging-backups-871376691052/staging/releases/20261121T224727Z-44958477501/`.
- Only the site container was recreated. All five containers showed 0
  restarts after `up -d` and after 30 s. Smoke passed in full.
- PREVIOUS_RELEASE is `577de6b8…` / `297a7974…`, so `mode=rollback` is a
  site-only step with no dump restore.

## What was exercised

Run from outside against `https://site.staging.compute.unifyai.us`:

1. **Static HTML (curl)** of `/`, `/zh`, `/ar`: 200. Each contains
   `a10g-24gb-x1`, `aws · g5.xlarge · us-east-1`, `batch-embed`,
   `batch-extract-sglang` and `batch-infer-lora`. No `reveal` class is in
   the exported HTML, and nothing matches `$<digit>`, `USD <digit>`, `/hr` or
   `per hour`. `/ar` is `<html lang="ar" dir="rtl">`.
2. **`/marketplace`, `/start`, `/privacy`**: 200. Their markup equals
   production's (then on the previous build) except for hashed asset names.
3. **Headless Chrome** (playwright-core, system Chrome) on `/`, `/zh` and
   `/ar`, at 1440×900 and 375×812, with motion allowed and with reduced
   motion. That is 12 runs, all passing:
   - `scrollWidth == clientWidth` in every run, so nothing scrolls sideways
     at 375 px;
   - with motion allowed: after hydration, 4 of 6 sections (desktop) or 5 of
     6 (phone) get `reveal`. After scrolling to the bottom none is left
     hidden and none is at opacity 0. 124 infinite animations are running,
     i.e. the panels loop;
   - with reduced motion: no section gets `reveal`, 0 animations are running,
     and all content is visible;
   - `/ar`: all 12 visible `.rtl-flip` panels compute to
     `matrix(-1, 0, 0, 1, 0, 0)`, so they flow right to left. On `/` and
     `/zh` none is mirrored. The 5 vertical conveyors that are hidden at a
     given width are excluded;
   - no page errors or console errors.
4. Screenshots of the placement and marketplace panels at both widths,
   looked at by eye.

## Found, not blocking

- At 375 px the placement row `aws · g5.xlarge · us-east-1` is 1 px wider
  than its box (227 vs 226), so it renders as `aws · g5.xlarge · us-east…`.
  It is complete at desktop width and in the DOM. This is cosmetic, in all
  locales.
- `/ar`: the panel captions (`.panel-caption`, mono, `letter-spacing: 1px`)
  are letter-spaced Arabic, which visibly breaks the cursive joining (for
  example the "placements in order" label). This is cosmetic.
- The reveal effect runs at hydration, which on a cold load comes several
  seconds after `load`. Third-party HubSpot requests dominate that time, and
  production's previous build showed the same load timings. Before
  hydration the page is static and fully visible, as designed.

## What this does not prove

Nobody watched the animations play in a real browser or on a phone, and no
real device was used. "Loops" means the Web Animations API reports
running, infinite animations, not that the drawing looks right in motion.
Only `/`, `/zh` and `/ar` were checked of the eleven locales. The other
seven were not rendered.
