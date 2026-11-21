---
environment: staging
action: release
outcome: success
run: 44957025547
commit: 72a4d302fb4313480271f4ce022aaac1468712c1
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:577de6b8515cd3ef3778fdb37959df49baf9b4f4ee58ea59bae1a66ce30b0988
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:297a79748d5e7d09642a3e664c1416e10453b4208510292e4e4c4f76aa660ac7
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:15cf14fb57d9a6a12e3483a07865463e33ba4944d49cc8c9225ef1510835c56f
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:b31260dab45bb555c0dd2323c4ec175f6aa4654bd22fb34ec5883db2e618071d
hotfix: false
---

# 2026-11-21: support conversations (#159, ADR-0014), on staging

Staging only. Production stays on `15cf14fb…` / `b31260da…` until the
operator has exercised staging.

## What shipped

Both images, built from `72a4d30` (CI run 44956119358 green; publish runs
44956368738 for compute-api, 44956371574 for the site; both manifest lists
carry linux/arm64).

- compute-api `15cf14fb…` (792b4ea) → `577de6b8…` (72a4d30).
- site `b31260da…` (47f54d8) → `297a7974…` (72a4d30).

Between the previous builds and this one, on main:

- `9bd26e3`/`78ffe36` #156: the compute-descriptor records. Docs only.
- `536dce7` #158: AGENTS.md, test changes and PRs no longer need the
  operator's LGTM. Docs only.
- `ee50309`, `b4d3957`, `03a61ce`, merged as `72a4d30` #159: customer
  support conversations (ADR-0014). compute-api: migration
  `0016_support.sql` (new tables `support_conversations`,
  `support_messages`; additive, `IF NOT EXISTS`, nothing existing altered),
  routes under `/dashboard/v1/support/conversations`, admin verbs
  `admin support list|show|reply|close`, a support-reply email and an
  operator notice (to `OPERATOR_EMAIL`, an existing optional variable). Site:
  the app-host page `/support` and its strings in the ten other languages.

No change to `skus.json`, `platform.json`, compose, Terraform, workflows,
env vars or SSM parameters.

## Before / after

- Before (status run 44956180265): `15cf14fb…` / `b31260da…`, matching the
  2026-11-11 record. IMDS lockdown present.
- Backup `/data/backups/20261121T223033Z-44957025547` (compute 30 tables,
  console 59 tables), copied to
  `s3://unify-compute-staging-backups-871376691052/staging/releases/20261121T223033Z-44957025547/`.
  The cutover pruned `/data/backups/20261015T105729Z-38723813378`.
- compute-api and site recreated; every container 0 restarts after up and
  after 30 s stable.
- PREVIOUS_RELEASE is now `15cf14fb…` / `b31260da…`.

## What was exercised

- Release smoke (in the run): healthz reports build `72a4d30…`; skus, 401
  `missing_key`; site `/`, `/privacy`, icon; app host `/`, session route
  reaches compute-api.
- `make smoke BASE_URL=https://staging.compute.unifyai.us` from outside:
  all ok.
- The app-page checks of `deploy/site/smoke.sh` (status 200, title, page
  script, no inline script/style) against `https://app.staging.compute.unifyai.us`
  for all 17 pages, including `/support` ("Support — Unify Compute"): all
  ok. The whole script was not run against the ALB because it is written for
  the container (Host-header overrides, header assertions the ALB changes).
- `GET /dashboard/v1/support/conversations` without a session: 401 JSON.
- On the host over SSM (read-only): both containers run the ECR refs of the
  two digests above; `schema_migrations` has `0016_support` applied
  22:30:37Z; both support tables exist with 0 rows;
  `admin support list` exit 0 ("no conversation is waiting"); compute-api
  logs since boot: 36 INFO, 2 WARN (CONSOLE_URL ignored in local mode;
  staging has no Stripe keys, both expected), 0 ERROR.

## What this does not prove

- Nothing was written: no conversation was created, no staff reply was
  sent, and neither email (support-reply to the customer, notice to the
  operator) went out. The operator runs that exercise next (customer writes
  → operator replies → customer sees it and is mailed).
- `/support` was checked as a served page, not rendered in a browser.

## Rolling back

`mode=rollback` returns to `15cf14fb…` / `b31260da…`. 0015's image ignores
the two 0016 tables, so no dump restore is needed; any conversations written
in the meantime stay in the tables, unused.
