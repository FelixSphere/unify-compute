#!/usr/bin/env bash
# Prove an image digest is safe to release, from the registry's own answers:
#
#   GH_TOKEN=... scripts/release/provenance.sh ghcr.io/felixsphere/<repo>@sha256:<digest>
#
# Prints `commit=<sha>` for the commit it was built from, and fails unless:
#   - the digest exists and its index carries linux/arm64 (every host is
#     Graviton; an amd64-only image is a container that never starts);
#   - a `sha-<commit>` tag points at exactly this digest (the publish
#     workflows tag every build that way);
#   - that commit is on main;
#   - CI concluded success on that commit.
#
# This is the gate that `needs:` would give us if images were built in the
# release workflow. They are built by the publish workflows instead, so the
# chain from "tests passed" to "these bytes" is re-established here, per
# digest, rather than assumed from the fact that someone typed it.
set -euo pipefail

REF="${1:?usage: provenance.sh <image>@sha256:<digest>}"
: "${GH_TOKEN:?}" "${GITHUB_REPOSITORY:?}"
ACTOR="${GITHUB_ACTOR:-x-access-token}"

repo="${REF%@*}"
digest="${REF#*@}"
path="${repo#ghcr.io/}"
case "$REF" in
  ghcr.io/*@sha256:*) ;;
  *) echo "::error::$REF is not ghcr.io/<path>@sha256:<digest>"; exit 1 ;;
esac

token="$(curl -fsS -u "$ACTOR:$GH_TOKEN" \
  "https://ghcr.io/token?service=ghcr.io&scope=repository:${path}:pull" | jq -r .token)"
if [ -z "$token" ] || [ "$token" = null ]; then echo "::error::no registry token for $path"; exit 1; fi
auth=(-H "Authorization: Bearer $token")
accept='application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json'

manifest="$(mktemp)"; trap 'rm -f "$manifest"' EXIT
if ! curl -fsS "${auth[@]}" -H "Accept: $accept" -o "$manifest" "https://ghcr.io/v2/${path}/manifests/${digest}"; then
  echo "::error::$REF does not exist in the registry"; exit 1
fi
if ! jq -e '.manifests[]? | select(.platform.os == "linux" and .platform.architecture == "arm64")' "$manifest" >/dev/null; then
  echo "::error::$REF has no linux/arm64 manifest; the hosts are t4g (Graviton)"; exit 1
fi
echo "ok   $REF exists and carries linux/arm64"

tags="$(curl -fsS "${auth[@]}" "https://ghcr.io/v2/${path}/tags/list?n=10000" \
  | jq -r '.tags[]? | select(test("^sha-[0-9a-f]{40}$"))')"
candidates=()
for t in $tags; do
  d="$(curl -fsSI "${auth[@]}" -H "Accept: $accept" "https://ghcr.io/v2/${path}/manifests/${t}" \
    | tr -d '\r' | awk -F': ' 'tolower($1) == "docker-content-digest" { print $2 }')"
  [ "$d" = "$digest" ] && candidates+=("${t#sha-}")
done
if [ "${#candidates[@]}" -eq 0 ]; then
  echo "::error::no sha-<commit> tag points at $digest; cannot tell which commit built it"; exit 1
fi

git fetch -q origin main
for c in "${candidates[@]}"; do
  if ! git merge-base --is-ancestor "$c" origin/main 2>/dev/null; then
    echo "     built from $c, which is not on main"; continue
  fi
  n="$(gh api "repos/$GITHUB_REPOSITORY/actions/workflows/ci.yml/runs?head_sha=$c&status=success&per_page=1" -q .total_count)"
  if [ "${n:-0}" -gt 0 ]; then
    echo "ok   built from $c: on main, CI green"
    echo "commit=$c"
    exit 0
  fi
  echo "     built from $c, which is on main but has no successful CI run"
done
echo "::error::$REF was not built from a commit that is on main with CI green"
exit 1
