#!/bin/sh
# Runs ON THE INSTANCE, as root. Closes the EC2 instance metadata service
# (IMDS) to every container on a compute host, and gives compute-api the
# instance role another way (ADR-0005, amendment of 2026-10-03). Never run it
# from a laptop.
#
# One implementation, two callers, so a running host and a rebuilt one end up
# the same:
#
# - user-data, at first boot (modules/compute-stack/user-data.sh.tftpl):
#   PHASE=files, before the stack first starts;
# - a one-time SSM run on a host that booted before this existed
#   (docs/staging.md, "IMDS lockdown"): PHASE=all, the default.
#     scripts/ssm-run.sh <instance-id> scripts/imds-lockdown.sh "imds lockdown <env>"
#
# What it installs:
#
# 1. unify-compute-imds-firewall.service: one iptables rule in the raw
#    table's PREROUTING chain dropping every packet that arrives on any
#    interface for 169.254.169.254 (and ip6tables for fd00:ec2::254). Traffic
#    the host itself sends never passes PREROUTING, so the host's own timers
#    and the AWS CLI keep IMDS; every container, on any bridge, loses it.
#    Ordered before docker.service and PartOf it, so the rule is in place
#    before Docker starts a container at boot and is re-applied whenever
#    Docker restarts.
# 2. unify-compute-api-credentials.timer: every two minutes the host copies
#    IMDS's answer for the instance role to
#    /run/unify-compute/compute-api-aws/credentials.json (tmpfs, owned by
#    compute-api's uid, 0400). Only compute-api mounts it.
# 3. docker-compose.imds.yml, added to COMPOSE_FILE: that mount, and
#    INSTANCE_ROLE_CREDENTIALS_FILE for compute-api. `print-overlay` as the
#    first argument prints it (make compose-config validates it that way).
#
# The instance's IMDS hop limit is 1 as well (instance.tf), so a container
# whose packets got past the rule still could not receive a token. Either
# layer alone keeps containers out.
#
# Idempotent. Ends with `IMDS_LOCKDOWN_DONE <phase>`.
#
# POSIX sh: SSM's AWS-RunShellScript runs payloads with `sh`.
set -eu

overlay() {
  cat <<'YAML'
# compute-api's AWS credentials on a host where no container may reach IMDS
# (ADR-0005, amendment of 2026-10-03). Written to the host and added to
# COMPOSE_FILE by scripts/imds-lockdown.sh; not used locally.
services:
  compute-api:
    environment:
      INSTANCE_ROLE_CREDENTIALS_FILE: /run/unify-compute/aws/credentials.json
    volumes:
      # The directory, not the file: the timer replaces the file by rename,
      # and a file bind mount would keep showing the old inode.
      - /run/unify-compute/compute-api-aws:/run/unify-compute/aws:ro
YAML
}

if [ "${1:-}" = print-overlay ]; then
  overlay
  exit 0
fi

PHASE=${PHASE:-all}

# Same contract as scripts/release/host.sh.
COMPOSE_DIR=${COMPOSE_DIR:-/opt/unify-compute/compose}
ENV_FILE=$COMPOSE_DIR/.env
LOCK_FILE=${LOCK_FILE:-/run/lock/unify-compute-ops.lock}

OVERLAY=docker-compose.imds.yml
FIREWALL=/usr/local/sbin/unify-compute-imds-firewall
FIREWALL_UNIT=unify-compute-imds-firewall
REFRESH=/usr/local/sbin/unify-compute-api-credentials
CREDS_UNIT=unify-compute-api-credentials
CREDS_FILE=/run/unify-compute/compute-api-aws/credentials.json

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }
compose() { docker compose --env-file "$ENV_FILE" "$@"; }

# nsenter proves the rule from inside compute-api's network namespace; a
# missing one would read as "unreachable".
for tool in iptables nsenter curl flock; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is not installed"
done

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
  cat > "$FIREWALL" <<'SH'
