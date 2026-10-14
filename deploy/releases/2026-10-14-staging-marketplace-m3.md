---
environment: staging
action: release
outcome: success
run: 38571762314
commit: d066488176f314ce8bee2214d096483e8f5b9a69
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:de7ce94deae0aaa3b38bc46b1a855f1665d83e71a63bbb09b4c69e4fa056e057
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:b9da4d906abd0fe20277e2645449dc9cae7761b89818ecdf48d5186903347a12
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:d70b1423b1bacdea7edf6b5a61de233c7dcabfc270b18fdab3d988afc7c04313
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:186c4d4483f02e7df5b4ce514386dc70f3633fdf870f136356d3be4ac354d42a
hotfix: false
---

# 2026-10-14 — marketplace-m3: run templates and manage listings from the dashboard

Operator: "继续做 M3", then "合并，发布" after PR #99 was green.

## What shipped

compute-api `de7ce94d…` and site `b9da4d90…`, both from main `d066488`
(publish runs 38570484100 and 38570496891). From the previous builds
(compute-api `f510310`, site `b87ec85`):

- #99 marketplace M3 (ADR-0012, `docs/spec/marketplace.md`):
  `GET /dashboard/v1/templates`, `GET /dashboard/v1/skus` and
  `POST /dashboard/v1/jobs`, the job API's own handlers run for the
  session's account (`dashboard.MarketRoutes`); a session submit has no
  key, so no key cap applies and the job's `token_id` is null. Pages
  `/marketplace` (tiles with the publisher of record and a "Run this" form
  per template) and `/listings` (the account's listings: review state and
  reason, submit as one templates.json entry, edit, unlist). Nav gains
  Marketplace and My listings.
- The site image also carries main's `app/` as of `d066488`, which has not
  changed since the site's previous build apart from the app pages above
  (#95's marketplace pages were already in `186c4d44…`? No: `186c4d44…` was
  built from `b87ec85`, before #95 merged; this site build is the first to
  carry the M1 `/marketplace` site pages as well).
- No pricing change, no migration.

## Before / after

- Before: compute-api `d70b1423…`, site `186c4d44…` (marketplace-m2 run
  38553488166; site from app-card-padding run 38018448565). The
  `mode=status` read before dispatch is run 38570516409 (same values).
- Backup: `/data/backups/20261014T132712Z-38571762314`, copied to the
  staging backups bucket by `host.sh`.
- Cutover: `RELEASED` both images at the digests above; the run's `deploy`
  job is green on the stability check.

## What was exercised (after the run)

- `https://staging.compute.unifyai.us/healthz` reports build `d066488`.
- `https://app.staging.compute.unifyai.us/marketplace` and `/listings`
  answer 200 with their titles; `GET /dashboard/v1/templates` without a
  session is `401` (the route exists and is session-guarded).
- `GET /compute/v1/templates` lists the four platform templates with
  `publisher: "Unify Compute"`.
- On the host over SSM: the site container serves `marketplace.html` and
  `listings.html`; `admin listing queue` answers "queue is empty";
  `admin audit`: "balance invariant holds"; 0 ERROR lines in compute-api's
  log after the cutover.
- `make smoke BASE_URL=https://staging.compute.unifyai.us`: all probes ok,
  including the two new app pages the smoke now checks.

## What this does not prove

- **No template was run from the dashboard on this host**, and no listing
  was submitted from `/listings`: both need a verified session on an
  account with compute enabled, which this agent does not have (see the
  marketplace-m2 staging record). The paths are held by
  `internal/api/market_e2e_test.go` (a session submit ends `201` with a
  null token id, an account-scoped hold and one launch) and by a browser
  check of the built pages against a mock API, not by a host yet.
  **Next:** the operator logs in at `app.staging.compute.unifyai.us`;
  an agent runs `admin account enable-compute <id> --reason …` and
  `admin balance credit` over SSM; the operator runs `batch-infer-vllm`
  from `/marketplace` and submits one listing from `/listings`.
