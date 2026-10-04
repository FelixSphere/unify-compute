#!/usr/bin/env python3
"""LoRA SFT with TRL + PEFT. Writes the adapter and t_train_start."""
import argparse
import json
import time
from pathlib import Path

import torch
from datasets import load_dataset
from peft import LoraConfig
from transformers import AutoModelForCausalLM, AutoTokenizer
from trl import SFTConfig, SFTTrainer


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", default="Qwen/Qwen3-4B-Instruct-2507")
    ap.add_argument("--train", required=True)
    ap.add_argument("--val", required=True)
    ap.add_argument("--out", required=True, help="adapter output directory")
    ap.add_argument("--timestamps", default=None, help="timestamps.json to append t_train_start to")
    ap.add_argument("--epochs", type=float, default=3)
    ap.add_argument("--lr", type=float, default=1e-4)
    ap.add_argument("--seq", type=int, default=2048)
    ap.add_argument("--batch", type=int, default=4)
    ap.add_argument("--grad-accum", type=int, default=4)
    ap.add_argument("--lora-r", type=int, default=16)
    ap.add_argument("--lora-alpha", type=int, default=32)
    args = ap.parse_args()

    data = load_dataset("json", data_files={"train": args.train, "val": args.val})
    tok = AutoTokenizer.from_pretrained(args.model)
    model = AutoModelForCausalLM.from_pretrained(
        args.model, torch_dtype=torch.bfloat16, attn_implementation="sdpa", device_map="auto"
    )

    # Weights are on the GPU from here: cold start ends, real compute starts.
    t_train_start = int(time.time())
    if args.timestamps:
        p = Path(args.timestamps)
        ts = json.loads(p.read_text()) if p.exists() else {}
        ts["t_train_start"] = t_train_start
        p.write_text(json.dumps(ts, indent=2) + "\n")

    peft_cfg = LoraConfig(
        r=args.lora_r,
        lora_alpha=args.lora_alpha,
        lora_dropout=0.05,
        bias="none",
        task_type="CAUSAL_LM",
        target_modules=["q_proj", "k_proj", "v_proj", "o_proj", "gate_proj", "up_proj", "down_proj"],
    )
    cfg = SFTConfig(
        output_dir=args.out,
        num_train_epochs=args.epochs,
        learning_rate=args.lr,
        lr_scheduler_type="cosine",
        warmup_ratio=0.03,
        per_device_train_batch_size=args.batch,
        per_device_eval_batch_size=args.batch,
        gradient_accumulation_steps=args.grad_accum,
        gradient_checkpointing=True,
        bf16=True,
        max_length=args.seq,
        # Loss on the assistant turn only; the prompt is given, not learned.
        assistant_only_loss=True,
        logging_steps=10,
        eval_strategy="epoch",
        save_strategy="epoch",
        save_total_limit=1,
        report_to="none",
        seed=20260921,
    )
    trainer = SFTTrainer(
        model=model,
        args=cfg,
        train_dataset=data["train"],
        eval_dataset=data["val"],
        processing_class=tok,
        peft_config=peft_cfg,
    )
    result = trainer.train()
    trainer.save_model(args.out)
    tok.save_pretrained(args.out)

    summary = {
        "model": args.model,
        "train_examples": len(data["train"]),
        "val_examples": len(data["val"]),
        "train_loss": result.training_loss,
        "log_history": trainer.state.log_history,
        "t_train_start": t_train_start,
        "t_train_end": int(time.time()),
    }
    Path(args.out, "train_summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    print(json.dumps({k: v for k, v in summary.items() if k != "log_history"}, indent=2))


if __name__ == "__main__":
    main()
