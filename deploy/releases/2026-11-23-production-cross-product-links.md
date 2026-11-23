---
environment: production
action: release
outcome: success
run: 45119402513
commit: 80610133dd3a28c7e16aca10182a5b6b8ce5136d
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:577de6b8515cd3ef3778fdb37959df49baf9b4f4ee58ea59bae1a66ce30b0988
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:b0347e7d086c4d736463919a630f54a72d5f5c8416df1bae4756f7e286b164c1
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:577de6b8515cd3ef3778fdb37959df49baf9b4f4ee58ea59bae1a66ce30b0988
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:196535d29f4e09cded19b2e1f956ba744176e223246cbeafeb3f7724467bde38
hotfix: false
---

# 2026-11-23: links to the other UnifyAI products (#164), on production

Same digests as staging (`2026-11-23-staging-cross-product-links.md`, run
45118606281, where every check passed). The staging-first gate passed. The
workflow ran from `8061013`, the same commit as staging.

## What shipped

See the staging record. Site `196535d2…` → `b0347e7d…` (8061013, #164).
compute-api `577de6b8…` unchanged. No migration (schema 0016), no change
to prices, env vars, compose or Terraform.

## Before / after

- Before (status run 45119315157): `577de6b8…` / `196535d2…`, matching
  `2026-11-21-production-site-motion-redesign.md`. IMDS lockdown present.
- Backup `/data/backups/20261123T054253Z-45119402513`, copied to
  `s3://unify-compute-production-backups-871376691052/production/releases/20261123T054253Z-45119402513/`.
- Only the site container was recreated. compute-api, postgres and site had
  0 restarts after `up -d` and after 30 s. Smoke passed in full.
- PREVIOUS_RELEASE is `577de6b8…` / `196535d2…`, so `mode=rollback` is a
  site-only step with no dump restore.

## What was exercised

Against `https://compute.unifyai.us` after the run:

1. `/`, `/start`, `/privacy`, `/marketplace`, `/zh`, `/ar`: five footer
   product links and the Unify Compute current-site entry on each; `/zh`
   and `/ar` hrefs carry their locale; `/` has five product-card links.
2. `deploy/site/smoke.sh`: no product-link failure (it had failed every
   page before the release).
3. Headless Chrome on `/`, `/zh`, `/ar`, `/privacy` at 1440×900 and
   375×812: all cards and entries visible and inside the viewport, no
   sideways scroll, no page or console errors. Screenshots of section 06 at
   375 px (`/`) and 1440 px (`/ar`, right to left) looked at by eye.
