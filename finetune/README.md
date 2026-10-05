# Fine-tuning reference run

> **Two ways to run this.** By hand with SkyPilot, as described below (the
> original reference run), or as the job templates `finetune-lora` and
> `batch-infer-lora` once the image in `Dockerfile` is published and pinned
> (`docs/spec/templates.md`). The template takes `prepare_data.py --archive`'s
> `data.tar.gz` as its one input and returns `adapter.tar.gz`, which
> `batch-infer-lora` takes as its adapter input.

The first real workload on the compute product, run by us as the first
customer. It does not go through `compute-api` (job submission is still `501`);
it drives SkyPilot directly and records what the backend will later have to
reproduce automatically. One run produces three things:

1. a LoRA adapter, served on vLLM at the end;
2. a SkyPilot task ([`sky/finetune.yaml`](sky/finetune.yaml)) that is the shape
   `compute-api` must one day generate from a job spec;
3. a job record with four timestamps and the hold/settle arithmetic, the first
   real numbers behind `docs/architecture.md` open questions 1 and 2.

Everything in this directory is a workload, not part of `compute-api/`. The
licence rules for `compute-api/` (permissive only, recorded in
`THIRD-PARTY-LICENSES.md`) do not apply here, but the model licence does:
Qwen3-4B-Instruct is Apache-2.0.

## Data contract

`prepare_data.py` takes one JSONL file, one example per line, in either form:

```json
{"messages": [{"role": "system", "content": "..."}, {"role": "user", "content": "..."}, {"role": "assistant", "content": "..."}]}
{"prompt": "...", "completion": "..."}
```

Optional fields: `"reject": true` marks an example whose correct answer is a
refusal; the eval reports accuracy on that subset separately. Any other field
is dropped. The script validates roles, deduplicates on the prompt side, splits
with a fixed seed, and writes a manifest with the SHA-256 of `test.jsonl`.
The test split is the private eval set: it is never looked at, never used for
prompt engineering, and its hash is what proves that.

```bash
python prepare_data.py --in raw.jsonl --out data/ --val 200 --test 500
```

## Recipe

| | |
|---|---|
| Model | `Qwen/Qwen3-4B-Instruct-2507` |
| Method | LoRA SFT, r=16, alpha=32, all attention and MLP projections |
| Stack | TRL `SFTTrainer` + PEFT ([`train.py`](train.py)) |
| Hardware | 1× A10G 24 GB, spot. SKU `a10g-24gb-x1` in `compute-api/config/skus.json` |
| Hyperparameters | bf16, lr 1e-4 cosine, 3 epochs, seq 2048, batch 4 × grad accum 4 |
| Budget | training ~1.5 h + eval ~0.5 h at the catalogue price ≈ USD 3; cap USD 20 including retries |

Stop condition: if loss has not fallen normally within two hours, switch to
LLaMA-Factory instead of debugging the training loop.

## Running it

Prerequisites on the launching machine: AWS credentials for the UnifyAI
account with quota for one `g5.xlarge` spot instance in `us-east-1`, an S3
bucket for artifacts (see [ADR-0005](../docs/adr/0005-artifact-storage.md)),
and `pip install skypilot[aws]` at the pin in ADR-0003.

```bash
cd finetune
python prepare_data.py --in raw.jsonl --out data/ --val 200 --test 500
./launch.sh <run-id> s3://<artifact-bucket>
```

`launch.sh` records `t_request`, uploads `data/`, launches the task with
`--down` so the instance terminates when the run exits, pulls the outputs back
to `out/<run-id>/`, and runs `record.py`.

The task itself, in order:

1. `t_instance_up` is written as the first line of `setup`.
2. Zero-shot and 3-shot baselines on the test split with the base model.
3. Gate: if the few-shot baseline already reaches 95% schema validity, the run
   stops with `task_not_worth_finetuning`. That is a result, not a failure.
4. Training. `train.py` writes `t_train_start` once weights are loaded.
5. Eval with the adapter. All three result files land next to each other.
6. `t_process_exit` and a final sync to S3 happen in an `EXIT` trap, so an
   interrupted run still leaves its timestamps behind.

## Metrics

`eval.py` reports, for each of zero-shot / few-shot / LoRA:

- JSON parseable rate
- schema valid rate (when `--schema` is given)
- exact match rate (normalised JSON equality when both sides parse)
- per-field accuracy for top-level keys
- correct rejection rate on the `reject` subset (target and output both carry
  a top-level `error` key)

Acceptance: LoRA schema validity ≥ 98% **and** ≥ 10 points above few-shot;
a job record with all four timestamps and the cost ratios; the adapter
answering on vLLM. If the first condition fails, the task was wrong for
fine-tuning, not fine-tuning wrong for the task: change the task and rerun
steps 1–2, do not tune hyperparameters.

## Job record

`record.py` merges the local `t_request` with the timestamps from the instance
and computes:

| Interval | Meaning |
|---|---|
| `t_request → t_instance_up` | queue + provisioning |
| `t_instance_up → t_train_start` | cold start: pip, data pull, weight load |
| `t_train_start → t_process_exit` | real compute |

and two ratios:

- **hold / settle**: `EstimateQuota(max_runtime_seconds)` against the SKU
  price × `t_instance_up → t_process_exit`. This is how much customer balance
  a full-runtime hold would freeze for nothing, and it decides whether the
  spec's "extend the hold every 5 minutes" design is needed.
- **catalogue / bill**: the SKU price against the actual spot bill for the
  instance (`--actual-bill-usd`, from Cost Explorer a day later). This is
  gross margin. Nobody has measured it yet.

`record.py` reimplements `EstimateQuota` from `compute-api/internal/sku/sku.go`
(`QuotaPerUSD = 500000`, round half up). If the two ever disagree, the Go code
is right and this script is wrong.

## Spec mapping

What `sky/finetune.yaml` does by hand and where `docs/spec/job-api.md` has a
field for it:

| Job spec | SkyPilot task | Gap |
|---|---|---|
| `sku` | `resources.accelerators`, `resources.cloud/region` | none; a SKU is a fixed resources block |
| `image` | `resources.image_id: docker:…` | this run uses the default VM image + pip, not a container; the backend must use `image_id` |
| `command` | `run` | none |
| `env` | `envs` | secrets (`HF_TOKEN`) must not be in the job object; the spec has no secret field |
| `inputs[].source → mount` | `aws s3 sync` in `setup` | SkyPilot `file_mounts` can do this natively; needs cross-account bucket access for customer buckets |
| `outputs[].mount` | `aws s3 sync` in the `EXIT` trap | same; where outputs live is ADR-0005 |
| `max_runtime_seconds` | `timeout` around the command | **SkyPilot has no task-level timeout.** The backend has to enforce it itself, and the `EXIT` trap shows why the outputs must still sync after a kill |
| `status: interrupted` | spot preemption | this run has no retry; the backend's default is architecture.md's open question |

Add to this table anything else that did not fit while running.

## Serving

```bash
sky launch -c serve-lora sky/serve-lora.yaml --env ADAPTER_URI=s3://<bucket>/finetune/<run-id>/out/adapter -y
curl http://<ip>:8000/v1/chat/completions -H 'content-type: application/json' \
  -d '{"model":"job-spec","messages":[{"role":"user","content":"…"}]}'
```

`model: job-spec` selects the adapter; the base model name selects the base.
The last step is registering that endpoint as an OpenAI-compatible channel in
the staging console so a request through `api.unifyapi.ai/v1` reaches it. That
is the "fine-tune, then serve on the same key" path that distinguishes this
product from renting a GPU.
