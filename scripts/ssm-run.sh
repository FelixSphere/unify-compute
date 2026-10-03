#!/usr/bin/env bash
# Run a shell script on one instance through SSM, and fail if it failed.
#
#   scripts/ssm-run.sh <instance-id> <script-file> <comment> [timeout-seconds]
#
# The host's stdout is printed and also written to <script-file>.out, so a
# workflow can read machine lines (e.g. `RELEASED KEY=ref`) from it.
#
# Taken from FelixSphere/unifydata, where both rules below were learned by a
# failed release:
#
# The payload is encoded by `jq` from a file, never escaped by hand into JSON
# inside YAML. `\$` is not a valid JSON escape, so a hand-escaped `$(date ...)`
# reaches the host as a literal `\$(date ...)` and `sh` rejects it.
#
# The host runs the payload with `sh`. Keep payloads POSIX: on a Debian-family
# host that is dash, and `set -o pipefail` on line 1 once made a whole host
# change silently do nothing.
set -euo pipefail

if [ $# -lt 3 ]; then
  sed -n '2,6p' "$0" >&2
  exit 2
fi

INSTANCE=$1
SCRIPT=$2
COMMENT=$3
TIMEOUT=${4:-600}

jq -Rs '{commands: [.]}' "$SCRIPT" > "$SCRIPT.params.json"

ID=$(aws ssm send-command \
  --instance-ids "$INSTANCE" \
  --document-name AWS-RunShellScript \
  --comment "${COMMENT:0:100}" \
  --timeout-seconds "$TIMEOUT" \
  --parameters "file://$SCRIPT.params.json" \
  --query Command.CommandId --output text)

echo "ssm command: $ID"

# Poll with our own deadline rather than `aws ssm wait command-executed`. The
# waiter gives up after about 100 seconds regardless of --timeout-seconds,
# which is only SSM's *delivery* deadline. In unifydata the waiter failed a
# release whose host step completed successfully five seconds later, after
# the host had already been changed.
DEADLINE=$(( $(date +%s) + TIMEOUT ))
while : ; do
  # Right after send-command the invocation may not exist yet. That is
  # Pending, not a failure.
  STATUS=$(aws ssm get-command-invocation --command-id "$ID" --instance-id "$INSTANCE" \
    --query Status --output text 2>/dev/null || echo Pending)
  case "$STATUS" in
    Success|Failed|Cancelled|TimedOut) break ;;
  esac
  if [ "$(date +%s)" -ge "$DEADLINE" ]; then
    # We stopped watching; the host did not necessarily stop working.
    echo "::error::$COMMENT still $STATUS after ${TIMEOUT}s; command $ID may still be running on $INSTANCE"
    exit 1
  fi
  sleep 5
done

# SSM truncates StandardOutputContent at 24,000 characters. The host scripts
# keep their output short (`pull --quiet`) so the machine lines at the end
# survive.
aws ssm get-command-invocation --command-id "$ID" --instance-id "$INSTANCE" \
  --query StandardOutputContent --output text | tee "$SCRIPT.out"
echo "--- stderr"
aws ssm get-command-invocation --command-id "$ID" --instance-id "$INSTANCE" \
  --query StandardErrorContent --output text

echo "status: $STATUS"
[ "$STATUS" = Success ] || { echo "::error::$COMMENT failed: $STATUS (ssm command $ID)"; exit 1; }
