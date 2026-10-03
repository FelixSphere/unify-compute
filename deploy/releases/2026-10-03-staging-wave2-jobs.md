---
environment: staging
action: release
outcome: success
run: 37091108250
commit: 667b3816f0d217829b95a9a8793d8ad4b51132ef
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:cfbbbdd90db29c223f1ad5483be798b41bd9950dd645064cbc5bfc8656fc6b0b
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:7b9bb6596a8cba954dccd8ff8bd4922eaf23d43d54b0ea9fe8a9b7315bace4ea
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:ec4bd964372acf9724365273204c556414a3090ee68035037b0c41b21e7d9902
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:7b9bb6596a8cba954dccd8ff8bd4922eaf23d43d54b0ea9fe8a9b7315bace4ea
hotfix: false
---

# 2026-10-03 — wave2-jobs: the job API stops answering 501

Staging only. **Production was deliberately not released** and still runs
`ec4bd964…` (`a8562dd`); see "Why production did not follow" below. Nothing
here was a hotfix.

## What shipped

compute-api: `cfbbbdd9…`, built from main `667b381` (publish run 37090558502,
arm64 asserted on the published manifest).

The previous compute-api build was `a8562dd`, not the previous release commit
`ea56779` — the two images had different baselines, so the delta is
`a8562dd..667b381`: 64 commits, ~32 touching `compute-api/`, 26,484 insertions
across 147 files.

- **#15** `c3-openapi` — serves an OpenAPI 3.1 contract at
  `/compute/v1/openapi.json`; CI fails when routes drift from it.
- **#17** `s2-bridge-patch` — docs only, console-billing-bridge spec revision.
- **#18** `c7-templates` — curated job templates validated at boot;
  `GET /compute/v1/templates`.
- **#19** `c6-events` — append-only job event log, webhook delivery worker,
  events route.
- **#20** `c8-quotas` — per-owner concurrency limits, `403 quota_exceeded`,
  per-key `429 rate_limited`.
- **#21** `c5-ledger` — GPU-seconds ledger and settlement engine; usage, hold
  and attempt tables.
- **#22** `c4-skypilot-backend` — drives SkyPilot clusters through the 0.13.0
  API server.
- **#38** `artifacts` — job outputs to S3 via per-file presigned uploads;
  stdlib SigV4, instance-role credentials (ADR-0005). **Dark, see below.**
- **#39** `gpu-infra` — Terraform for GPU workers in the shared account, the
  reaper and a budget stack (ADR-0010). **Not applied, see below.**
- **#40** `wire-jobs` — wires job submission end to end; submit stops
  answering 501.
- **#42** `resume-live-jobs` — resumes live jobs after a restart, with one
  driver per database elected through a worker lock.
- **#41** `sku-interconnect` — adds `interconnect` (`pcie`|`nvlink`) and
  `network` (`ethernet`|`infiniband`) to the SKU schema, validated inside
  `sku.Parse` at boot. Both are `omitempty` and every shipped SKU is
  single-GPU, so neither appears in `/compute/v1/skus`.

