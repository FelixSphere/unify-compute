---
environment: staging
action: release
outcome: success
run: 37180194765
commit: 4d4d9868bdb910a1b639acb7928df275ff1615c1
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:bc9befb77b39acb3e5800d4857cce140893ee2600a70685bbc5e08b99b43a374
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:361d43965351ec6bed71cc84794f7c7345c271ee99a52af2bfc22cb021fddfdd
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:e5966d7d11199c125b59a2e5b480cecbd840d6b2019d1be33ef8eb17907530a8
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:28991b2696a4dc00011d329e1a989ea6c5eaf6082cb08d63af958994f8bd0b0d
hotfix: false
---

# 2026-10-03 — usage-views: usage by day/key/job, date ranges, CSV export, key caps, "Compute & Flatkey"

Operator: "合并，发布，然后继续加功能" (merge, release, keep adding features),
after asking for the Unify API console's usage features to be reproduced here
by behaviour (never by code: the console is AGPL, ADR-0002).

## What shipped

Both images from main `4d4d986` (publish runs 37179719006 compute-api,
37179728449 site). From the previous build `5d065d0`:

- compute-api `bc9befb7…`:
  - #77 `0adfc1a` — `GET /dashboard/v1/usage` gains `jobs`, `by_day`, `by_key`;
    new `GET /dashboard/v1/usage/jobs` (charged jobs, newest settle first,
    SKU/key filters, keyset paging). `5482210` moves the tests to a new file.
  - #79 `19f8afd` — `?from=&to=` on `/usage` and `/usage/jobs`;
    `/transactions?kind=`; new `GET /dashboard/v1/usage/export` (FOCUS CSV,
    product-design C10).
  - #80 `aad376c` — **migration 0009** `api_keys.monthly_cap`; `POST /keys`
    takes `monthly_cap_usd`; new `PATCH /dashboard/v1/keys/{id}`; the local
    biller refuses a hold past the key's cap as `403 key_cap_exceeded`.
  - No pricing change (`skus.json` untouched).
- site `361d4396…`:
  - #71 `91b9617` — header descriptor "Compute & Flatkey" (site header, app
    header, auth card), the partner lockup Unify Data uses for Realset.
  - #77/#79/#80 — the dashboard's usage page (chart, key table, jobs table,
    period control, Export CSV), billing history kind filter, keys page cap
    field, Cap action and "This month / cap" column.

## Before / after

- Before: compute-api `e5966d7d…`, site `28991b26…` (legal-entity release,
  run 37151356901).
- Backup: `/data/backups/20261004T053324Z-37180194765` (compute and console
  dumps, `PREVIOUS_RELEASE`, env), copied to the staging backups bucket by
  `host.sh` as on every release.
- Cutover `noop=false`; `RELEASED` both ECR refs at the digests above; every
  container running with no restart-count change after the stability window
  (the run's `deploy` job is green on that check).
- Migration 0009 (`api_keys.monthly_cap`, one nullable column) ran at
  compute-api boot.

## What was exercised (from outside, after the run)

- Release smoke in the run: `/healthz` names compute-api and reports build
  `4d4d986`; `/compute/v1/skus` is JSON with a priced SKU; `/` is the Unify
  Compute site; `/icon-v4.svg` with `Cache-Control: public`.
- `make smoke BASE_URL=https://staging.compute.unifyai.us`: 14/14 ok,
  including the local-accounts probes (a made-up `uck_` key is refused).
- https://site.staging.compute.unifyai.us/ header reads "Compute & Flatkey"
  (#71).
- `/compute/v1/openapi.json` lists `key_cap_exceeded` (#80).
- New dashboard routes exist on the app host and are gated as specified:
  `GET /dashboard/v1/usage/jobs`, `GET /dashboard/v1/usage/export` and
  `GET /dashboard/v1/usage?from=2026-10-01&to=2026-10-03` answer `401`
  without a session; `PATCH /dashboard/v1/keys/1` answers `403` (CSRF)
  without a same-origin request; an unknown route answers `404`.

## What this does not prove

- The authenticated dashboard views (`/usage`, `/billing`, `/keys`) need a
  session; they were exercised in CI against Postgres (dashboard tests and
  the local-accounts end-to-end scenario), not clicked on staging.
- The three usage reads and the kind filter have no database test of their
  SQL beyond the end-to-end harness; staging's data is the first real run.
