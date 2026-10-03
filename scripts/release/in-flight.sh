#!/usr/bin/env bash
# Refuse to release while another release.yml run is queued, waiting for
# approval, or running.
#
#   GH_TOKEN=... GITHUB_RUN_ID=<this run> scripts/release/in-flight.sh
#
# The concurrency group serialises cutovers, but it does not stop a second
# dispatch: GitHub keeps one pending run per group and silently cancels the
# older pending one when a third arrives. In unifydata two sessions released
# the same host minutes apart three times in two days, each unaware of the
# other. The host lock serialises the cutovers; only this check makes the
# second dispatch fail loudly instead of quietly shipping twice.
set -euo pipefail

: "${GH_TOKEN:?}" "${GITHUB_REPOSITORY:?}" "${GITHUB_RUN_ID:?}"

others="$(gh api "repos/$GITHUB_REPOSITORY/actions/workflows/release.yml/runs?per_page=50" \
  -q ".workflow_runs[] | select(.status != \"completed\" and .id != $GITHUB_RUN_ID)
       | \"\(.id)\t\(.status)\t\(.actor.login)\t\(.display_title)\t\(.html_url)\"")"

if [ -n "$others" ]; then
  echo "::error::another release is in flight; wait for it, or cancel it, then dispatch again"
  printf '%s\n' "$others"
  exit 1
fi
echo "ok   no other release.yml run is queued, waiting or running"
