#!/usr/bin/env python3
"""Batch chat completions with a LoRA adapter, in the OpenAI batch file shape.

Reads one OpenAI batch request per line (url /v1/chat/completions), runs
them through vLLM with the adapter applied, and writes one result line per
request matched by custom_id, the same shape `vllm run-batch` writes. The
batch runner itself takes engine flags but no adapter list, which is why this
exists (templates.md, batch-infer-lora).
"""
import argparse
import json
import sys
import time
import uuid

from vllm import LLM, SamplingParams
from vllm.lora.request import LoRARequest


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", required=True, help="base model the adapter was trained on")
    ap.add_argument("--revision", default="")
    ap.add_argument("--lora", required=True, help="adapter directory (adapter_config.json, adapter_model.safetensors)")
    ap.add_argument("--lora-name", default="lora", help="the model name requests use to select the adapter")
    ap.add_argument("-i", "--input", required=True)
    ap.add_argument("-o", "--output", required=True)
    ap.add_argument("--max-model-len", type=int, default=8192)
    ap.add_argument("--max-lora-rank", type=int, default=64)
    args = ap.parse_args()

    requests = []
    with open(args.input, encoding="utf-8") as f:
        for n, line in enumerate(f, 1):
            line = line.strip()
            if not line:
                continue
            try:
                req = json.loads(line)
                if req.get("url") != "/v1/chat/completions" or "messages" not in req.get("body", {}):
                    raise ValueError("only /v1/chat/completions with messages is supported")
                requests.append(req)
            except (ValueError, TypeError) as e:
                requests.append({"custom_id": f"line-{n}", "_error": str(e)})

    llm = LLM(model=args.model, revision=args.revision or None, enable_lora=True,
              max_lora_rank=args.max_lora_rank, max_model_len=args.max_model_len)
    lora = LoRARequest(args.lora_name, 1, args.lora)
    good = [r for r in requests if "_error" not in r]
    outputs = []
    if good:
        params = []
        for r in good:
            b = r["body"]
            params.append(SamplingParams(temperature=b.get("temperature", 1.0), top_p=b.get("top_p", 1.0),
                                         max_tokens=b.get("max_tokens", 256), seed=b.get("seed")))
        # The adapter is applied to every request: the batch is for one
        # adapter, whatever model name a line carries.
        outputs = llm.chat([r["body"]["messages"] for r in good], params, lora_request=lora, use_tqdm=False)

    by_id = {id(r): o for r, o in zip(good, outputs)}
    created = int(time.time())
    written = 0
    with open(args.output, "w", encoding="utf-8") as out:
        for r in requests:
            rid = f"vllm-{uuid.uuid4().hex[:24]}"
            if "_error" in r:
                out.write(json.dumps({"id": rid, "custom_id": r["custom_id"], "response": None,
                                      "error": {"message": r["_error"], "type": "invalid_request_error", "code": None}}) + "\n")
                continue
            o = by_id[id(r)]
            text = o.outputs[0].text
            usage = {"prompt_tokens": len(o.prompt_token_ids or []), "completion_tokens": len(o.outputs[0].token_ids),
                     "total_tokens": len(o.prompt_token_ids or []) + len(o.outputs[0].token_ids)}
            body = {"id": f"chatcmpl-{uuid.uuid4().hex[:24]}", "object": "chat.completion", "created": created,
                    "model": args.lora_name,
                    "choices": [{"index": 0, "message": {"role": "assistant", "content": text},
                                 "finish_reason": o.outputs[0].finish_reason}],
                    "usage": usage}
            out.write(json.dumps({"id": rid, "custom_id": r.get("custom_id"),
                                  "response": {"status_code": 200, "request_id": rid, "body": body}, "error": None},
                                 ensure_ascii=False) + "\n")
            written += 1
    print(f"wrote {written} results, {len(requests) - written} errors -> {args.output}", file=sys.stderr)


if __name__ == "__main__":
    main()
