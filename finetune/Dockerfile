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
RUN pip install --no-cache-dir -r requirements.txt \
 && python -c "import trl, peft, datasets, accelerate, jsonschema, vllm, transformers; print('trl', trl.__version__, 'peft', peft.__version__, 'vllm', vllm.__version__)"
COPY prepare_data.py train.py eval.py infer_lora.py record.py ./
RUN python -m py_compile prepare_data.py train.py eval.py infer_lora.py record.py

# Templates give the full argv; the vLLM image's own entrypoint must not wrap it.
ENTRYPOINT []
CMD ["python", "-c", "print('unify-compute finetune image: run a template command')"]
