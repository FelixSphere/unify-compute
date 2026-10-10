---
environment: production
action: release
outcome: success
run: 38019104970
commit: 67f76006445e349596670e7bb9bf8d3399b47a31
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:de58cc43dadcec5339519dbebe3b21733caba72af9d2af33e150a1ee3fd67f94
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:186c4d4483f02e7df5b4ce514386dc70f3633fdf870f136356d3be4ac354d42a
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:de58cc43dadcec5339519dbebe3b21733caba72af9d2af33e150a1ee3fd67f94
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:361d43965351ec6bed71cc84794f7c7345c271ee99a52af2bfc22cb021fddfdd
hotfix: false
---

# 2026-10-09 — app-card-padding on production: the same digests staging ran

The digests of [`2026-10-09-staging-app-card-padding.md`](2026-10-09-staging-app-card-padding.md),
released after staging's run 38018448565 succeeded and the scoped rule was
seen in staging's stylesheet. What shipped is listed there: #83, the
dashboard's phone-width card padding scoped to `body.app`. compute-api
unchanged. No pricing change, no migration.

## Before / after

- Before: site `361d4396…` (lora-templates release, run 38013108239).
- Backup: `/data/backups/20261010T030228Z-38019104970`, copied to the
  production backups bucket by `host.sh`.
- Cutover `noop=false`; `RELEASED` the site at the digest above.
- **Two more runs with the same digests followed, 38019228596 and
  38019355507**, dispatched by a release script whose run-detection failed
  on a flaky network and retried. Both reached the host, found the target
  already running, and ended `NOOP` (health verified, nothing changed), as
  `host.sh` is written to. Recorded in their own files so the records check
  sees every successful run.

## What was exercised (from outside, after the run)

- From the staging host (this machine's network to the ALBs was flaky):
  `https://app.compute.unifyai.us/login` serves `app-Bga0LnHc.css`, the same
  stylesheet as staging, containing `body.app .card{padding:20px 16px}` and
  no unscoped `.card{padding:20px 16px}`.

## What this does not prove

- No phone-width screenshot; the proof is the selector in the served
  stylesheet.
