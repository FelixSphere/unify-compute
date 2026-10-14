---
environment: production
action: release
outcome: success
run: 38572473686
commit: d066488176f314ce8bee2214d096483e8f5b9a69
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:de7ce94deae0aaa3b38bc46b1a855f1665d83e71a63bbb09b4c69e4fa056e057
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:b9da4d906abd0fe20277e2645449dc9cae7761b89818ecdf48d5186903347a12
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:d70b1423b1bacdea7edf6b5a61de233c7dcabfc270b18fdab3d988afc7c04313
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:186c4d4483f02e7df5b4ce514386dc70f3633fdf870f136356d3be4ac354d42a
hotfix: false
---

# 2026-10-14 — marketplace-m3 on production: the same digests staging ran

The digests of [`2026-10-14-staging-marketplace-m3.md`](2026-10-14-staging-marketplace-m3.md),
released after staging's run 38571762314 succeeded and its checks passed.
What shipped is listed there: the dashboard's marketplace routes and the
`/marketplace` and `/listings` pages (#99), and the site image's first build
carrying #95's public marketplace pages. No pricing change, no migration.

Account listings stay closed here (`ENVIRONMENT=production`, ADR-0012
§4): `/listings` lets an account submit and edit, and says above its table
that the deployment does not serve listings to buyers yet. Running a
platform template from `/marketplace` is live for any account with compute
enabled; today GPUs are off in production, so a submit ends `503` after
validation, as a key submit does.

## Before / after

- Before: compute-api `d70b1423…`, site `186c4d44…` (marketplace-m2 run
  38554449578; site from app-card-padding run 38019104970).
- Backup: `/data/backups/20261014T133217Z-38572473686`, copied to the
  production backups bucket by `host.sh`.
- Cutover: `RELEASED` both images at the digests above; the run's `deploy`
  job is green on the stability check.

## What was exercised (after the run)

- `https://api.compute.unifyai.us/healthz` reports build `d066488`;
  `https://compute.unifyai.us/` 200.
- `https://app.compute.unifyai.us/marketplace` and `/listings` answer 200
  with their titles; `GET /dashboard/v1/templates` without a session is
  `401`.
- `GET /compute/v1/templates` lists exactly the four platform templates
  with `publisher: "Unify Compute"`.
- On the host over SSM: the site container serves both new pages;
  `admin listing queue`: "queue is empty"; `admin audit`: "balance
  invariant holds"; 0 ERROR lines in compute-api's log after the cutover.
- `make smoke BASE_URL=https://api.compute.unifyai.us`: 16 ok, no FAIL.

## What this does not prove

- As on staging: no dashboard submit and no listing submission has run on
  a host yet (no usable verified session). The staging record says what
  the operator has to do next.
