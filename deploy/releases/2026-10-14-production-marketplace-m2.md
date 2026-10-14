---
environment: production
action: release
outcome: success
run: 38554449578
commit: f51031060ca9b1fed3816808a36c6a32038818c1
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:d70b1423b1bacdea7edf6b5a61de233c7dcabfc270b18fdab3d988afc7c04313
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:186c4d4483f02e7df5b4ce514386dc70f3633fdf870f136356d3be4ac354d42a
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:de58cc43dadcec5339519dbebe3b21733caba72af9d2af33e150a1ee3fd67f94
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:186c4d4483f02e7df5b4ce514386dc70f3633fdf870f136356d3be4ac354d42a
hotfix: false
---

# 2026-10-14 — marketplace-m2 on production: the same digests staging ran

The digests of [`2026-10-14-staging-marketplace-m2.md`](2026-10-14-staging-marketplace-m2.md),
released after staging's run 38553488166 succeeded and its checks passed.
What shipped is listed there: migration 0010, the listings store, dashboard
listing routes, the admin `listing` verbs and `publisher` on every template
(#96). Site unchanged. No pricing change.

**On production account listings are closed** (`ENVIRONMENT=production`,
ADR-0012 §4): `GET /compute/v1/templates` serves no account listing and
`admin listing approve` refuses. Accounts can still submit, edit and unlist,
and the queue can be read, so a listing posted here waits until the gate
opens.

## Before / after

- Before: compute-api `de58cc43…`, site `186c4d44…` (lora-templates run
  38013108239, then the app-card-padding site release 38019104970).
- Backup: `/data/backups/20261014T110903Z-38554449578`, copied to the
  production backups bucket by `host.sh`.
- Cutover: `RELEASED` compute-api at the digest above; site unchanged; the
  run's `deploy` job is green on the stability check.

## What was exercised (after the run)

- `https://api.compute.unifyai.us/healthz` reports build `f510310`.
- `GET /compute/v1/templates` lists exactly the four platform templates,
  each with `"publisher":"Unify Compute"`, all available: nothing else is
  served, as the gate requires.
- On the host over SSM: `.env` has `ENVIRONMENT=production` and
  `ACCOUNTS_BACKEND=local`; `schema_migrations` latest is `0010_listings`;
  `listings` has 0 rows; `admin listing queue` answers "queue is empty";
  `admin audit`: "balance invariant holds"; 0 ERROR lines in compute-api's
  log in the 15 minutes around the cutover.

## What this does not prove

- As on staging: the dashboard submit → approve → serve → unlist path has
  not run on a host (see the staging record for what it needs). On
  production the approve half cannot run by design until ADR-0012 §4
  holds, which is also why this release is safe to ship without it.
