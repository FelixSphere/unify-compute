---
environment: production
action: release
outcome: success
run: 37180336504
commit: 4d4d9868bdb910a1b639acb7928df275ff1615c1
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:bc9befb77b39acb3e5800d4857cce140893ee2600a70685bbc5e08b99b43a374
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:361d43965351ec6bed71cc84794f7c7345c271ee99a52af2bfc22cb021fddfdd
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:e5966d7d11199c125b59a2e5b480cecbd840d6b2019d1be33ef8eb17907530a8
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:28991b2696a4dc00011d329e1a989ea6c5eaf6082cb08d63af958994f8bd0b0d
hotfix: false
---

# 2026-10-03 — usage-views on production: the same digests staging ran

The digests of [`2026-10-03-staging-usage-views.md`](2026-10-03-staging-usage-views.md),
released to production after staging's run 37180194765 succeeded and its
public checks passed. What shipped is listed there: #71 "Compute & Flatkey",
#77 usage by day/key/job, #79 date ranges, `kind` filter and FOCUS CSV
export, #80 per-key monthly caps with migration 0009. No pricing change.

## Before / after

- Before: compute-api `e5966d7d…`, site `28991b26…` (legal-entity release,
  run 37151500537).
- Backup: `/data/backups/20261004T053611Z-37180336504`, copied to the
  production backups bucket by `host.sh`.
- Cutover `noop=false`; `RELEASED` both ECR refs at the digests above; the
  run's `deploy` job is green on the stability check.
- Migration 0009 (`api_keys.monthly_cap`) ran at compute-api boot. Production
  has no customer accounts yet (ADR-0011 D3: compute off by default), so the
  cap is live code with nothing to cap.

## What was exercised (from outside, after the run)

- `https://api.compute.unifyai.us/healthz` reports build `4d4d986`.
- `make smoke BASE_URL=https://api.compute.unifyai.us`: 16 ok, no FAIL.
- `https://compute.unifyai.us/` header reads "Compute & Flatkey".
- `/compute/v1/openapi.json` lists `key_cap_exceeded`.
- `GET https://app.compute.unifyai.us/dashboard/v1/usage/jobs` and
  `/usage/export` answer `401` without a session (routes present, gated).

## What this does not prove

- No authenticated dashboard view was clicked on production; there is no
  production account to log in with yet. CI's Postgres-backed dashboard and
  end-to-end tests are the evidence for those paths.
