#!/usr/bin/env python3
"""Validate, deduplicate, and split a JSONL dataset into train/val/test.

Accepts {"messages": [...]} or {"prompt": ..., "completion": ...} per line.
Writes train.jsonl, val.jsonl, test.jsonl and manifest.json to --out.
"""
import argparse
import hashlib
import json
import random
import shutil
import sys
import tarfile
from pathlib import Path

ROLES = {"system", "user", "assistant"}


def to_messages(row, system):
    if "messages" in row:
        msgs = row["messages"]
    elif "prompt" in row and "completion" in row:
        msgs = [{"role": "user", "content": row["prompt"]},
                {"role": "assistant", "content": row["completion"]}]
        if system:
            msgs.insert(0, {"role": "system", "content": system})
    else:
        raise ValueError("row has neither 'messages' nor 'prompt'+'completion'")
    if not msgs or msgs[-1]["role"] != "assistant":
        raise ValueError("last message must be from the assistant")
    for m in msgs:
        if m.get("role") not in ROLES or not str(m.get("content", "")).strip():
            raise ValueError(f"bad message: {m!r}")
    return msgs


def prompt_key(msgs):
    # Everything before the final assistant turn identifies the example.
    return hashlib.sha256(json.dumps(msgs[:-1], sort_keys=True, ensure_ascii=False).encode()).hexdigest()


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--in", dest="inp", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--val", type=int, required=True, help="examples in the validation split")
    ap.add_argument("--test", type=int, required=True, help="examples in the held-out test split")
    ap.add_argument("--system", default="", help="system prompt to prepend to prompt/completion rows")
    ap.add_argument("--seed", type=int, default=20260921)
    ap.add_argument("--schema", default=None, help="JSON schema file to ship alongside the splits as schema.json")
    ap.add_argument("--archive", default=None,
                    help="also write this .tar.gz of the splits (and schema.json): the one file the finetune-lora template takes as its /mnt/in input")
    args = ap.parse_args()

    rows, bad, seen, dupes = [], 0, set(), 0
    with open(args.inp, encoding="utf-8") as f:
        for n, line in enumerate(f, 1):
            line = line.strip()
            if not line:
                continue
            try:
                raw = json.loads(line)
                msgs = to_messages(raw, args.system)
            except (ValueError, KeyError, TypeError) as e:
                bad += 1
                print(f"line {n}: {e}", file=sys.stderr)
                continue
            k = prompt_key(msgs)
            if k in seen:
                dupes += 1
                continue
            seen.add(k)
            out = {"messages": msgs}
            if raw.get("reject"):
                out["reject"] = True
            rows.append(out)

    if len(rows) <= args.val + args.test:
        sys.exit(f"only {len(rows)} usable examples; need more than val+test={args.val + args.test}")

    random.Random(args.seed).shuffle(rows)
    test, val, train = rows[:args.test], rows[args.test:args.test + args.val], rows[args.test + args.val:]

    outdir = Path(args.out)
    outdir.mkdir(parents=True, exist_ok=True)
    splits = {"train": train, "val": val, "test": test}
    for name, split in splits.items():
        with open(outdir / f"{name}.jsonl", "w", encoding="utf-8") as f:
            for r in split:
                f.write(json.dumps(r, ensure_ascii=False) + "\n")

    manifest = {
        "source": args.inp,
        "seed": args.seed,
        "rejected_lines": bad,
        "duplicates_dropped": dupes,
        "splits": {name: {"n": len(s), "reject": sum(1 for r in s if r.get("reject"))} for name, s in splits.items()},
        "test_sha256": sha256_file(outdir / "test.jsonl"),
    }
    (outdir / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    if args.schema:
        shutil.copyfile(args.schema, outdir / "schema.json")
    if args.archive:
        with tarfile.open(args.archive, "w:gz") as tar:
            for name in ["train.jsonl", "val.jsonl", "test.jsonl", "manifest.json"] + (["schema.json"] if args.schema else []):
                tar.add(outdir / name, arcname=name)
        manifest["archive"] = args.archive
    print(json.dumps(manifest, indent=2))


if __name__ == "__main__":
    main()
