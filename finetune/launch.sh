#!/usr/bin/env bash
# Run the reference fine-tune end to end from a machine with AWS credentials.
#   ./launch.sh <run-id> s3://<artifact-bucket> [MAX_RUNTIME_SECONDS]
set -euo pipefail
cd "$(dirname "$0")"

RUN_ID=${1:?run id}
BUCKET=${2:?s3://bucket}
MAX_RUNTIME_SECONDS=${3:-7200}
OUT="out/${RUN_ID}"
mkdir -p "$OUT"

[ -f data/test.jsonl ] || { echo "run prepare_data.py first"; exit 1; }
aws s3 sync --quiet data/ "${BUCKET}/finetune/${RUN_ID}/data"

echo "{\"t_request\": $(date +%s)}" > "$OUT/timestamps.local.json"
sky launch -c "ft-${RUN_ID}" sky/finetune.yaml \
  --env RUN_ID="$RUN_ID" --env BUCKET="$BUCKET" --env MAX_RUNTIME_SECONDS="$MAX_RUNTIME_SECONDS" \
  --down -y

aws s3 sync --quiet "${BUCKET}/finetune/${RUN_ID}/out" "$OUT"
cat "$OUT/RESULT" 2>/dev/null || echo "no RESULT file: the run did not reach the end"
python record.py --timestamps "$OUT/timestamps.local.json" "$OUT/timestamps.json" \
  --max-runtime-seconds "$MAX_RUNTIME_SECONDS" --out "$OUT/job-record.json"
