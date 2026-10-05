#!/usr/bin/env python3
"""Evaluate base model (zero/few-shot) or base + LoRA adapter on the test split.

Greedy decoding with vLLM. Reports JSON parseability, schema validity, exact
match, per-field accuracy, and correct-rejection rate.
"""
import argparse
import json
import random
from pathlib import Path

from transformers import AutoTokenizer
from vllm import LLM, SamplingParams
from vllm.lora.request import LoRARequest


def load_jsonl(path):
    with open(path, encoding="utf-8") as f:
        return [json.loads(l) for l in f if l.strip()]


def parse_json(text):
    text = text.strip()
    if text.startswith("```"):
        text = text.strip("`")
        if text.startswith("json"):
            text = text[4:]
    try:
        return json.loads(text)
    except ValueError:
        return None


def build_prompt(tok, example, shots):
    msgs = [m for m in example["messages"][:-1]]
    if shots:
        # Few-shot examples go after the system prompt as prior turns.
        system = [m for m in msgs if m["role"] == "system"]
        rest = [m for m in msgs if m["role"] != "system"]
        demo = []
        for s in shots:
            demo.extend(m for m in s["messages"] if m["role"] != "system")
        msgs = system + demo + rest
    return tok.apply_chat_template(msgs, tokenize=False, add_generation_prompt=True, enable_thinking=False)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", default="Qwen/Qwen3-4B-Instruct-2507")
    ap.add_argument("--lora", default=None, help="adapter directory")
    ap.add_argument("--test", required=True)
    ap.add_argument("--train", default=None, help="needed with --few-shot")
    ap.add_argument("--few-shot", type=int, default=0)
    ap.add_argument("--schema", default=None, help="JSON schema the target must satisfy")
    ap.add_argument("--max-tokens", type=int, default=512)
    ap.add_argument("--out", required=True)
    ap.add_argument("--seed", type=int, default=20260921)
    args = ap.parse_args()

    test = load_jsonl(args.test)
    shots = []
    if args.few_shot:
        if not args.train:
            ap.error("--few-shot needs --train")
        shots = random.Random(args.seed).sample(load_jsonl(args.train), args.few_shot)

    validator = None
    if args.schema:
        import jsonschema
        validator = jsonschema.Draft202012Validator(json.loads(Path(args.schema).read_text()))

    tok = AutoTokenizer.from_pretrained(args.model)
    llm = LLM(model=args.model, enable_lora=bool(args.lora), max_lora_rank=64, dtype="bfloat16",
              max_model_len=4096, gpu_memory_utilization=0.85)
    prompts = [build_prompt(tok, ex, shots) for ex in test]
    sp = SamplingParams(temperature=0, max_tokens=args.max_tokens, seed=args.seed)
    lora_req = LoRARequest("adapter", 1, args.lora) if args.lora else None
    outputs = llm.generate(prompts, sp, lora_request=lora_req)

    n = len(test)
    parseable = schema_ok = exact = 0
    field_hits, field_total = {}, {}
    reject_n = reject_ok = 0
    rows = []
    for ex, out in zip(test, outputs):
        text = out.outputs[0].text
        target_text = ex["messages"][-1]["content"]
        got, want = parse_json(text), parse_json(target_text)
        is_reject = bool(ex.get("reject")) or (isinstance(want, dict) and "error" in want)
        row = {"output": text, "target": target_text, "parseable": got is not None}
        if got is not None:
            parseable += 1
            if validator is not None and validator.is_valid(got):
                schema_ok += 1
        if want is not None and got is not None:
            row["exact"] = got == want
        else:
            row["exact"] = text.strip() == target_text.strip()
        exact += row["exact"]
        if isinstance(want, dict) and not is_reject:
            for k, v in want.items():
                field_total[k] = field_total.get(k, 0) + 1
                if isinstance(got, dict) and got.get(k) == v:
                    field_hits[k] = field_hits.get(k, 0) + 1
        if is_reject:
            reject_n += 1
            if isinstance(got, dict) and "error" in got:
                reject_ok += 1
        rows.append(row)

    summary = {
        "model": args.model,
        "lora": args.lora,
        "few_shot": args.few_shot,
        "n": n,
        "json_parseable_rate": parseable / n,
        "schema_valid_rate": (schema_ok / n) if validator is not None else None,
        "exact_match_rate": exact / n,
        "field_accuracy": {k: field_hits.get(k, 0) / t for k, t in sorted(field_total.items())},
        "reject_n": reject_n,
        "correct_rejection_rate": (reject_ok / reject_n) if reject_n else None,
    }
    Path(args.out).write_text(json.dumps({"summary": summary, "rows": rows}, indent=2, ensure_ascii=False) + "\n")
    print(json.dumps(summary, indent=2))


if __name__ == "__main__":
    main()
