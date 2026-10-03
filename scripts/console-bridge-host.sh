#!/bin/sh
# Connects (on) or disconnects (off) a RUNNING production host's compute-api
# and the production console bridge, over SSM:
#
#   sed 's/^MODE=.*/MODE=on/'  scripts/console-bridge-host.sh > /tmp/b.sh   # or MODE=off
#   scripts/ssm-run.sh i-07dd2f6d313a0dafd /tmp/b.sh "console bridge on" 900
#
# A rebuilt instance does not need this: user-data renders the same two lines
# from bridge_enabled = true and /unify-compute-production/bridge_secret. This
# is for the host that is already running, because user_data is ignored after
# first boot. It also rotates: write the new value to both parameters, run the
# console side, then this with MODE=on.
#
# The secret is read on the host with the instance role and never passes
# through the SSM command, its output, or this script's arguments.
#
# Both lines change together. CONSOLE_URL set with an empty or short secret is
# a crash loop at boot (console.New refuses it; production's first boot,
# 2026-10-02); both empty is compute-api's own dark mode (keyed routes 503
# console_unavailable). Only compute-api is recreated; the site and Postgres
# are not touched. POSIX sh: ssm-run.sh hands this to sh.
set -eu
MODE=on
CONSOLE_URL_VALUE=https://app.unifyapi.ai
PARAM=/unify-compute-production/bridge_secret
REGION=us-east-1
COMPOSE_DIR=/opt/unify-compute/compose

exec 9>/run/lock/unify-compute-ops.lock
flock -w 900 9 || { echo "FAIL: operations lock is held"; exit 1; }
cd "$COMPOSE_DIR"

TS=$(date -u +%Y%m%dT%H%M%SZ)
B=/data/backups/${TS}-manual-console-bridge-$MODE
install -d -m 0700 "$B"
cp -p .env "$B/.env"
echo "BACKUP $B/.env"

case "$MODE" in
  on)
    SECRET=$(aws ssm get-parameter --with-decryption --region "$REGION" --name "$PARAM" --query Parameter.Value --output text)
    case "$SECRET" in *[!0-9a-f]*|"") echo "FAIL: $PARAM missing or not hex"; exit 1 ;; esac
    [ ${#SECRET} -ge 64 ] || { echo "FAIL: $PARAM shorter than 64 hex"; exit 1; }
    URL=$CONSOLE_URL_VALUE
    ;;
  off) SECRET=""; URL="" ;;
  *) echo "FAIL: MODE must be on or off"; exit 2 ;;
esac

grep -v -e '^COMPUTE_BRIDGE_SECRET=' -e '^CONSOLE_URL=' .env > "$B/.env.new"
printf 'COMPUTE_BRIDGE_SECRET=%s\nCONSOLE_URL=%s\n' "$SECRET" "$URL" >> "$B/.env.new"
unset SECRET
cat "$B/.env.new" > .env
rm -f "$B/.env.new"
diff "$B/.env" .env | sed -E 's/(SECRET=)[0-9a-f]+/\1<hex>/' || true

docker compose config -q
docker compose up -d compute-api
c=000
for _ in $(seq 1 30); do
  c=$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 http://127.0.0.1:8080/healthz || true)
  [ "$c" = 200 ] && break
  sleep 2
done
echo "healthz $c"
docker logs --since 1m compose-compute-api-1 2>&1 | grep -e 'console bridge' -e 'CONSOLE_URL unset' | tail -1
[ "$c" = 200 ] || { echo "FAIL: compute-api not healthy; restore with: cp -p $B/.env $COMPOSE_DIR/.env && docker compose up -d compute-api"; exit 1; }
echo "CONSOLE_BRIDGE_HOST_DONE $MODE"
