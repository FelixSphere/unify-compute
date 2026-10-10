---
environment: staging
action: release
outcome: success
run: 38018448565
commit: 67f76006445e349596670e7bb9bf8d3399b47a31
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:de58cc43dadcec5339519dbebe3b21733caba72af9d2af33e150a1ee3fd67f94
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:186c4d4483f02e7df5b4ce514386dc70f3633fdf870f136356d3be4ac354d42a
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:de58cc43dadcec5339519dbebe3b21733caba72af9d2af33e150a1ee3fd67f94
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:361d43965351ec6bed71cc84794f7c7345c271ee99a52af2bfc22cb021fddfdd
hotfix: false
---

# 2026-10-09 — app-card-padding: the dashboard's mobile card padding scoped to the app shell

Operator: "合并，发布" with #83 the one open code PR.

## What shipped

site `186c4d44…` (publish run 38018167840, from main after #83). compute-api
unchanged (`de58cc43…`). From the previous site build:

- #83 `2914a11` — `dashboard/src/app.css`: the `@media` card padding override
  for phones is `body.app .card` instead of `.card`, so it no longer leaks
  into the marketing site's cards. No pricing change, no migration.

## Before / after

- Before: compute-api `de58cc43…`, site `361d4396…` (lora-templates release,
  run 38012888002).
- Backup: `/data/backups/20261010T025132Z-38018448565`, copied to the
  staging backups bucket by `host.sh`.
- Cutover: `RELEASED` the site at the digest above; compute-api unchanged.

## What was exercised (from outside, after the run)

- From the staging host (this machine's network to the ALBs was flaky):
  `https://app.staging.compute.unifyai.us/login` serves a new stylesheet
  (`app-Bga0LnHc.css`, production still `app-CzamDT5H.css`), and it contains
  the scoped rule `body.app .card{padding:20px 16px}`.
- `https://staging.compute.unifyai.us/healthz` reports build `73e43b6`
  (compute-api unchanged).

## What this does not prove

- No phone-width screenshot was taken; the proof is the rule's selector in
  the served stylesheet.
