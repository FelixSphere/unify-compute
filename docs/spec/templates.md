# Job templates

- **Status:** Spec, partially implemented. The catalogue file, boot
  validation, `GET /compute/v1/templates` and template resolution on
  `POST /compute/v1/jobs` exist in `compute-api/internal/template/`, and a
  resolved template is submitted and launched like any job wherever the
  binary has a job store (`internal/jobs`). It has run end to end only
  against the fake SkyPilot API server in compute-api's tests; no template
  has run on a real cloud yet.
- Task C7 in [`product-design.md`](../product-design.md). Field names are
  those of the job spec in [`job-api.md`](job-api.md).

A template is a named, reviewed job spec: image, command, env defaults,
default SKU, default runtime, the mounts the command reads and writes, and
which fields a submit may change. It is how a customer runs a batch or a
fine-tune without writing a spec, the way Vast.ai templates package an image
and launch settings under a name.

## The file

`compute-api/config/templates.json` ships inside the image next to
`skus.json` (path overridable with `TEMPLATE_FILE`). Like the SKU catalogue it
is code: a change to a template's image, command or default SKU changes what
customers run under a name they trust, and is reviewed like a price change.

| Field | Meaning |
|---|---|
| `id` | Lowercase slug; what a submit names |
| `title`, `description` | Shown to customers; required |
| `image` | Container image, **pinned by digest** (below). Empty only on an unavailable placeholder |
| `image_licence`, `image_source` | Licence of the image and where it was checked; recorded for every available template |
| `command` | Full argv run inside the image (the image's entrypoint is not used) |
| `env` | Defaults; keys are shell identifiers. Never secrets |
| `sku` | Default SKU; must exist in `skus.json` |
| `max_runtime_seconds` | Default; in `(0, ceiling]`, the deployment's ceiling from `config/platform.json` (14400 today; never above 86400) |
| `inputs`, `outputs` | Declared mounts: absolute, clean, non-overlapping paths with a description of what the command expects there or writes there |
| `overridable` | Which request fields a submit may set (below) |
| `available`, `unavailable_reason` | Declared availability; `false` requires a reason |

### Validated at boot

The process refuses to start, as it does for a bad `skus.json`, when a
template has: an unknown field (a typo in `overridable` must not mean "no
overrides"), a non-slug or duplicate id, no title or description, an image
that is not digest-pinned or is tagged `latest`, a default SKU not in the
catalogue, a runtime outside `(0, ceiling]`, a bad env key, a relative, `/`,
unclean or overlapping mount, an `overridable` entry outside the allowed set or
listed twice, `available: true` without an image or command, or
`available: false` without a reason.

### Availability

A template is listed as available only if it is declared available **and**
its default SKU is `available` in the catalogue. A template whose default SKU
stops being sold does not stop the boot; it lists as unavailable with
`unavailable_reason` naming the SKU, and a submit naming it is refused even if
the submit overrides `sku`. Whether this deployment has an upstream enabled for
the SKU is not part of template availability; that is decided at submit with
the same `503 upstream_unavailable` as any job.

### Why digests

A tag is a pointer the publisher can move; Docker Hub lets `v0.30.0` be
re-pushed, and `latest` moves on every release. A template is reviewed so that
what runs is what was read, and a job record should say exactly what ran. So
the digest (`@sha256:…`, the manifest-list digest for multi-arch images) is
mandatory and is what gets pulled; the tag stays beside it because a human
reviewing `v0.30.0-cu129` learns more than from 64 hex characters. A `latest`
tag is refused even with a digest because it then tells the reader something
false. Bumping an image is a diff to the digest, re-checked against the
registry.

## Overrides

`overridable` may only contain:

| Field | Effect |
|---|---|
| `sku` | Replaces the default SKU |
| `providers` | Restricts placement, as on any submit |
| `spec.command` | Replaces the command (no curated template allows it) |
| `spec.env` | Merged key by key over the defaults; a default cannot be removed, only set to `""` |
| `spec.max_runtime_seconds` | Replaces the default runtime |

`spec.image`, `spec.inputs` and `spec.outputs` can never be overridable. A
different image is a different template; the declared mounts are the contract
between the template and its command. A submit does not override inputs, it
**binds** them: it must give a `source` for every declared input mount and may
not name an undeclared one.

## API

### `GET /compute/v1/templates`

Public, like `/skus`. Every template with `object: "compute.template"`,
including unavailable ones and their reasons, sorted by id.

### `template` on `POST /compute/v1/jobs`

```json
{
  "template": "batch-embed",
  "sku": "a10g-24gb-x1",
  "spec": {
    "inputs": [{ "source": "https://acme-data.s3.amazonaws.com/requests.jsonl?X-Amz-Signature=…", "mount": "/mnt/in" }],
    "env": { "MODEL": "BAAI/bge-m3", "MODEL_REVISION": "" },
    "max_runtime_seconds": 1800
  }
}
```

Validated in this order, after the body parses and before anything
`job-api.md` lists:

1. `template` names a template (`400 unknown_template`) that is available
   (`400 template_unavailable`, message carries the reason).
2. Every field sent is in the template's `overridable`
   (`400 field_not_overridable`, `param` is the field, e.g. `spec.image`).
   Sending a field counts as overriding it, even with the template's own value.
3. Env keys are shell identifiers (`400 invalid_env`).
4. Inputs: every declared mount has exactly one non-empty source, and no
   undeclared mount is named (`400 missing_input`, `unknown_input_mount`,
   `duplicate_input_mount`; `param` is `spec.inputs`).
5. The resolved spec then meets **exactly** the checks a hand-written spec does
   (`missing_sku`, `unknown_sku`, `sku_unavailable`, `missing_max_runtime`,
   `runtime_over_ceiling`, `provider_not_offered`, `503 upstream_unavailable`).
   An overridden runtime above the ceiling (14401 today) fails as `runtime_over_ceiling`, not as a
   template error.

What survives is a full spec (`image`, `command`, `env`, `inputs`, `outputs`,
`max_runtime_seconds`) plus `sku` and `providers`, submitted exactly as a
hand-written spec would be (`job-api.md`), including its launchability
checks: every input `source` must be an `https://` URL, and because the run
step saves it under the URL's last path segment, the batch templates need a
URL whose path ends in `requests.jsonl`. The Job records the resolved spec,
so a later change to the template does not change what an old job says it
ran. *Planned:* the Job also records the template id; it does not yet.

## Curated templates

| id | Image | Default SKU | Available |
|---|---|---|---|
| `batch-embed` | `vllm/vllm-openai:v0.30.0-cu129@sha256:a67f8f18…` | `a10g-24gb-x1` | yes (not yet run on a real cloud) |
| `batch-infer-vllm` | same | `a10g-24gb-x1` | yes (same caveat) |
| `batch-infer-lora` | `ghcr.io/felixsphere/unify-compute-finetune:finetune-2026-10-04@sha256:6d426175…` | `a10g-24gb-x1` | yes (not yet run on a real cloud) |
| `finetune-lora` | same | `a10g-24gb-x1` | yes (same caveat) |

### Image provenance

`vllm/vllm-openai` is the vLLM project's own image on Docker Hub, Apache-2.0
(`LICENSE` at tag `v0.30.0` of github.com/vllm-project/vllm). The pinned digest
is the multi-arch manifest list for tag `v0.30.0-cu129`, pushed 2026-09-22,
read from `registry-1.docker.io` on 2026-10-01; it lists `linux/amd64` and
`linux/arm64`. The `-cu129` variant is chosen over the default `v0.30.0`
(CUDA 13.0, which needs a host driver of 580 or newer) because CUDA 12.9 runs
on the older drivers GPU VM images still ship. Which driver SkyPilot's AWS
image has is not verified. Both batch templates use `vllm run-batch`
(`vllm/entrypoints/cli/run_batch.py` at v0.30.0), which reads an OpenAI batch
file and supports `/v1/chat/completions` and `/v1/embeddings`.

The images do not contain model weights. Defaults are downloaded from Hugging
Face at start, pinned by commit with `MODEL_REVISION`:
`Qwen/Qwen3-4B-Instruct-2507` at `cdbee75f…` and `Qwen/Qwen3-Embedding-0.6B` at
`97b0c614…`, both Apache-2.0 and not gated. A customer overriding `MODEL`
should set `MODEL_REVISION` to that model's commit or to `""` (the command
omits `--revision` when it is empty); leaving the old revision fails at
download.

### The finetune image

`finetune/Dockerfile` builds `ghcr.io/felixsphere/unify-compute-finetune`:
the vLLM image above as base (so eval and training share one torch), the
exact pins in `finetune/requirements.txt` (TRL, PEFT, datasets, accelerate,
jsonschema; all Apache-2.0 or MIT, recorded in `THIRD-PARTY-LICENSES.md`),
and the scripts `prepare_data.py`, `train.py`, `eval.py`, `infer_lora.py`,
`record.py` at `/app`. `publish-image.yml` (`image=finetune`, or a
`finetune-v*` tag) builds it for `linux/amd64` only, frees the runner's disk
first (the base unpacks to more than the 72 GB runner has free), constrains
pip to the base's packages, and gates the push on importing `torchvision.ops`,
`trl`, `peft`, `datasets`, `accelerate`, `jsonschema` and vLLM's `LLM`. GPU
workers are x86 (g5); the Graviton hosts never run it, and the `pinned-images`
CI job does not scan `templates.json`.

The base's own torch family is not consistent: torch 2.14.0 (CUDA 13.0) ships
beside torchvision 0.28.0+cu129 and torchaudio 2.11.0+cu129, which vLLM never
loads but transformers does the moment a training import touches vision or
audio. The image therefore reinstalls torchvision as 0.29.0+cu130 and removes
torchaudio (no matching build exists; nothing here does audio). The first
publish took nine runs to get there (37257555345 … 37266527011); the
Dockerfile's comments carry the reasons.

Published 2026-10-05 as digest `6d426175…` (run 37266527011, from main
`150de552`) and pinned in both LoRA templates, and since 2026-10-16 in
`batch-infer-vllm` and `batch-embed` as well: the bare base cannot start
`vllm` at all (the first real staging job, 2026-10-16, crashed in
`torchvision/_meta_registrations.py` with `operator torchvision::nms does not
exist` before reading an argument), and this image is the same base with
torchvision fixed and vLLM's import gated at build. The GHCR package must be **public**
for workers to pull it without credentials; that is a package setting the
operator changes once.

### `finetune-lora`

The template form of the reference fine-tune in `finetune/` (the
`sky/finetune.yaml` run block). Its one input is `data.tar.gz` from
`prepare_data.py --archive` (`train.jsonl`, `val.jsonl`, `test.jsonl`,
optionally `schema.json`, which is used when `SCHEMA` is empty), because a
template binds one file per input mount; the command unpacks it beside
itself, so the reference run's paths under `/mnt/in` are unchanged. It also
writes `adapter.tar.gz` next to `adapter/`, the hand-off to
`batch-infer-lora`. It keeps that task's steps in order:
zero-shot and 3-shot baselines with `eval.py`, the few-shot gate that writes
`RESULT = task_not_worth_finetuning` and stops, `train.py` writing the adapter,
and an eval with the adapter. Paths move from `~/in` and `~/out` to the
declared mounts `/mnt/in` and `/mnt/out`; `BASE_MODEL`, `FEW_SHOT_GATE` and
`SCHEMA` keep their defaults; the default SKU is the spot A10G the reference
run uses, and the default runtime is `launch.sh`'s 7200 s. A test pins these
steps so the two cannot drift silently.

It is available since the image was pinned (above); it has not yet run on a
real cloud. Of what was missing when the placeholder was written:

1. **An image.** Built, published and pinned (`finetune/Dockerfile`,
   `publish-finetune`).
2. **Input and output transfer.** Done by the backend: https inputs are
   fetched into the mount by the run step, outputs are uploaded by the
   artifacts step after the command, whatever its exit status (ADR-0005).
   The one-file-per-mount rule is why the input is an archive.
3. **Runtime enforcement.** The reference run wraps only `train.py` in
   `timeout $MAX_RUNTIME_SECONDS`, leaving time for the last eval and sync.
   SkyPilot has no task-level timeout, so the template drops `timeout` and
   relies on the backend enforcing `max_runtime_seconds` for the whole job,
   which it does (`backend.md`, "Max runtime"); a job killed at the limit
   loses its final eval and its output upload, which the reference run did
   not.
4. **Disk.** The reference task asks for `disk_size: 100`. Neither the job spec
   nor the SKU has a disk field.
5. **Secrets.** `HF_TOKEN` is left out on purpose (Qwen3 is not gated). The job
   spec has no secret field; a gated base model cannot be fine-tuned through
   this template until it has one.
6. **Instrumentation.** The reference run writes `t_instance_up` and
   `t_process_exit` itself to measure cold start. The template keeps only
   `train.py --timestamps`; the other two are the backend's to record.

### `batch-infer-lora`

`batch-infer-vllm` with an adapter. `vllm run-batch` takes the engine flags
(`--enable-lora`, `--max-lora-rank`) but no adapter list, so the finetune
image's `infer_lora.py` does the same job through vLLM's Python API: it reads
the OpenAI batch file, runs every `/v1/chat/completions` line with the one
adapter applied (`LoRARequest`), and writes result lines in the batch
runner's shape, `model` set to `LORA_NAME`. Inputs: `/mnt/in/requests.jsonl`
and `/mnt/adapter/adapter.tar.gz` (a PEFT adapter directory, as `finetune-lora`
writes it; `adapter_config.json` is located wherever the archive put it). The
base model and revision default to `batch-infer-vllm`'s, which are
`finetune-lora`'s `BASE_MODEL`; an adapter trained on another base needs
`MODEL`/`MODEL_REVISION` overridden to that base. `TestBatchInferLoraPairsWithFinetuneLora`
pins the pairing.

## Open questions

- Default SKU for `finetune-lora`: the reference task runs spot
  (`use_spot: true`), but the catalogue does not sell `a10g-24gb-x1-spot`, and
  a template whose default SKU is unsold is itself unavailable. The default is
  therefore the on-demand `a10g-24gb-x1` (what `finetune/README.md`'s recipe
  table names); customers override `sku` to the spot SKU the day it is sold.
- Whether SkyPilot pulls a digest reference through `image_id: docker:<ref>`
  unchanged has not been tried.
- Whether a customer may define their own templates (per account, not
  reviewed) is not decided. This file is curated templates only.
