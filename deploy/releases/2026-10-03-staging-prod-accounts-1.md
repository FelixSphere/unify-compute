---
environment: staging
action: release
outcome: success
run: 37145018753
commit: ec3fb28d3859d9163ced3d2aee82c08ef4b73f2c
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:d5344254507f4a39eec78076e948d21c98d8446696650da393824006d98701da
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:48aa37ffeca00fcbd85889d3a8d7b780eeea3d15071606ab4bf7c9bc02736146
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:40e08796c20704762896ecef877a2039ce8e2de9524df443db94a366556e5a7f
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:48aa37ffeca00fcbd85889d3a8d7b780eeea3d15071606ab4bf7c9bc02736146
hotfix: false
---

# 2026-10-03 — prod-accounts-1 on staging: compute-api 6c1d796

Staging-first step for the production cutover (C1). compute-api `d5344254…`
from main `6c1d796` (publish run 37144381133): over 3871bb5 it adds #68
(terms and privacy text) and #69 (Stripe top-ups, migration 0008, dormant
without keys). No change under `compute-api/config/`. The site is unchanged.

Before the release, staging got 6c1d796's `docker-compose.yml` (the Stripe
pass-throughs) over SSM under the ops lock (previous files in
`/data/backups/20261003T183846Z-manual-compose-stripe-passthrough/`).
Staging has no Stripe keys (checked: both lengths 0).

Backup `/data/backups/20261003T184013Z-37145018753` (S3 copy). Smoke passed:
healthz build 6c1d796, the app CSP enforced, and `/dashboard/v1/session` from
compute-api.
