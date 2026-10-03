#!/bin/sh
# Runs ON THE INSTANCE, as root, through scripts/ssm-run.sh. Read-only.
#
#   scripts/ssm-run.sh <instance-id> scripts/release/check-imds.sh "check imds <env>"
#
# Proves the IMDS lockdown (scripts/imds-lockdown.sh; ADR-0005, amendment of
# 2026-10-03) on a running host:
#
# - the host itself gets an IMDSv2 token. This is the control: without it a
#   broken probe would read as "blocked";
# - from the network namespace of EVERY running container, compute-api
#   included, the token PUT gets no answer at all. compute-api has no IMDS
#   either; it reads the role from a file;
# - compute-api mounts that file, is told its path, and the file holds
#   credentials with more than ten minutes left;
# - the firewall rule is in place and the refresh timer is active.
#
# The probe runs the host's curl inside each container's network namespace
# (nsenter -n) rather than `docker exec`: compute-api and the site are
# distroless or minimal and Postgres and Redis ship no curl, and a throwaway
# curl image would add a registry pull to a production host. The network
# namespace is the whole question: routing and the host's rules are the same
# for every process in it.
#
# Ends with CHECK_IMDS_OK, or CHECK_IMDS_FAILED and a non-zero exit.
set -eu

IMDS=${IMDS:-http://169.254.169.254}
FIREWALL=${FIREWALL:-/usr/local/sbin/unify-compute-imds-firewall}
CREDS_UNIT=${CREDS_UNIT:-unify-compute-api-credentials}
CREDS_HOST_DIR=${CREDS_HOST_DIR:-/run/unify-compute/compute-api-aws}
CREDS_MOUNT=${CREDS_MOUNT:-/run/unify-compute/aws}
CREDS_MIN_SECONDS=${CREDS_MIN_SECONDS:-600}

say() { printf '%s\n' "$*"; }
fail=0
bad() { say "FAIL $*"; fail=1; }

for tool in docker nsenter curl; do
  command -v "$tool" >/dev/null 2>&1 || { say "FAIL $tool is not installed"; say CHECK_IMDS_FAILED; exit 1; }
done

# HTTP status of the token PUT, 000 when nothing answered. Prefixed by
# `nsenter -t <pid> -n` to run it in a container's network namespace. Empty
# if nsenter itself failed, which counts as an answer: never a false "ok".
probe() {
  "$@" curl -s -o /dev/null -w '%{http_code}' -m 2 -X PUT \
    -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' "$IMDS/latest/api/token" || true
}

code=$(probe)
if [ "$code" = 200 ]; then
  say "ok   host: IMDS token (control)"
else
  bad "host: no IMDS token (HTTP $code); without the control the container probes prove nothing"
  say CHECK_IMDS_FAILED
  exit 1
fi

ids=$(docker ps -q)
[ -n "$ids" ] || bad "no running containers"
for id in $ids; do
  name=$(docker inspect -f '{{.Name}}' "$id"); name=${name#/}
  if [ "$(docker inspect -f '{{.HostConfig.NetworkMode}}' "$id")" = host ]; then
    bad "$name: network_mode host, so it is the host as far as IMDS is concerned"
    continue
  fi
  pid=$(docker inspect -f '{{.State.Pid}}' "$id")
  code=$(probe nsenter -t "$pid" -n)
  if [ "$code" = 000 ]; then
    say "ok   $name: IMDS unreachable"
  else
    bad "$name: IMDS answered HTTP $code"
  fi
done

api=$(docker ps -q --filter label=com.docker.compose.service=compute-api)
case "$(printf '%s\n' "$api" | grep -c .)" in
  1)
    if docker inspect -f '{{range .Mounts}}{{.Source}}:{{.Destination}}:{{.RW}}{{"\n"}}{{end}}' "$api" \
        | grep -qx "$CREDS_HOST_DIR:$CREDS_MOUNT:false"; then
      say "ok   compute-api: mounts $CREDS_HOST_DIR read-only at $CREDS_MOUNT"
    else
      bad "compute-api: does not mount $CREDS_HOST_DIR read-only at $CREDS_MOUNT"
    fi
    if docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$api" \
        | grep -qx "INSTANCE_ROLE_CREDENTIALS_FILE=$CREDS_MOUNT/credentials.json"; then
      say "ok   compute-api: INSTANCE_ROLE_CREDENTIALS_FILE=$CREDS_MOUNT/credentials.json"
    else
      bad "compute-api: INSTANCE_ROLE_CREDENTIALS_FILE is not $CREDS_MOUNT/credentials.json"
    fi
    ;;
  0) bad "no running compute-api container" ;;
  *) bad "more than one running compute-api container" ;;
esac

f=$CREDS_HOST_DIR/credentials.json
if [ -s "$f" ]; then
  exp=$(sed -n 's/.*"Expiration"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$f" | head -n 1)
  left=$(( $(date -d "$exp" +%s 2>/dev/null || echo 0) - $(date +%s) ))
  if [ "$left" -gt "$CREDS_MIN_SECONDS" ]; then
    say "ok   $f: expires $exp (${left}s left), owner $(stat -c '%u %a' "$f")"
  else
    bad "$f: expires '$exp' (${left}s left); is $CREDS_UNIT.timer running?"
  fi
else
  bad "$f is missing or empty"
fi

if [ -x "$FIREWALL" ] && "$FIREWALL" check; then
  say "ok   firewall: rule in place ($FIREWALL check)"
else
  bad "firewall: rule missing or $FIREWALL not installed; run scripts/imds-lockdown.sh"
fi
if systemctl is-active --quiet "$CREDS_UNIT.timer"; then
  say "ok   $CREDS_UNIT.timer active"
else
  bad "$CREDS_UNIT.timer is not active"
fi

if [ "$fail" = 0 ]; then say CHECK_IMDS_OK; else say CHECK_IMDS_FAILED; exit 1; fi
