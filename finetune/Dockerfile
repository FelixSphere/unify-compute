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
# failed on exactly that); nothing in vLLM pins fsspec.
#
# torchvision is reinstalled to match torch. The base carries torch 2.14.0
# (CUDA 13.0) with torchvision 0.28.0+cu129 (publish run 37263815010), a pair
# that does not load ("operator torchvision::nms does not exist"), which
# breaks every transformers import that touches vision and so `import trl`.
# torch 2.14 pairs with torchvision 0.29 and exists only as a CUDA 13 build,
# which is what the base's nvidia/cu13 libraries are for.
#
# torchaudio is removed. The base's 2.11.0+cu129 is compiled against CUDA
# 12.9 and refuses to load next to the CUDA 13.0 torch (publish run
# 37265125567: "PyTorch and TorchAudio were compiled with different CUDA
# versions"), transformers imports it whenever it is installed, and no
# matching build exists (torchaudio 2.11 is the newest). Nothing here does
# audio; vLLM touches it only for audio models, which these templates never
# load. The gate below has to construct-import vLLM's LLM without it.
RUN pip freeze | grep -v -i -E '^(fsspec|torchvision)==' > /tmp/base-constraints.txt \
 && pip install --no-cache-dir -c /tmp/base-constraints.txt -r requirements.txt \
 && pip install --no-cache-dir --no-deps --index-url https://download.pytorch.org/whl/cu130 "torchvision==0.29.0" \
 && pip uninstall -y torchaudio \
 && pip freeze > /app/requirements.lock \
 && echo "torch-family and CUDA packages:" && grep -i -E '^(torch|nvidia-|triton|flashinfer|xformers)' /app/requirements.lock | tr '\n' ' ' && echo \
 && python3 -c "import importlib.metadata as m, torch; print(' '.join(p + '=' + m.version(p) for p in ['torch', 'torchvision', 'transformers', 'vllm', 'trl', 'peft', 'datasets', 'accelerate', 'jsonschema']), 'torch.cuda', torch.version.cuda)" \
 && python3 -c "import torchvision.ops, trl, peft, datasets, accelerate, jsonschema, vllm; from vllm import LLM; print('imports ok: torchvision.ops trl peft datasets accelerate jsonschema vllm')"
# The base has python3 but no `python` (publish run 37261402438: "python: not
# found"); the templates' commands say `python`, so give them the alias.
RUN ln -s "$(command -v python3)" /usr/local/bin/python
COPY prepare_data.py train.py eval.py infer_lora.py record.py ./
RUN python -m py_compile prepare_data.py train.py eval.py infer_lora.py record.py

# Templates give the full argv; the vLLM image's own entrypoint must not wrap it.
ENTRYPOINT []
CMD ["python3", "-c", "print('unify-compute finetune image: run a template command')"]
