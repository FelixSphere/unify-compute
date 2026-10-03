#!/bin/sh
# Runs ON THE INSTANCE, as root. Installs the SkyPilot API server on a compute
# host (ADR-0010). Never run it from a laptop.
#
# One implementation, two callers, so the running host and a rebuilt one end
# up the same:
#
# - user-data, at first boot (modules/compute-stack/user-data.sh.tftpl):
#   PHASE=files before the stack starts, PHASE=token after;
# - a one-time SSM run on a host that booted before this existed
#   (docs/staging.md, "GPU workers and SkyPilot"): PHASE=all, the default.
#
# Terraform renders the payload (output `skypilot_host_setup` of the
# environment root): these assignments, then this file.
#
#   ENV_NAME=staging|production
#   REGION=us-east-1
#   LAUNCH_ROLE_ARN=arn:aws:iam::<acct>:role/unify-compute-<env>-skypilot
#   SSM_ADMIN_PASSWORD=/unify-compute-<env>/skypilot_admin_password
#   OVERLAY_B64=<deploy/compose/docker-compose.skypilot.yml, base64>
#   SKY_CONFIG_B64=<SkyPilot config.yaml from modules/gpu-workers, base64>
#
# Idempotent: a second run rewrites the same files, keeps the existing
# service-account token, and recreates only containers whose config changed.
#
# POSIX sh: SSM's AWS-RunShellScript runs payloads with `sh`.
set -eu

: "${ENV_NAME:?}" "${REGION:?}" "${LAUNCH_ROLE_ARN:?}" "${SSM_ADMIN_PASSWORD:?}" "${OVERLAY_B64:?}" "${SKY_CONFIG_B64:?}"
PHASE=${PHASE:-all}

# Same contract as scripts/release/host.sh.
COMPOSE_DIR=${COMPOSE_DIR:-/opt/unify-compute/compose}
ENV_FILE=$COMPOSE_DIR/.env
LOCK_FILE=${LOCK_FILE:-/run/lock/unify-compute-ops.lock}
DATA_DIR=${DATA_DIR:-/data}

OVERLAY=docker-compose.skypilot.yml
CREDS_DIR=$DATA_DIR/skypilot-aws
AUTH_DIR=$DATA_DIR/skypilot-auth
TOKEN_FILE=$AUTH_DIR/compute-api.token
REFRESH=/usr/local/sbin/unify-compute-skypilot-credentials
UNIT=unify-compute-skypilot-credentials

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }
compose() { docker compose --env-file "$ENV_FILE" "$@"; }

cd "$COMPOSE_DIR" || die "compose directory $COMPOSE_DIR does not exist"
[ -f "$ENV_FILE" ] || die "compose env file $ENV_FILE does not exist"

env_get() { sed -n "s/^$1=//p" "$ENV_FILE" | tail -n 1; }

# Replace or append one key, atomically; every other line byte for byte.
env_set() {
  awk -v k="$1" -v v="$2" '
    index($0, k "=") == 1 { if (!done) print k "=" v; done = 1; next }
    { print }
    END { if (!done) print k "=" v }
  ' "$ENV_FILE" > "$ENV_FILE.new"
  chmod 0600 "$ENV_FILE.new"
  mv -f "$ENV_FILE.new" "$ENV_FILE"
}

