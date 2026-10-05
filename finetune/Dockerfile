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
 && python3 -c "import trl, peft, datasets, accelerate, jsonschema, vllm, transformers, torch; print('trl', trl.__version__, 'peft', peft.__version__, 'transformers', transformers.__version__, 'torch', torch.__version__, 'vllm', vllm.__version__)"
# The base has python3 but no `python` (publish run 37261402438: "python: not
# found"); the templates' commands say `python`, so give them the alias.
RUN ln -s "$(command -v python3)" /usr/local/bin/python
COPY prepare_data.py train.py eval.py infer_lora.py record.py ./
RUN python -m py_compile prepare_data.py train.py eval.py infer_lora.py record.py

# Templates give the full argv; the vLLM image's own entrypoint must not wrap it.
ENTRYPOINT []
CMD ["python3", "-c", "print('unify-compute finetune image: run a template command')"]
