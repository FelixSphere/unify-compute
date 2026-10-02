#!/bin/sh
# Runs ON THE INSTANCE, as root, through scripts/ssm-run.sh. Never run it from
# a laptop: it rewrites the compose env file and restarts containers.
#
# .github/workflows/release.yml prepends the parameters as plain assignments
# (each value validated on the runner first) and sends the result:
#
#   ACTION=release|rollback|status
#   ENV_NAME=staging|production
#   RELEASE_ID=<workflow run id>
#   REGION=us-east-1
#   TARGET='COMPUTE_API_IMAGE=ghcr.io/...@sha256:... SITE_IMAGE=ghcr.io/...@sha256:...'
#
# POSIX sh on purpose: SSM's AWS-RunShellScript runs the payload with `sh`.
# No pipefail, no arrays, no [[ ]].
#
# Machine lines on stdout, read by the workflow: `CURRENT KEY=ref`,
# `PREVIOUS KEY=ref`, `RELEASED KEY=ref`, `NOOP`, `BACKUP <dir> <s3 uri>`.
set -eu

# --- The contract with the host (deploy/aws, agent M2) ----------------------
# Assumptions, stated here so a mismatch is a one-line change rather than a
# hunt. Each is overridable by a prepended assignment.
APP_DIR=${APP_DIR:-/opt/unify-compute}
# Where docker-compose.yml and its .env live. The systemd unit's
# WorkingDirectory must be this directory, so that `docker compose` here
# addresses the same project the unit started.
COMPOSE_DIR=${COMPOSE_DIR:-$APP_DIR/compose}
ENV_FILE=${ENV_FILE:-$COMPOSE_DIR/.env}
# On the data volume, so release history and backups survive an instance
# replacement.
STATE_DIR=${STATE_DIR:-/data/releases}
BACKUP_ROOT=${BACKUP_ROOT:-/data/backups}
KEEP_LOCAL_BACKUPS=${KEEP_LOCAL_BACKUPS:-10}
LOCK_FILE=${LOCK_FILE:-/run/lock/unify-compute-ops.lock}
LOCK_WAIT=${LOCK_WAIT:-900}
# Same prefix as the stack's secrets, so the instance role's existing
# ssm:GetParameter scope already covers it.
SSM_PREFIX=${SSM_PREFIX:-/unify-compute-$ENV_NAME}
BACKUP_BUCKET_PARAM=${BACKUP_BUCKET_PARAM:-$SSM_PREFIX/backup_bucket}
PG_SERVICE=${PG_SERVICE:-postgres}
PG_USER=${PG_USER:-unify}
# KEY:compose-service:repository. A release may only change these keys, and
# each only to a digest of its own repository.
MANAGED=${MANAGED:-"COMPUTE_API_IMAGE:compute-api:ghcr.io/felixsphere/unify-compute-api SITE_IMAGE:site:ghcr.io/felixsphere/unify-compute-site"}
# compute-api is distroless and has no container healthcheck, so "running" is
# all docker can say about it. This probe is what says it answers.
LOCAL_PROBES=${LOCAL_PROBES:-http://127.0.0.1:8080/healthz}
HEALTH_TIMEOUT=${HEALTH_TIMEOUT:-240}
STABLE_SECONDS=${STABLE_SECONDS:-30}
# -----------------------------------------------------------------------------

: "${ACTION:?}" "${ENV_NAME:?}" "${RELEASE_ID:?}" "${REGION:?}"
TARGET=${TARGET:-}

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

cd "$COMPOSE_DIR" || die "compose directory $COMPOSE_DIR does not exist"
[ -f "$ENV_FILE" ] || die "compose env file $ENV_FILE does not exist"

compose() { docker compose --env-file "$ENV_FILE" "$@"; }

keys() { for m in $MANAGED; do printf '%s\n' "${m%%:*}"; done; }
service_of() { for m in $MANAGED; do [ "${m%%:*}" = "$1" ] && { r=${m#*:}; printf '%s' "${r%%:*}"; return; }; done; }
repo_of() { for m in $MANAGED; do [ "${m%%:*}" = "$1" ] && { printf '%s' "${m##*:}"; return; }; done; }

env_get() { sed -n "s/^$1=//p" "$ENV_FILE" | tail -n 1; }

# "KEY=ref KEY=ref" for what the env file says now.
current_set() {
  out=""
  for k in $(keys); do out="$out $k=$(env_get "$k")"; done
  printf '%s' "${out# }"
}

# A ref must be <its own repository>@sha256:<64 hex>. A tag can be repointed
# by anyone who can push; a digest cannot.
validate_set() {
  for kv in $1; do
    k=${kv%%=*}; v=${kv#*=}
    repo=$(repo_of "$k")
    [ -n "$repo" ] || die "$k is not a key a release may change (managed: $(keys | tr '\n' ' '))"
    case "$v" in
      "$repo"@sha256:*) ;;
      *) die "$k=$v is not $repo@sha256:<digest>" ;;
    esac
    d=${v#*@sha256:}
    [ "${#d}" -eq 64 ] || die "$k digest is not 64 hex characters"
    case "$d" in *[!0-9a-f]*) die "$k digest is not lowercase hex" ;; esac
  done
  for k in $(keys); do
    case " $1 " in *" $k="*) ;; *) die "release does not name $k; a release is the full set of digests" ;; esac
  done
}

# Replace (or append) the managed keys in the env file, atomically. Every
# other line, the secrets included, is copied byte for byte.
write_env() {
  awk -v kv="$1" '
    BEGIN { n = split(kv, a, " "); for (i = 1; i <= n; i++) { p = index(a[i], "="); v[substr(a[i], 1, p - 1)] = substr(a[i], p + 1) } }
    { p = index($0, "="); if (p > 1) { k = substr($0, 1, p - 1); if (k in v) { print k "=" v[k]; seen[k] = 1; next } } print }
    END { for (k in v) if (!(k in seen)) print k "=" v[k] }
  ' "$ENV_FILE" > "$ENV_FILE.new"
  chmod 0600 "$ENV_FILE.new"
  mv -f "$ENV_FILE.new" "$ENV_FILE"
}

# name id restarts status, one line per container, sorted.
snapshot() {
  for id in $(compose ps -aq); do
    docker inspect -f '{{.Name}} {{.Id}} {{.RestartCount}} {{.State.Status}}' "$id"
  done | sort
}

# Healthy means: every container of the project is running, and is healthy
# where it has a healthcheck; every local probe answers; then nothing restarts
# or is replaced for STABLE_SECONDS. A container that crashes and comes back
# under `restart: unless-stopped` looks fine a few seconds later. unifydata's
# version of this step stayed green through a six-second crash until it
# compared restart counts, so this compares them and prints them either way.
verify() {
  want=$1
  s0=$(snapshot)
  deadline=$(( $(date +%s) + HEALTH_TIMEOUT ))
  while : ; do
    ids=$(compose ps -aq)
    [ -n "$ids" ] || { say "no containers in the compose project"; return 1; }
    pending=0; bad=0
    for id in $ids; do
      read -r name status health <<EOF
$(docker inspect -f '{{.Name}} {{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$id")
EOF
      case "$status:$health" in
        running:healthy|running:none) ;;
        running:starting|created:*) pending=1 ;;
        *) say "  $name is $status/$health"; bad=1 ;;
      esac
    done
    if [ "$bad" = 0 ] && [ "$pending" = 0 ]; then
      for url in $LOCAL_PROBES; do
        curl -fsS -o /dev/null --max-time 5 "$url" 2>/dev/null || pending=1
      done
      [ "$pending" = 0 ] && break
    fi
    [ "$bad" = 0 ] || { compose ps -a; compose logs --tail 30 2>&1 | tail -n 60; return 1; }
    if [ "$(date +%s)" -ge "$deadline" ]; then
      say "not healthy after ${HEALTH_TIMEOUT}s"
      compose ps -a; compose logs --tail 30 2>&1 | tail -n 60
      for url in $LOCAL_PROBES; do say "  probe $url: $(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 "$url" 2>&1 || true)"; done
      return 1
    fi
    sleep 5
  done

  sleep "$STABLE_SECONDS"
  s2=$(snapshot)
  say "--- containers after up -d (name id restarts status)"; say "$s0"
  say "--- containers after ${STABLE_SECONDS}s stable (name id restarts status)"; say "$s2"
  # Same containers, same restart counts, all running. Status is excluded from
  # the comparison because s0 may legitimately catch one in `created`.
  if [ "$(say "$s0" | cut -d' ' -f1-3)" != "$(say "$s2" | cut -d' ' -f1-3)" ]; then
    say "a container restarted or was replaced after cutover"; compose logs --tail 40 2>&1 | tail -n 80
    return 1
  fi
  if say "$s2" | grep -v ' running$' >/dev/null; then
    say "a container is not running"; return 1
  fi

  # The swap actually happened: each service runs the exact ref asked for.
  for kv in $want; do
    k=${kv%%=*}; v=${kv#*=}; svc=$(service_of "$k")
    cid=$(compose ps -q "$svc")
    [ -n "$cid" ] || { say "service $svc ($k) has no container"; return 1; }
    got=$(docker inspect -f '{{.Config.Image}}' "$cid")
    [ "$got" = "$v" ] || { say "$svc runs $got, expected $v"; return 1; }
  done
  return 0
}

