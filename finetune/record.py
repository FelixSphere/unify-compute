#!/usr/bin/env python3
"""Build the job record: four timestamps, three intervals, two cost ratios.

Reimplements sku.EstimateQuota from compute-api/internal/sku/sku.go. If the two
disagree, the Go code is right.
"""
import argparse
import json
from fractions import Fraction
from pathlib import Path

QUOTA_PER_USD = 500000


def round_half_up(fr):
    return (fr.numerator * 2 + fr.denominator) // (fr.denominator * 2)


def estimate_quota(price_usd_per_hour, seconds):
    return round_half_up(Fraction(price_usd_per_hour) * Fraction(seconds, 3600) * QUOTA_PER_USD)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--timestamps", required=True, nargs="+", help="one or more timestamps.json files to merge")
    ap.add_argument("--skus", default=str(Path(__file__).resolve().parents[1] / "compute-api/config/skus.json"))
    ap.add_argument("--sku", default="a10g-24gb-x1")
    ap.add_argument("--max-runtime-seconds", type=int, required=True)
    ap.add_argument("--actual-bill-usd", default=None, help="the provider's spot bill for the instance, when known")
    ap.add_argument("--out", required=True)
    args = ap.parse_args()

    ts = {}
    for p in args.timestamps:
        ts.update(json.loads(Path(p).read_text()))
    required = ["t_request", "t_instance_up", "t_train_start", "t_process_exit"]
    missing = [k for k in required if k not in ts]
    if missing:
        raise SystemExit(f"timestamps missing: {missing}")

    sku = next(s for s in json.loads(Path(args.skus).read_text())["skus"] if s["id"] == args.sku)
    price = sku["price_usd_per_hour"]

    intervals = {
        "queue_and_provision_s": ts["t_instance_up"] - ts["t_request"],
        "cold_start_s": ts["t_train_start"] - ts["t_instance_up"],
        "compute_s": ts["t_process_exit"] - ts["t_train_start"],
    }
    billable_s = ts["t_process_exit"] - ts["t_instance_up"]
    hold_quota = estimate_quota(price, args.max_runtime_seconds)
    settle_quota = estimate_quota(price, billable_s)

    record = {
        "sku": args.sku,
        "price_usd_per_hour": price,
        "max_runtime_seconds": args.max_runtime_seconds,
        "timestamps": {k: ts[k] for k in required},
        "intervals": intervals,
        "billable_seconds": billable_s,
        "hold_quota": hold_quota,
        "settle_quota": settle_quota,
        "hold_usd": f"{hold_quota / QUOTA_PER_USD:.4f}",
        "settle_usd": f"{settle_quota / QUOTA_PER_USD:.4f}",
        "hold_over_settle": round(hold_quota / settle_quota, 3) if settle_quota else None,
    }
    if args.actual_bill_usd is not None:
        bill = Fraction(args.actual_bill_usd)
        record["actual_bill_usd"] = args.actual_bill_usd
        record["catalogue_over_bill"] = round(float(Fraction(settle_quota, QUOTA_PER_USD) / bill), 3) if bill else None

    Path(args.out).write_text(json.dumps(record, indent=2) + "\n")
    print(f"| interval | seconds |\n|---|---|")
    for k, v in intervals.items():
        print(f"| {k} | {v} |")
    print(f"\nhold {record['hold_usd']} USD vs settle {record['settle_usd']} USD "
          f"(ratio {record['hold_over_settle']})")
    if "catalogue_over_bill" in record:
        print(f"catalogue / bill = {record['catalogue_over_bill']}")


if __name__ == "__main__":
    main()
