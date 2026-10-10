---
environment: staging
action: release
outcome: success
run: 38012888002
commit: 73e43b6c24801ee0ad14860ed7d365e96d175728
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:de58cc43dadcec5339519dbebe3b21733caba72af9d2af33e150a1ee3fd67f94
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:361d43965351ec6bed71cc84794f7c7345c271ee99a52af2bfc22cb021fddfdd
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:bc9befb77b39acb3e5800d4857cce140893ee2600a70685bbc5e08b99b43a374
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:361d43965351ec6bed71cc84794f7c7345c271ee99a52af2bfc22cb021fddfdd
hotfix: false
---

# 2026-10-09 — lora-templates: finetune-lora and batch-infer-lora available

Operator: "加 LoRA 和推理" (2026-10-04), then LGTM on #92 and "发布了吗？".

## What shipped

compute-api `de58cc43…` from main `73e43b6` (publish run 38012166069). Site
unchanged (`361d4396…`). From the previous compute-api build `4d4d986`:

- #82 `finetune/` on main with its Dockerfile and `publish-finetune` job;
  `finetune-lora` takes one `data.tar.gz` and writes `adapter.tar.gz`; new
  `batch-infer-lora` (`infer_lora.py`: OpenAI batch file + adapter through
  vLLM's Python API).
- #84–#91: the finetune image publish (disk, build-once, python alias,
  fsspec, torchvision 0.29.0+cu130, torchaudio removed). Workflow and
  Dockerfile only; nothing in the compute-api binary.
- #92 both templates pin
  `ghcr.io/felixsphere/unify-compute-finetune:finetune-2026-10-04@sha256:6d426175…`
  and are `available`; `finetune-lora` defaults to the on-demand
  `a10g-24gb-x1`.
- No pricing change (`skus.json` untouched). No migration.

## Before / after

- Before: compute-api `bc9befb7…`, site `361d4396…` (usage-views release,
  run 37180194765).
- Backup: `/data/backups/20261010T012237Z-38012888002`, copied to the
  staging backups bucket by `host.sh`.
- Cutover: `RELEASED` compute-api at the digest above; site unchanged; the
  run's `deploy` job is green on the stability check.

## What was exercised (from outside, after the run)

- `https://staging.compute.unifyai.us/healthz` reports build `73e43b6`.
- `GET /compute/v1/templates` lists `batch-embed`, `batch-infer-lora`,
  `batch-infer-vllm`, `finetune-lora`, all available; the two LoRA templates
  share the digest-pinned finetune image.
- `make smoke BASE_URL=https://staging.compute.unifyai.us`: all probes ok.

## What this does not prove

- **Neither template has run on a GPU.** The GPU vCPU quota (L-DB2E81BA) is
  still 0 with the request case open, so a submit ends `capacity_exhausted`
  before any image is pulled.
- **The finetune image is still private on GHCR** at release time (anonymous
  manifest GET → 403). Until the operator sets the package public, a worker
  cannot pull it and a `finetune-lora` or `batch-infer-lora` job would end
  `failed` at image pull even with quota. Recorded here so the first real
  run is not surprised by it.