#!/bin/sh
# Installed by scripts/imds-lockdown.sh. `apply` adds the rules if they are
# missing; `check` fails if they are not there. Never removes them: stopping
# the unit leaves the host locked down.
#
# The raw table rather than Docker's DOCKER-USER chain: it is evaluated
# before anything Docker installs, Docker never rewrites it, and it does not
# depend on which firewall backend the daemon uses.
set -eu
v4="-d 169.254.169.254/32 -m comment --comment unify-compute-imds -j DROP"
v6="-d fd00:ec2::254/128 -m comment --comment unify-compute-imds -j DROP"
has6() { command -v ip6tables >/dev/null 2>&1 && ip6tables -t raw -S PREROUTING >/dev/null 2>&1; }
# shellcheck disable=SC2086 # the rules are word lists on purpose
case "${1:-apply}" in
  apply)
    iptables -t raw -C PREROUTING $v4 2>/dev/null || iptables -t raw -I PREROUTING 1 $v4
    if has6; then ip6tables -t raw -C PREROUTING $v6 2>/dev/null || ip6tables -t raw -I PREROUTING 1 $v6; fi
    ;;
  check)
    iptables -t raw -C PREROUTING $v4 2>/dev/null || { echo "missing: iptables -t raw PREROUTING $v4" >&2; exit 1; }
    if has6; then
      ip6tables -t raw -C PREROUTING $v6 2>/dev/null || { echo "missing: ip6tables -t raw PREROUTING $v6" >&2; exit 1; }
    fi
    ;;
  *) echo "usage: $0 apply|check" >&2; exit 2 ;;
esac
SH
  chmod 0755 "$FIREWALL"

  # Before docker.service, not after it: Docker starts `restart:
  # unless-stopped` containers as soon as it is up, and After= would leave
  # them a window without the rule on every boot. WantedBy docker.service, so
  # starting Docker by any route starts this first; PartOf it, so restarting
  # Docker re-applies the rule.
  cat > "/etc/systemd/system/$FIREWALL_UNIT.service" <<EOF
[Unit]
Description=Drop container traffic to the EC2 instance metadata service
Before=docker.service unify-compute.service
PartOf=docker.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=$FIREWALL apply

[Install]
WantedBy=docker.service multi-user.target
EOF

  cat > "$REFRESH" <<'SH'
#!/bin/sh
# Installed by scripts/imds-lockdown.sh. Copies IMDS's answer for the
# instance role, which only the host can reach, to the one file compute-api
# mounts. compute-api (awsv4.File) parses it as IMDS's own JSON.
set -eu
umask 077
IMDS=http://169.254.169.254
DIR=/run/unify-compute/compute-api-aws
# compute-api's image runs as distroless's nonroot user.
OWNER=65532:65532
tok=$(curl -fsS -m 5 -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' "$IMDS/latest/api/token")
role=$(curl -fsS -m 5 -H "X-aws-ec2-metadata-token: $tok" "$IMDS/latest/meta-data/iam/security-credentials/" | head -n 1)
[ -n "$role" ] || { echo "the instance has no role" >&2; exit 1; }
body=$(curl -fsS -m 5 -H "X-aws-ec2-metadata-token: $tok" "$IMDS/latest/meta-data/iam/security-credentials/$role")
case "$body" in
  *'"Success"'*) ;;
  *) echo "IMDS returned no credentials for $role" >&2; exit 1 ;;
esac
mkdir -p "$DIR"
chown "$OWNER" "$DIR"
chmod 0700 "$DIR"
printf '%s\n' "$body" > "$DIR/credentials.json.new"
chown "$OWNER" "$DIR/credentials.json.new"
chmod 0400 "$DIR/credentials.json.new"
mv -f "$DIR/credentials.json.new" "$DIR/credentials.json"
SH
  chmod 0700 "$REFRESH"

  # Before docker on boot, so compute-api never starts without the file; but
  # Before= orders without requiring, so a failed copy does not keep the
  # rest of the stack down. EC2 offers rotated credentials at least five
  # minutes before the old ones expire; every two minutes keeps the file
  # inside that, and compute-api re-reads it when five minutes remain.
  cat > "/etc/systemd/system/$CREDS_UNIT.service" <<EOF