files() {
  umask 077
  mkdir -p "$COMPOSE_DIR/skypilot" "$DATA_DIR/skypilot" "$CREDS_DIR" "$AUTH_DIR"

  # Heredocs, not pipes: POSIX sh has no pipefail, and a failed decode must
  # stop here rather than leave a truncated compose file behind.
  base64 -d > "$COMPOSE_DIR/$OVERLAY.new" <<EOF
$OVERLAY_B64
EOF
  base64 -d > "$COMPOSE_DIR/skypilot/config.yaml.new" <<EOF
$SKY_CONFIG_B64
EOF
  if [ ! -s "$COMPOSE_DIR/$OVERLAY.new" ] || [ ! -s "$COMPOSE_DIR/skypilot/config.yaml.new" ]; then
    die "an embedded file decoded to nothing"
  fi
  mv -f "$COMPOSE_DIR/$OVERLAY.new" "$COMPOSE_DIR/$OVERLAY"
  mv -f "$COMPOSE_DIR/skypilot/config.yaml.new" "$COMPOSE_DIR/skypilot/config.yaml"
  cat > "$COMPOSE_DIR/skypilot/aws-config" <<EOF
[default]
region = $REGION
credential_process = cat /run/unify-compute/aws/credentials.json
EOF

  # The host's instance role assumes the launch role. Role chaining caps the
  # session at an hour; refreshing every 20 minutes keeps at least 40 left,
  # and botocore re-runs credential_process when 15 remain.
  cat > "$REFRESH" <<EOF
#!/bin/sh
set -eu
umask 077
aws sts assume-role --region '$REGION' --role-arn '$LAUNCH_ROLE_ARN' \\
  --role-session-name 'skypilot-$ENV_NAME' --duration-seconds 3600 \\
  --query '{Version: \`1\`, AccessKeyId: Credentials.AccessKeyId, SecretAccessKey: Credentials.SecretAccessKey, SessionToken: Credentials.SessionToken, Expiration: Credentials.Expiration}' \\
  --output json > '$CREDS_DIR/credentials.json.new'
mv -f '$CREDS_DIR/credentials.json.new' '$CREDS_DIR/credentials.json'
EOF
  chmod 0700 "$REFRESH"

  # Before docker on boot, so the container never starts on hour-old
  # credentials; Before= orders without requiring, so a failed refresh does
  # not keep the rest of the stack down.
  cat > "/etc/systemd/system/$UNIT.service" <<EOF
[Unit]
Description=Assume the unify-compute $ENV_NAME SkyPilot launch role for the API server
Wants=network-online.target
After=network-online.target
Before=docker.service
RequiresMountsFor=$DATA_DIR

[Service]
Type=oneshot
ExecStart=$REFRESH

[Install]
WantedBy=multi-user.target
EOF
  cat > "/etc/systemd/system/$UNIT.timer" <<EOF
[Unit]
Description=Refresh the SkyPilot launch-role credentials every 20 minutes

[Timer]
OnUnitActiveSec=20min
OnBootSec=1min
Persistent=true

[Install]
WantedBy=timers.target
EOF
  systemctl daemon-reload
  systemctl enable "$UNIT.service" "$UNIT.timer" >/dev/null 2>&1
  systemctl start "$UNIT.timer"
  # First proof that the trust policy and the instance role's sts:AssumeRole
  # agree. Failing here is cheaper than a SkyPilot server that starts and then
  # reports "AWS: disabled". Except at first boot: an apply that creates the
  # launch role and replaces the instance together can boot the host before
  # IAM has propagated, and the rest of the stack must not wait on that. The
  # timer retries a minute after boot and every 20 minutes.
  if "$REFRESH"; then
    say "credentials: assumed $LAUNCH_ROLE_ARN"
  elif [ "$PHASE" = files ]; then
    say "warning: could not assume $LAUNCH_ROLE_ARN yet; $UNIT.timer will retry"
  else
    die "could not assume $LAUNCH_ROLE_ARN with the instance role; has the gpu_workers module been applied?"
  fi

  if ! pw=$(aws ssm get-parameter --with-decryption --region "$REGION" \
      --name "$SSM_ADMIN_PASSWORD" --query Parameter.Value --output text); then
    die "SSM parameter $SSM_ADMIN_PASSWORD is missing or unreadable; create it with scripts/staging-secrets.sh --env $ENV_NAME"
  fi
  case "$pw" in
    *[!0-9a-f]*|"") die "SSM parameter $SSM_ADMIN_PASSWORD is not hex (scripts/staging-secrets.sh generates it)" ;;
  esac
  env_set SKYPILOT_ADMIN_PASSWORD "$pw"
  unset pw

  cf=$(env_get COMPOSE_FILE)
  case ":$cf:" in
    *":$OVERLAY:"*) ;;
    *) env_set COMPOSE_FILE "${cf:-docker-compose.yml}:$OVERLAY" ;;
  esac
  if [ -s "$TOKEN_FILE" ]; then
    env_set SKYPILOT_SERVICE_ACCOUNT_TOKEN "$(cat "$TOKEN_FILE")"
  fi
  say "files: $COMPOSE_DIR/$OVERLAY, $COMPOSE_DIR/skypilot/, $UNIT.timer; COMPOSE_FILE=$(env_get COMPOSE_FILE)"
}

wait_healthy() {
  deadline=$(( $(date +%s) + 300 ))
  while : ; do
    cid=$(compose ps -q skypilot)
    status=$( [ -n "$cid" ] && docker inspect -f '{{.State.Status}}/{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$cid" || echo missing)
    [ "$status" = running/healthy ] && return 0
    if [ "$(date +%s)" -ge "$deadline" ]; then
      say "skypilot is $status after 300s"
      compose logs --tail 60 skypilot 2>&1 | tail -n 60
      return 1
    fi
    sleep 5
  done
}

token() {
  wait_healthy || die "the SkyPilot API server did not become healthy"
  if [ ! -s "$TOKEN_FILE" ]; then
    # From inside the container, but to its network name rather than
    # 127.0.0.1: SkyPilot skips basic auth for loopback callers and then has
    # no user to create the token for (sky/server/server.py,
    # BasicAuthMiddleware). The token never leaves the host.
    # shellcheck disable=SC2016 # expanded inside the container, on purpose
    compose exec -T skypilot sh -c '
      curl -fsS -u "admin:$SKYPILOT_ADMIN_PASSWORD" -H "Content-Type: application/json" \
        -d "{\"token_name\": \"compute_api\", \"expires_in_days\": 0}" \
        http://skypilot:46580/users/service-account-tokens | jq -er .token' > "$TOKEN_FILE.new" \
      || die "could not create the compute_api service-account token (if it already exists in SkyPilot but $TOKEN_FILE is gone, rotate it: POST /users/service-account-tokens/rotate)"
    case "$(cat "$TOKEN_FILE.new")" in
      sky_*) mv -f "$TOKEN_FILE.new" "$TOKEN_FILE" ;;
      *) rm -f "$TOKEN_FILE.new"; die "SkyPilot returned something that is not a service-account token" ;;
    esac
    say "token: created service account compute_api"
  fi
  env_set SKYPILOT_SERVICE_ACCOUNT_TOKEN "$(cat "$TOKEN_FILE")"
  # Recreated only if its environment changed.
  compose up -d --no-deps compute-api
  say "token: compute-api has SKYPILOT_SERVICE_ACCOUNT_TOKEN"
}

# The host's operations lock (scripts/release/host.sh): no release, rollback
# or data fix may overlap this.
mkdir -p "$(dirname "$LOCK_FILE")"
exec 9>"$LOCK_FILE"
flock -w 900 9 || die "operations lock $LOCK_FILE is held by another operation"

case "$PHASE" in
  files) files ;;
  token) token ;;
  all)
    files
    compose up -d --remove-orphans --quiet-pull
    token
    ;;
  *) die "unknown PHASE $PHASE (files, token, all)" ;;
esac
say "SKYPILOT_SETUP_DONE $PHASE"
