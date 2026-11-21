---
environment: production
action: release
outcome: success
run: 44959603174
commit: e3a46abcf783cccdd21061b6466ebed1ec3b739b
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:577de6b8515cd3ef3778fdb37959df49baf9b4f4ee58ea59bae1a66ce30b0988
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:196535d29f4e09cded19b2e1f956ba744176e223246cbeafeb3f7724467bde38
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:577de6b8515cd3ef3778fdb37959df49baf9b4f4ee58ea59bae1a66ce30b0988
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:297a79748d5e7d09642a3e664c1416e10453b4208510292e4e4c4f76aa660ac7
hotfix: false
---

# 2026-11-21: homepage motion redesign (#161), on production

Same digests as staging (`2026-11-21-staging-site-motion-redesign.md`, run
44958477501, where every check passed). The staging-first gate passed. The
workflow ran from `e3a46ab` (main). That adds only the production
support-conversations record and AGENTS.md on top of 2ca4c4f, and no
release script changed.

## What shipped

See the staging record. Site `297a7974…` → `196535d2…` (2ca4c4f, #161).
compute-api `577de6b8…` unchanged. No migration (schema 0016), no change
to prices, env vars, compose or Terraform.

## Before / after

- Before: the run's own status read gave `577de6b8…` / `297a7974…`, which is
  support-conversations (run 44957938798, finished 22:42Z, before this
  dispatch). IMDS lockdown present.
- Backup `/data/backups/20261121T230123Z-44959603174` (compute, 32 tables),
  copied to
  `s3://unify-compute-production-backups-871376691052/production/releases/20261121T230123Z-44959603174/`.
- Only the site container was recreated. compute-api, postgres and site had
  0 restarts after `up -d` and after 30 s. Smoke passed in full, including
  `compute.unifyai.us` → ALB, `/healthz` being the site container, and HSTS.
- PREVIOUS_RELEASE is `577de6b8…` / `297a7974…`, so `mode=rollback` is a
  site-only step with no dump restore.

## What was exercised

The same checks as staging, from outside:

1. **Static HTML (curl)** on both `https://compute.unifyai.us` and
   `https://site.compute.unifyai.us`, for `/`, `/zh`, `/ar`, `/marketplace`,
   `/start` and `/privacy`: all 200, all serving the new build's stylesheet
   (`index.rcV75Bwn.css`, the same as staging). The homepages carry the
   placement and marketplace strings, with no `reveal` class and no price
   pattern. `/marketplace`, `/start` and `/privacy` were compared against
   this build on staging.
2. **Headless Chrome** on `https://compute.unifyai.us` `/`, `/zh` and `/ar`,
   at 1440 and 375 px, with motion allowed and reduced: all 12 runs
   pass, with results identical to staging. No sideways scroll at 375 px.
   Sections reveal on scroll (4 of 6 or 5 of 6 get `reveal` after
   hydration, and none is left hidden). 124 infinite animations run with
   motion allowed, and 0 run under reduced motion, where no `reveal` class
   is applied. `/ar` mirrors all 12 visible `.rtl-flip` panels. No page or
   console errors.

## What this does not prove

The same as staging. The cosmetic findings recorded there (the 375 px
ellipsis on the placement row, letter-spaced Arabic captions) are live on
production too.
