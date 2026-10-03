#!/usr/bin/env bash
# Refuse a production release of digests that staging has not run.
#
#   GH_TOKEN=... COMPUTE_API_IMAGE=<ref> SITE_IMAGE=<ref> scripts/release/staging-first.sh
#
# Evidence is machine-made, not prose: every successful release.yml run
# uploads a manifest artifact as its LAST step, named after the environment
# and the digests. Here we look for one from staging with these exact
# digests, then check the run that made it really was a successful
# release.yml dispatch on main, and compare the full digests inside it.
#
# A release record in deploy/releases/ is written after the fact by a person
# or an agent; it is the history, not the gate. Artifacts expire after 90
# days, and a digest staging ran three months ago should go through staging
# again anyway.
set -euo pipefail

: "${GH_TOKEN:?}" "${GITHUB_REPOSITORY:?}" "${COMPUTE_API_IMAGE:?}" "${SITE_IMAGE:?}"

short() { local d="${1#*@sha256:}"; printf '%s' "${d:0:12}"; }
name="release-manifest-staging-$(short "$COMPUTE_API_IMAGE")-$(short "$SITE_IMAGE")"
echo "looking for artifact $name"

work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
gh api "repos/$GITHUB_REPOSITORY/actions/artifacts?name=$name&per_page=100" \
  -q '.artifacts[] | select(.expired == false) | .workflow_run.id' > "$work/runs"

while read -r run; do
  [ -n "$run" ] || continue
  info="$(gh api "repos/$GITHUB_REPOSITORY/actions/runs/$run" \
    -q '[.path, .event, .head_branch, .conclusion, .html_url] | @tsv')"
  IFS=$'\t' read -r wf_path event branch conclusion url <<<"$info"
  if [ "$wf_path" != .github/workflows/release.yml ] || [ "$event" != workflow_dispatch ] \
     || [ "$branch" != main ] || [ "$conclusion" != success ]; then
    echo "     run $run does not count: $wf_path $event $branch $conclusion"
    continue
  fi
  rm -rf "$work/m" && mkdir "$work/m"
  gh run download "$run" --repo "$GITHUB_REPOSITORY" -n "$name" -D "$work/m" >/dev/null
  m="$work/m/manifest.env"
  if grep -qx "ENVIRONMENT=staging" "$m" \
     && grep -qxF "COMPUTE_API_IMAGE=$COMPUTE_API_IMAGE" "$m" \
     && grep -qxF "SITE_IMAGE=$SITE_IMAGE" "$m"; then
    echo "ok   staging ran exactly these digests: $url"
    echo "staging_run=$url"
    exit 0
  fi
  echo "     run $run: short digests collide but the full digests differ"
done < "$work/runs"

echo "::error::these digests have no successful staging release. Release them to staging first: gh workflow run release.yml -f environment=staging -f compute_api_image=$COMPUTE_API_IMAGE -f site_image=$SITE_IMAGE -f reason=..."
exit 1
