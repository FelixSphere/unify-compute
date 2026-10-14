---
environment: staging
action: release
outcome: success
run: 38553488166
commit: f51031060ca9b1fed3816808a36c6a32038818c1
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:d70b1423b1bacdea7edf6b5a61de233c7dcabfc270b18fdab3d988afc7c04313
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:186c4d4483f02e7df5b4ce514386dc70f3633fdf870f136356d3be4ac354d42a
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:de58cc43dadcec5339519dbebe3b21733caba72af9d2af33e150a1ee3fd67f94
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:186c4d4483f02e7df5b4ce514386dc70f3633fdf870f136356d3be4ac354d42a
hotfix: false
---

# 2026-10-14 — marketplace-m2: account listings with brokered review

Operator: "第 2 项，先读 Data 的 listing 实现" (2026-10-13), then
"feat/template-marketplace-m2 发布吗？" after PR #96 was green.

## What shipped

compute-api `d70b1423…` from main `f510310` (publish run 38513064098). Site
unchanged (`186c4d44…`). From the previous compute-api build `73e43b6`:

- #96 marketplace M2 (ADR-0012, `docs/spec/marketplace.md`): **migration
  0010** adds `listings` and `jobs.template_id`; `internal/listings` with
  the brokered review ported from Unify Data (any edit returns a listing to
  pending, rejection unlists, unlisting is never refused, every decision
  audited in the same transaction); dashboard `GET|POST /listings`,
  `PATCH /listings/{id}`, `POST /listings/{id}/unlist`; `admin listing
  queue | show | approve | reject | unlist --reason`; every template in
  `GET /compute/v1/templates` carries `publisher: "Unify Compute"`, and a
  submit may name an approved listing's id. Jobs now record the template id
  they were submitted with.
- #97 docs and agent files only (performance agent, AGENTS.md status).
- No pricing change (`skus.json` untouched).

**Gate:** with `ENVIRONMENT=staging` account listings are *open* here:
approved, listed ones are served to buyers and `admin listing approve`
works. On production (`ENVIRONMENT=production`) both are closed until
ADR-0012 §4 holds.

## Before / after

- Before: compute-api `de58cc43…`, site `186c4d44…` (lora-templates run
  38012888002, then the app-card-padding site release 38018448565). The
  `mode=status` read before dispatch is run 38513080890 (same values).
- Backup: `/data/backups/20261014T110107Z-38553488166`, copied to the
  staging backups bucket by `host.sh`.
- Cutover: `RELEASED` compute-api at the digest above; site unchanged; the
  run's `deploy` job is green on the stability check.

## What was exercised (after the run)

- `https://staging.compute.unifyai.us/healthz` reports build `f510310`.
- `GET /compute/v1/templates` lists the four platform templates, each with
  `"publisher":"Unify Compute"`, all available.
- On the host over SSM: `schema_migrations` has `0010_listings` applied at
  11:01:12Z; `listings` has the eleven columns of the migration and `jobs`
  has `template_id`; `admin listing queue` answers "queue is empty";
  `admin audit`: "balance invariant holds"; no ERROR lines in compute-api's
  log after the cutover.
- Both CI/CD agent attempts before this run (one cut off by an API rate
  limit, one stalled) dispatched nothing but the image publish and the
  status read; verified from the run lists before dispatching.

## What this does not prove

- **The publisher's path was not exercised on a host:** submit through
  `POST /dashboard/v1/listings` → absent from `/templates` → `admin listing
  approve` → present with the publisher → `unlist` → absent. It needs a
  verified session on an account with compute enabled. Staging's only
  verified account (id 3) is the operator's, with compute disabled, and no
  session is available to this agent; enabling compute on it or inserting a
  listing by SQL would bypass exactly the path under test, so neither was
  done. The flow is held by the new tests (`internal/listings`,
  `internal/dashboard`, `internal/admin`, `internal/api` listing tests)
  against Postgres, not by a host yet. **Next:** the operator logs in at
  `app.staging.compute.unifyai.us`, an agent runs
  `admin account enable-compute 3 --reason …` over SSM, the operator posts
  one listing, and the agent approves and unlists it from the host.
- No job has run with a `template_id` yet, so `runs` on a listing has only
  been seen at 0 outside the tests.