print_set() { for kv in $2; do say "$1 $kv"; done; }

ghcr_login() {
  # Only if the stack provides pull credentials; public packages need none.
  user=$(aws ssm get-parameter --region "$REGION" --name "$SSM_PREFIX/ghcr_pull_user" --query Parameter.Value --output text 2>/dev/null || true)
  token=$(aws ssm get-parameter --region "$REGION" --with-decryption --name "$SSM_PREFIX/ghcr_pull_token" --query Parameter.Value --output text 2>/dev/null || true)
  if [ -n "$user" ] && [ -n "$token" ]; then
    printf '%s' "$token" | docker login ghcr.io -u "$user" --password-stdin >/dev/null
    say "ghcr: logged in as $user"
  fi
}

backup() {
  B="$BACKUP_ROOT/$STAMP-$RELEASE_ID"
  mkdir -p "$B"; chmod 0700 "$B"
  cp -p "$ENV_FILE" "$B/env.before"
  printf '%s\n' "$CURRENT" | tr ' ' '\n' > "$B/PREVIOUS_RELEASE"

  if ! bucket=$(aws ssm get-parameter --region "$REGION" --name "$BACKUP_BUCKET_PARAM" --query Parameter.Value --output text); then
    die "backup bucket parameter $BACKUP_BUCKET_PARAM is missing or unreadable; no cutover without an off-host backup"
  fi
  if [ -z "$bucket" ] || [ "$bucket" = None ]; then
    die "backup bucket parameter $BACKUP_BUCKET_PARAM is empty"
  fi

  dbs=$(compose exec -T "$PG_SERVICE" psql -U "$PG_USER" -d postgres -Atc \
        "select datname from pg_database where not datistemplate and datname <> 'postgres' order by 1")
  [ -n "$dbs" ] || die "found no databases to back up in $PG_SERVICE"
  for db in $dbs; do
    compose exec -T "$PG_SERVICE" pg_dump -U "$PG_USER" -Fc "$db" > "$B/$db.dump"
    [ -s "$B/$db.dump" ] || die "pg_dump of $db is empty"
    # A dump nobody has read back is a hope. pg_restore -l parses the whole
    # archive's table of contents.
    compose exec -T "$PG_SERVICE" pg_restore -l < "$B/$db.dump" > "$B/$db.toc"
    say "backup: $db $(wc -c < "$B/$db.dump" | tr -d " ") bytes, $(grep -c ' TABLE DATA ' "$B/$db.toc" || true) tables"
  done
  (cd "$B" && sha256sum ./*.dump PREVIOUS_RELEASE > SHA256SUMS)

  # env.before holds the secrets; it stays on the host (0700) and never goes
  # to S3.
  S3_URI="s3://$bucket/$ENV_NAME/releases/$STAMP-$RELEASE_ID/"
  aws s3 cp --recursive --only-show-errors --region "$REGION" --exclude env.before "$B/" "$S3_URI"
  say "BACKUP $B $S3_URI"
}

prune_local_backups() {
  # Never fatal: a snapshot left behind costs disk, a release failed over it
  # costs a rollback. S3 retention is the bucket's lifecycle rule.
  n=$(find "$BACKUP_ROOT" -mindepth 1 -maxdepth 1 -type d | wc -l)
  if [ "$n" -gt "$KEEP_LOCAL_BACKUPS" ]; then
    find "$BACKUP_ROOT" -mindepth 1 -maxdepth 1 -type d | sort | head -n "$(( n - KEEP_LOCAL_BACKUPS ))" \
      | while read -r old; do rm -rf "$old" && say "pruned $old"; done || say "pruning failed; not fatal"
  fi
}

CURRENT=$(current_set)

if [ "$ACTION" = status ]; then
  print_set CURRENT "$CURRENT"
  [ -f "$STATE_DIR/PREVIOUS_RELEASE" ] && print_set PREVIOUS "$(tr '\n' ' ' < "$STATE_DIR/PREVIOUS_RELEASE")"
  say "--- containers (name id restarts status)"; snapshot
  say "--- last releases"; tail -n 5 "$STATE_DIR/history.log" 2>/dev/null || say "(none)"
  exit 0
fi

# The host's operations lock: no other release, rollback, data fix or reboot
# may overlap this one. Held until the script exits. Anything else that
# writes data or restarts a service on this host must take it too:
#   flock -w 900 /run/lock/unify-compute-ops.lock <command>
mkdir -p "$(dirname "$LOCK_FILE")" "$STATE_DIR" "$BACKUP_ROOT"
exec 9>"$LOCK_FILE"
flock -w "$LOCK_WAIT" 9 || die "operations lock $LOCK_FILE is held by another operation"

case "$ACTION" in
  release)
    [ -n "$TARGET" ] || die "release needs TARGET"
    ;;
  rollback)
    [ -s "$STATE_DIR/PREVIOUS_RELEASE" ] || die "no $STATE_DIR/PREVIOUS_RELEASE on this host; nothing to roll back to"
    TARGET=$(grep -v '^$' "$STATE_DIR/PREVIOUS_RELEASE" | tr '\n' ' ')
    TARGET=${TARGET% }
    ;;
  *) die "unknown ACTION $ACTION" ;;
esac
validate_set "$TARGET"

# Refuse to start a second stack beside the first. If nothing is running in
# this directory's project, COMPOSE_DIR is probably not where the unit runs.
[ -n "$(compose ps -q)" ] || die "no running containers in the compose project at $COMPOSE_DIR; is this the directory the systemd unit uses?"

print_set CURRENT "$CURRENT"
print_set TARGET "$TARGET"

if [ "$TARGET" = "$CURRENT" ]; then
  say "target equals what is running; verifying health only"
  verify "$TARGET" || die "already running $TARGET but it is not healthy"
  say NOOP
  print_set RELEASED "$TARGET"
  exit 0
fi

# Pull before touching anything. A missing digest or an image without arm64
# fails here, with the old stack still up and nothing to undo.
ghcr_login
for kv in $TARGET; do docker pull -q "${kv#*=}"; done

STAMP=$(date -u +%Y%m%dT%H%M%SZ)
# In the same script as the cutover on purpose: a dump taken minutes earlier,
# before something else wrote, is not the one to restore. compute-api migrates
# at startup, so this dump is the only way back from a bad migration.
backup

SWAPPED=0
on_exit() {
  rc=$?
  trap - EXIT
  if [ "$rc" -ne 0 ] && [ "$SWAPPED" = 1 ]; then
    set +e
    say "!!! cutover failed (exit $rc); restoring the previous images"
    cp -p "$B/env.before" "$ENV_FILE.restore" && mv -f "$ENV_FILE.restore" "$ENV_FILE"
    compose up -d --remove-orphans --quiet-pull
    if verify "$CURRENT"; then
      say "AUTO_ROLLBACK healthy on the previous images"
    else
      say "AUTO_ROLLBACK UNHEALTHY: the previous images did not come back healthy either"
    fi
    say "The database was NOT restored. If the new release migrated it, the dump is in $B."
    print_set RUNNING "$CURRENT"
  fi
  exit "$rc"
}
trap on_exit EXIT

SWAPPED=1
write_env "$TARGET"
compose up -d --remove-orphans --quiet-pull
verify "$TARGET" || die "the new release is not healthy"

tmp="$STATE_DIR/PREVIOUS_RELEASE.new"
printf '%s\n' "$CURRENT" | tr ' ' '\n' > "$tmp" && mv -f "$tmp" "$STATE_DIR/PREVIOUS_RELEASE"
printf '%s\n' "$TARGET" | tr ' ' '\n' > "$STATE_DIR/CURRENT_RELEASE"
printf '%s %s %s %s\n' "$STAMP" "$ACTION" "$RELEASE_ID" "$TARGET" >> "$STATE_DIR/history.log"
SWAPPED=0

prune_local_backups
print_set PREVIOUS "$CURRENT"
print_set RELEASED "$TARGET"
