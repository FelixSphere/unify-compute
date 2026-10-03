---
environment: production
action: release
outcome: success
run: 37095677205
commit: 746b446eda020c631b6536471df976e6deb3ae9d
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:9dfc652abae49a0b622eda994c4152aa16b3a295b830e76daeae2685b03496b8
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:6981df3526a829868b3360d938b3240217f0e9d19790c303505b0cdcbf587f63
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:9dfc652abae49a0b622eda994c4152aa16b3a295b830e76daeae2685b03496b8
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:6981df3526a829868b3360d938b3240217f0e9d19790c303505b0cdcbf587f63
hotfix: false
---

# 2026-10-03 — production compute connected to the Unify API account system

The operator approved this explicitly ("现在开启", turn it on now, in the
unify-compute session) as a cross-product change: ADR-0001's bridge, live in
production. No image changed on either product. Run 37095677205 is a
same-digest production release made **after** the host change, to put
host.sh's checks and the smoke on the new shape. It reported `NOOP`.

## Order (one product at a time)

1. **UnifyAPI console staging** (`i-0469afe3c6b3bec23`, us-east-1). It was
   already at production's version: v0.1.144, the same digest
   `sha256:ffc90e7d…`, `build_id` 27e9887b. It got its own secret
   (us-east-1 `/unifyapi/console/compute_bridge_secret`, distinct from every
   other), then a same-image swap. Users, tokens, logs, holds and the
   quota sum were identical before and after. `/api/status` 200 with the same
   build, `/api/tenant/` 401 `AUTH_UNAUTHORIZED`, `/api/about` carries
   `[Source]`. Unsigned bridge call: 401 `invalid_signature`. Signed, made-up
   key: 401 `invalid_key`. Limiter: 300 unsigned requests gave 138 `429`s.
   Compute's staging console runs a different build (`e3cef17`), so this
   staging run of the exact production image was still needed.
2. **UnifyAPI console production** (`i-0358879cccc5be97a`, ap-southeast-5).
   One secret value went to ap-southeast-5
   `/unifyapi/console/compute_bridge_secret` and to us-east-1
   `/unify-compute-production/bridge_secret`, compared by hash, never printed.
   Backups and a `pg_dump` came first (`/opt/unifyapi/*-pre-compute-bridge-20261003T040734Z*`).
   Then `COMPUTE_BRIDGE_SECRET` in `.env` and the compose file, and a same-image
   `console-swap.sh` (traffic moved to a standby and back, no gap). The
   instance was **not** stopped or started: same LaunchTime, same public IP.
   Same checks as staging, all passed. users 175, tokens 177, logs 110745,
   holds 0 and the quota sum were identical before and after.
3. **Compute production** (`i-07dd2f6d313a0dafd`), under the ops lock. Backup
   `/data/backups/20261003T040909Z-manual-console-bridge/.env`. Then
   `CONSOLE_URL=https://app.unifyapi.ai` and `COMPUTE_BRIDGE_SECRET` (read on
   the host from SSM), and only compute-api was recreated. Boot log:
   `console bridge configured url=https://app.unifyapi.ai`. The committed
   `scripts/console-bridge-host.sh` was then run once more against the live
   host (`MODE=on`): no diff, no recreate.

## What was exercised

- From outside, a made-up key on `GET /compute/v1/jobs`: **401
  `invalid_key`**, decided by the production console. It was `503
  console_unavailable` before.
- From the compute host to `app.unifyapi.ai`, there is no edge or WAF in the
  way: both HMAC headers arrive. 0.22 s to connect, 0.66 s for a cold TLS
  request.
- Console bridge from outside, unsigned: 401 (it was an empty 404 before).
  The production limiter: 200 unsigned requests gave 62 `429`s; `/api/status`
  was unaffected.
- `check-imds.sh` on the compute host after the change: `CHECK_IMDS_OK`.
  Run 37095677205: host checks, `NOOP`, smoke passed.
- Relay: no relay request arrived between the swap and these checks (the last
  was 02:44Z), so the before/after error rate has no "after" yet. The 24 h
  baseline is 1015 billed and 44 errors. An unauthenticated
  `/v1/chat/completions` gets our fork's 401. The image and every other
  variable are unchanged.
- Holds: production console `compute_holds` 0 and `compute_settlements` 0
  after everything. The quota sum over all users is unchanged.

## What this does not prove

- **The real-key path on production has not run.** Nothing in UnifyAPI's
  repo provides an admin path for creating an internal production user.
  Signup needs email verification and Turnstile, and no root credential is
  held anywhere an agent can read. No customer account was touched. Pending:
  the operator creates the user, and then `GET /compute/v1/jobs` → 200 `[]`
  and `POST` `batch-infer-vllm` → `503 upstream_unavailable` with no hold and
  an unchanged balance. See "Operator steps" below.
- That a hold, settle or release works against production's console. The
  path ran on staging against build `e3cef17`, not v0.1.144. Production
  v0.1.144 lacks console #233 (413 / internal_error) and #243 (settle without
  sku is 400). Compute always sends sku, so #243's absence does not bite.

## Rollback

One step per side, compute first. The commands are in `docs/staging.md`,
"Production console bridge".

## Operator steps

1. On https://app.unifyapi.ai as root: create user `compute-e2e-prod`
   (internal, quota 0). Log in as it, create one `sk-` key, and store the key
   as us-east-1 SecureString `/unify-compute-production/test_api_key`.
2. Ask the CI/CD agent to run the real-key checks above, reading the key from
   SSM.
