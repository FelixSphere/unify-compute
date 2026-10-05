# The finetune workload image: the reference run's scripts on top of the same
# vLLM image the batch templates use, so eval.py (vLLM) and train.py (TRL +
# PEFT) share one torch. Published by publish-image.yml (image=finetune) to
# ghcr.io/felixsphere/unify-compute-finetune and pinned by digest in
# compute-api/config/templates.json (finetune-lora). amd64 only: GPU workers
# are x86_64 (g5); nothing on the Graviton hosts runs it.
#
# No model weights, no secrets. Qwen3 is not gated; a gated base model needs
# a secret field the job spec does not have (templates.md, finetune-lora §5).
FROM vllm/vllm-openai:v0.30.0-cu129@sha256:a67f8f186d4567612ac37a55bd82295006002b18858eb12a8af2f05f86c2ae3f

WORKDIR /app
COPY requirements.txt ./
# The base's own packages (torch 2.13, transformers >= 5.10, vLLM) are frozen
# into a constraints file first, so nothing here can pull a second torch or a
# different transformers under vLLM's feet; a pin that cannot coexist fails
# the build here instead of breaking the first GPU job. requirements.lock
# records what the image actually carries.
# fsspec is the one package let out of the constraints: datasets 5.0.1 wants
# fsspec <= 2026.6.0 and the base carries 2026.9.0 (publish run 37260216800
# failed on exactly that); nothing in vLLM pins fsspec, and the import check
# below proves vLLM still loads with the older one.
RUN pip freeze | grep -v -i '^fsspec==' > /tmp/base-constraints.txt \
 && pip install --no-cache-dir -c /tmp/base-constraints.txt -r requirements.txt \
 && pip freeze > /app/requirements.lock \
 && python3 -c "import importlib.metadata as m; print(' '.join(p + '=' + m.version(p) for p in ['torch', 'torchvision', 'transformers', 'vllm', 'trl', 'peft', 'datasets', 'accelerate', 'jsonschema']))" \
 && python3 -c "import datasets, accelerate, jsonschema, peft, torch, vllm; print('imports ok: datasets accelerate jsonschema peft torch vllm')"
# Diagnostic, not a gate: on the GPU-less build runner `import trl` pulls
# transformers' vision stack and died with "operator torchvision::nms does
# not exist" (publish run 37262401309), which is either a torch/torchvision
# pairing in the base or a CPU-only artefact. Print what happens so the
# first GPU job of finetune-lora is not the first time anyone sees it.
RUN python3 -c "import torch, torchvision; print('torch', torch.__version__, 'torchvision', torchvision.__version__); import torchvision.ops; print('torchvision ops ok')" || echo "DIAGNOSTIC: torchvision ops do not load on this CPU-only runner" \
 && (python3 -c "import trl; print('trl import ok', trl.__version__)" || echo "DIAGNOSTIC: trl does not import on this CPU-only runner; verify on the first GPU job")
# The base has python3 but no `python` (publish run 37261402438: "python: not
# found"); the templates' commands say `python`, so give them the alias.
RUN ln -s "$(command -v python3)" /usr/local/bin/python
COPY prepare_data.py train.py eval.py infer_lora.py record.py ./
RUN python -m py_compile prepare_data.py train.py eval.py infer_lora.py record.py

# Templates give the full argv; the vLLM image's own entrypoint must not wrap it.
ENTRYPOINT []
CMD ["python3", "-c", "print('unify-compute finetune image: run a template command')"]