**No price change.** `git diff ea56779..667b381 -- compute-api/config/skus.json`
is empty: no SKU was added, removed, repriced or made available. The two new
files under `compute-api/config/` are `quotas.json` (#20) and `templates.json`
(#18).

site: **unchanged**, deliberately held at `7b9bb659…` (`ea56779`). Nothing in
the delta touches `app/`, `components/`, `public/`, `lib/` or `package.json`,
so there was nothing to rebuild. `previous_site_image` is therefore the same
digest as `site_image`.

## This was a data release

Not obvious from the diff, and the next person needs it:
`deploy/compose/docker-compose.yml` has always passed `DATABASE_URL` to
compute-api, but `a8562dd`'s `main.go` never read it
(`git show a8562dd:compute-api/cmd/compute-api/main.go` has no `DATABASE_URL`,
no `openStore`, no `Migrate`). This build does: `openStore` calls
`store.Migrate` at boot and **`os.Exit(1)` on failure**.

So **there are two crash-loop paths in this image, not one**. The known one is
`sku.Parse` rejecting the catalogue (#41). The second is a failed migration.
If compute-api crash-loops after a deploy of this image or later, read the
container log for `sku catalogue:` / `interconnect` **and** for `job store`.

The pre-release `pg_dump` recorded `compute 814 bytes, 0 tables` — the compute
database had never been migrated, so this was a create-from-empty, the benign
case. A future release migrating a populated database is not this, and the
dry-run-against-a-restored-dump discipline applies to it.

The migration created 10 tables: `holds`, `idempotency_keys`,
`job_cancel_requests`, `job_events`, `jobs`, `metering_attempts`,
`schema_migrations`, `usage_events`, `webhook_deliveries`, `webhooks`.

Rollback is clean despite the migration: `ec4bd964…` ignores `DATABASE_URL`
entirely, so the 10 tables would sit unused rather than break it.

## #38 and #39 shipped dark

The binary carries artifact storage and the SkyPilot backend; **no Terraform
was applied**, so neither is reachable. This was deliberate — the release was
kept to one thing, the image.

Boot log on the host:

```
WARN  ARTIFACTS_BUCKET or ARTIFACTS_TOKEN_SECRET unset; spec.outputs cannot be uploaded and artifact routes answer 501
WARN  SKYPILOT_API_SERVER_URL unset; job submission answers 503 upstream_unavailable
```

The host's `.env` and `docker-compose.yml` predate those variables, and
`scripts/release/host.sh` only rewrites the image pins — it does not touch the
compose file — so a release alone can never turn these on.

What #38/#39 still need, which is a separate operation: the artifacts S3
bucket and writer role (`deploy/aws/modules/compute-stack/artifacts.tf`), the
`gpu-workers` module, the shared `gpu-budget` stack, the SkyPilot compose
overlay, the `user_data` change and the IMDSv2
`http_put_response_hop_limit = 2`. **Applying that replaces the instance** —
`ignore_changes = [user_data, user_data_base64]` means it is otherwise inert,
and the module's own comment requires the replace to be run on staging before
production. The data volume survives (`prevent_destroy`); the containers do
not.

## Before / after

- `status` before dispatch (run 37090442081): compute-api `ec4bd964…`, site
  `7b9bb659…` — **identical to production**, so the delta staging took is the
  delta production would take.
- Backup: `/data/backups/20261003T024950Z-37091108250`, copied to
  `s3://unify-compute-staging-backups-871376691052/staging/releases/20261003T024950Z-37091108250/`
  (`compute` 814 bytes / 0 tables, `console` 181591 bytes / 51 tables).
- compute-api was replaced (`7d25682e…` → `f01c293f…`); console, postgres,
  redis and site kept their container ids. **All five at 0 restarts, running,
  after the 30s stability window** — no crash loop on either path above.

## What was exercised

Release smoke (in the run): `healthz names compute-api`, `healthz reports
build 667b3816f0d217829b95a9a8793d8ad4b51132ef`, `skus is JSON`, `skus lists
at least one SKU`, `skus carry a price`, site `/` and icon `Cache-Control`.

Then from outside, against `https://staging.compute.unifyai.us`, with the
local HTTP proxy bypassed (`--noproxy '*'`) and the hostname pinned to a
staging ALB IP first checked to be disjoint from production's
(staging `44.195.241.99`/`184.193.100.59`, production `34.195.70.165`/
`44.198.223.1`):

- `/healthz` 200, `build` = `667b3816f0d217829b95a9a8793d8ad4b51132ef` — the
  exact commit, not a rebuild.
- `/compute/v1/skus` — 4 SKUs, all `gpu_count: 1`, zero occurrences of
  `interconnect` or `network`. Fetched the same endpoint from production,
  which still runs `a8562dd`: the two responses are **byte-for-byte
  identical, 1926 bytes each**. #41 changed the schema and not the payload.
- `/compute/v1/openapi.json` 200 — `SKU.interconnect` enum
  `[pcie, nvlink]`, `SKU.network` enum `[ethernet, infiniband]`. Production
  404s on this route; it did not exist in `a8562dd`.
- `GET /compute/v1/jobs` → **401 `missing_key`** (was 501): the job store is
  open and auth is in front of it.
- `GET /compute/v1/templates` → **200**, 3 templates.
- `POST /compute/v1/jobs` with a deliberately invalid key → **503
  `console_unavailable`** (was 501). Wired; stops at key verification.
- Boot log: `job store open quotas_enforced=true overrides=0`,
  **`worker lock acquired; driving jobs`** (#42's election), and
  `listening build=667b3816… skus=4 templates=3`. No `sku catalogue:` error —
  #41's validation accepted the shipped catalogue.
- `\dt` on the host's `compute` database: the 10 tables listed above.

## What this does not prove

- **That a job can be submitted, run, or billed.** It cannot be, anywhere.
  The host log shows `ERROR "console verify" err="console bridge: 404"` — the
  UnifyAPI console fork does not implement the billing bridge
  (`docs/spec/console-billing-bridge.md` is still a spec). Every keyed job
  route therefore stops at key verification. This is pre-existing and was not
  caused by this release.
- That artifact upload or SkyPilot launch work — neither is configured; see
  "#38 and #39 shipped dark". The `/compute/v1/jobs` routes answering is not
  evidence that the backend behind them exists.
- That a populated database migrates cleanly. This one had 0 tables.
- That `#43` is covered. It merged to `main` (`dd9fbd3`) **after** this
  release and is not in `cfbbbdd9…`; it also edits both copies of
  `openapi.json`, so the document verified above is already behind `main`.

## Why production did not follow

Staging-first was satisfied and production was still a no-go, on purpose. The
open question is whether it is right to put a job API into production that
returns 503 to every keyed request, with artifact storage and the SkyPilot
backend dark behind it, while the console bridge does not exist. That is a
product decision, not a release-gate one, and it was referred to the operator.
When production does go, it takes **these same digests** — not a rebuild of a
later `main` — or the staging-first gate has tested the wrong delta.

Production will take the same create-from-empty migration (it runs postgres
and passes `DATABASE_URL` too) and, having no console and no bridge secret,
will answer `503 console_unavailable` on keyed routes rather than 401.
