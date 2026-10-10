---
environment: production
action: release
outcome: success
run: 38013108239
commit: 73e43b6c24801ee0ad14860ed7d365e96d175728
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:de58cc43dadcec5339519dbebe3b21733caba72af9d2af33e150a1ee3fd67f94
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:361d43965351ec6bed71cc84794f7c7345c271ee99a52af2bfc22cb021fddfdd
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:bc9befb77b39acb3e5800d4857cce140893ee2600a70685bbc5e08b99b43a374
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:361d43965351ec6bed71cc84794f7c7345c271ee99a52af2bfc22cb021fddfdd
hotfix: false
---

# 2026-10-09 — lora-templates on production: the same digests staging ran

The digests of [`2026-10-09-staging-lora-templates.md`](2026-10-09-staging-lora-templates.md),
released after staging's run 38012888002 succeeded and its checks passed.
What shipped is listed there: `finetune-lora` and `batch-infer-lora`
available on the published finetune image (#82, #84–#92). Site unchanged.
No pricing change, no migration.

## Before / after

- Before: compute-api `bc9befb7…`, site `361d4396…` (usage-views release,
  run 37180336504).
- Backup: `/data/backups/20261010T012603Z-38013108239`, copied to the
  production backups bucket by `host.sh`.
- Cutover: `RELEASED` compute-api at the digest above; the run's `deploy`
  job is green on the stability check.

## What was exercised (from outside, after the run)

- `https://api.compute.unifyai.us/healthz` reports build `73e43b6`.
- `GET /compute/v1/templates` lists all four templates available, the two
  LoRA ones on the digest-pinned finetune image.
- `make smoke BASE_URL=https://api.compute.unifyai.us`: 16 ok, no FAIL.

## What this does not prove

- As on staging: no template has run on a GPU (quota 0, case open), and the
  finetune image is still private on GHCR until the operator sets the
  package public. Production GPUs are off (`gpu_workers_enabled = false`),
  so nothing here can launch anyway.
