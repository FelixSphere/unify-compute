#!/usr/bin/env bash
# Creates an environment's SSM SecureString parameters, idempotently.
#
#   scripts/staging-secrets.sh [--env staging|production] [--prefix /<name>] [--force]
#
# --env picks the prefix and the parameter set: staging runs its own console
# and needs four, plus artifacts_token_secret; production runs no console and
# no Redis and keeps the bridge dark (bridge_enabled = false), so it needs
# only postgres_password and artifacts_token_secret. Creating a production
# bridge_secret here would be a secret nothing reads, waiting to be mistaken
# for the real one. artifacts_token_secret signs job upload tokens (ADR-0005);
# rotating it only invalidates the tokens of jobs running at that moment.
#
# Why this is a script and not four `aws ssm put-parameter` lines in a runbook:
#
# The runbook said to generate them with `openssl rand -base64 32`. The base64
# alphabet includes "/", and these values are interpolated raw into two
# connection URLs in deploy/compose/docker-compose.yml:
#
#   postgresql://unify:$POSTGRES_PASSWORD@postgres:5432/console
#   redis://:$REDIS_PASSWORD@redis:6379
#
# A "/" ends the URL authority, so the DSN silently becomes nonsense and
# Postgres refuses the connection. Across 43 base64 characters the chance of at
# least one "/" is about one in two. The failure surfaces as a console that
# never passes its health check, several layers away from the cause.
#
# So: hex. No "/", no "+", no "=", no shell metacharacters, still 256 bits.
set -euo pipefail

ENV_NAME="staging"
PREFIX=""
FORCE=0
REGION="${AWS_REGION:-us-east-1}"

while [ $# -gt 0 ]; do
  case "$1" in
    --env)    ENV_NAME="$2"; shift 2 ;;
    --prefix) PREFIX="$2"; shift 2 ;;
    --force)  FORCE=1; shift ;;
    --region) REGION="$2"; shift 2 ;;
    -h|--help) sed -n '2,10p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

# Names must match locals.ssm_* in deploy/aws/modules/compute-stack/instance.tf,
# which are derived from the stack's name (unify-compute-<env>). The IAM read
# policy is scoped to the same prefix.
case "$ENV_NAME" in
  staging)    KEYS="postgres_password redis_password session_secret bridge_secret artifacts_token_secret" ;;
  production) KEYS="postgres_password artifacts_token_secret" ;;
  *) echo "unknown --env: $ENV_NAME (staging or production)" >&2; exit 2 ;;
esac
PREFIX="${PREFIX:-/unify-compute-$ENV_NAME}"

echo "account: $(aws sts get-caller-identity --query Account --output text)  region: $REGION  prefix: $PREFIX"
echo

for key in $KEYS; do
  name="$PREFIX/$key"
  if aws ssm get-parameter --region "$REGION" --name "$name" >/dev/null 2>&1; then
    if [ "$FORCE" -eq 0 ]; then
      echo "keep    $name (exists)"
      continue
    fi
    # Rotating postgres_password after first boot does not change the password
    # Postgres already initialised on the data volume; it only changes what the
    # clients send. That is a database operation, not a parameter edit.
    echo "WARNING $name exists and --force was given."
    echo "        If the stack has booted, rotating this does not rotate the"
    echo "        database — it breaks the clients. Ctrl-C now unless you know."
    sleep 5
  fi

  value="$(openssl rand -hex 32)"
  aws ssm put-parameter --region "$REGION" \
    --name "$name" --type SecureString --value "$value" --overwrite >/dev/null
  echo "created $name"
done

echo
echo "Verifying every parameter is readable and URL-safe:"
bad=0
for key in $KEYS; do
  name="$PREFIX/$key"
  value="$(aws ssm get-parameter --region "$REGION" --with-decryption \
    --name "$name" --query 'Parameter.Value' --output text)"
  if [ -z "$value" ]; then
    printf 'FAIL %-48s empty\n' "$name"; bad=1
  elif ! printf '%s' "$value" | grep -Eq '^[0-9a-f]{64}$'; then
    # Positive assertion, not a blocklist: 64 hex characters cannot contain
    # anything that changes the meaning of a connection URL or a shell word.
    printf 'FAIL %-48s not 64 hex characters — may break a connection URL\n' "$name"; bad=1
  else
    printf 'ok   %-48s %d chars\n' "$name" "${#value}"
  fi
done

exit "$bad"