[Unit]
Description=Copy the instance role's credentials for compute-api
Wants=network-online.target
After=network-online.target
Before=docker.service

[Service]
Type=oneshot
ExecStart=$REFRESH

[Install]
WantedBy=multi-user.target
EOF
  cat > "/etc/systemd/system/$CREDS_UNIT.timer" <<EOF
[Unit]
Description=Refresh compute-api's copy of the instance role's credentials every 2 minutes

[Timer]
OnBootSec=1min
OnUnitActiveSec=2min
AccuracySec=1s

[Install]
WantedBy=timers.target
EOF

  systemctl daemon-reload
  systemctl enable "$FIREWALL_UNIT.service" "$CREDS_UNIT.service" "$CREDS_UNIT.timer" >/dev/null 2>&1
  # restart, not start: a rerun with a changed rule must apply it.
  systemctl restart "$FIREWALL_UNIT.service"
  "$FIREWALL" check || die "the IMDS rule is not in place after $FIREWALL_UNIT started"
  say "firewall: container traffic to IMDS is dropped ($FIREWALL_UNIT)"

  "$REFRESH" || die "could not copy the instance role's credentials from IMDS on the host"
  systemctl start "$CREDS_UNIT.timer"
  say "credentials: $CREDS_FILE, refreshed by $CREDS_UNIT.timer"

  umask 077
  overlay > "$COMPOSE_DIR/$OVERLAY.new"
  mv -f "$COMPOSE_DIR/$OVERLAY.new" "$COMPOSE_DIR/$OVERLAY"
  cf=$(env_get COMPOSE_FILE)
  case ":$cf:" in
    *":$OVERLAY:"*) ;;
    *) env_set COMPOSE_FILE "${cf:-docker-compose.yml}:$OVERLAY" ;;
  esac
  say "files: $COMPOSE_DIR/$OVERLAY; COMPOSE_FILE=$(env_get COMPOSE_FILE)"
}

# compute-api picks up the mount and the variable, and can then no longer
# reach IMDS itself. A compute-api older than awsv4.File ignores the
# variable and loses the role; that is the right trade (nothing uses it
# before ADR-0005 is applied) and it is said loudly.
running() {
  compose up -d --no-deps compute-api
  cid=$(compose ps -q compute-api)
  [ -n "$cid" ] || die "compute-api has no container after up -d"
  pid=$(docker inspect -f '{{.State.Pid}}' "$cid")
  if code=$(nsenter -t "$pid" -n curl -s -o /dev/null -w '%{http_code}' -m 2 -X PUT \
      -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' http://169.254.169.254/latest/api/token) \
      && [ "$code" != 000 ]; then
    die "compute-api still reaches IMDS (HTTP $code)"
  fi
  say "compute-api: IMDS unreachable from its network namespace"

  if [ -n "$(env_get ARTIFACTS_BUCKET)" ] && [ -n "$(env_get ARTIFACTS_TOKEN_SECRET)" ]; then
    seen=0
    for _ in 1 2 3 4 5 6; do
      if compose logs --no-log-prefix compute-api 2>&1 | grep -q 'aws credentials from file'; then seen=1; break; fi
      sleep 5
    done
    if [ "$seen" = 1 ]; then
      say "compute-api: reads the instance role from $CREDS_FILE"
    else
      say "WARNING: artifact storage is configured but this compute-api does not read INSTANCE_ROLE_CREDENTIALS_FILE; it has no AWS credentials until a release ships a build with awsv4.File"
    fi
  fi
}

# The host's operations lock (scripts/release/host.sh).
mkdir -p "$(dirname "$LOCK_FILE")"
exec 9>"$LOCK_FILE"
flock -w 900 9 || die "operations lock $LOCK_FILE is held by another operation"

case "$PHASE" in
  files) files ;;
  all) files; running ;;
  *) die "unknown PHASE $PHASE (files, all)" ;;
esac
say "IMDS_LOCKDOWN_DONE $PHASE"
