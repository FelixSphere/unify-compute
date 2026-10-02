#!/usr/bin/env bash
# CI check: one release record per release, in a shape the next agent can
# read without guessing. See deploy/releases/README.md for the format.
#
#   scripts/release/check-records.sh            # format only
#   GH_TOKEN=... GITHUB_REPOSITORY=... scripts/release/check-records.sh
#                                                # + every release has a record
#
# The second half asks GitHub which release.yml runs succeeded. Every
# successful release or rollback older than GRACE_HOURS must be cited by a
# record's `run:`; a HOTFIX run's record must say what was skipped. A pushed
# commit is not a release, and a release nobody wrote down is found later by
# someone who needed to know.
set -euo pipefail

DIR="${DIR:-deploy/releases}"
GRACE_HOURS="${GRACE_HOURS:-24}"
problems=0
problem() { echo "FAIL $*"; problems=$((problems + 1)); }

field() { awk -v k="$1" 'NR == 1 && $0 != "---" { exit } NR > 1 && $0 == "---" { exit } NR > 1 { p = index($0, ":"); if (p && substr($0, 1, p - 1) == k) { v = substr($0, p + 1); sub(/^[ \t]+/, "", v); sub(/[ \t]+(#.*)?$/, "", v); print v; exit } }' "$2"; }

digest_re='@sha256:[0-9a-f]{64}$'
cited=" "
hotfix_cited=" "
shopt -s nullglob
for f in "$DIR"/*; do
  base="${f##*/}"
  [ "$base" = README.md ] && continue
  if [[ ! "$base" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}-[a-z0-9-]+\.md$ ]]; then
    problem "$f is not named YYYY-MM-DD-<name>.md"; continue
  fi
  [ "$(head -n 1 "$f")" = "---" ] || { problem "$f does not start with the --- field block"; continue; }

  env="$(field environment "$f")"; action="$(field action "$f")"; outcome="$(field outcome "$f")"
  run="$(field run "$f")"; api="$(field compute_api_image "$f")"; site="$(field site_image "$f")"
  hotfix="$(field hotfix "$f")"
  case "$env" in staging|production) ;; *) problem "$f: environment '$env' is not staging|production" ;; esac
  case "$action" in release|rollback) ;; *) problem "$f: action '$action' is not release|rollback" ;; esac
  case "$outcome" in success|failed|rolled-back) ;; *) problem "$f: outcome '$outcome' is not success|failed|rolled-back" ;; esac
  [[ "$run" =~ ^[0-9]+$ ]] || problem "$f: run '$run' is not a release.yml run id"
  [[ "$api" =~ ^ghcr\.io/felixsphere/unify-compute-api$digest_re ]] || problem "$f: compute_api_image is not a unify-compute-api digest"
  [[ "$site" =~ ^ghcr\.io/felixsphere/unify-compute-site$digest_re ]] || problem "$f: site_image is not a unify-compute-site digest"
  case "$hotfix" in
    true) grep -q '^## What was skipped' "$f" || problem "$f: hotfix: true needs a '## What was skipped' section"
          hotfix_cited="$hotfix_cited$run " ;;
    false) ;;
    *) problem "$f: hotfix '$hotfix' is not true|false" ;;
  esac
  cited="$cited$run "
done

if [ -n "${GH_TOKEN:-}" ] && [ -n "${GITHUB_REPOSITORY:-}" ]; then
  cutoff="$(date -u -d "-${GRACE_HOURS} hours" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
            || date -u -v-"${GRACE_HOURS}"H +%Y-%m-%dT%H:%M:%SZ)"
  if ! runs="$(gh api --paginate \
        "repos/$GITHUB_REPOSITORY/actions/workflows/release.yml/runs?status=success&per_page=100" \
        -q '.workflow_runs[] | select(.head_branch == "main") | "\(.id)\t\(.updated_at)\t\(.display_title)"' 2>err.txt)"; then
    if grep -q 'Not Found\|404' err.txt; then
      runs=""; echo "note: release.yml is not on the default branch yet; no releases to account for"
    else
      cat err.txt; problem "could not list release.yml runs"; runs=""
    fi
  fi
  rm -f err.txt
  while IFS=$'\t' read -r id updated title; do
    [ -n "$id" ] || continue
    case "$title" in release\ *|rollback\ *) ;; *) continue ;; esac   # status runs change nothing
    case "$cited" in *" $id "*) ;; *)
      if [[ "$updated" < "$cutoff" ]]; then
        problem "run $id ($title, $updated) has no record in $DIR"
      else
        echo "note: run $id ($title) has no record yet; it must within ${GRACE_HOURS}h of $updated"
      fi
      continue ;;
    esac
    case "$title" in *HOTFIX*)
      case "$hotfix_cited" in *" $id "*) ;; *) problem "run $id is a HOTFIX; its record needs hotfix: true and '## What was skipped'" ;; esac
    esac
  done <<<"$runs"
fi

if [ "$problems" -gt 0 ]; then
  echo "$problems problem(s) with release records"; exit 1
fi
echo "ok   release records"
