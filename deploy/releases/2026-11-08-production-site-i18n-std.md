---
environment: production
action: release
outcome: success
run: 42491954565
commit: fc0edb0188b9beda3c057af3df646c64e2ad7179
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fd586e94c7fe65a844ccae5f8ee4f44f39ff15591bd5f5cdfa37df4c67e77ae4
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:5925c7c10f3c5b6555f57507c2ec512ccf5bb3721cccc46a48c257a8ee1bccb0
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fd586e94c7fe65a844ccae5f8ee4f44f39ff15591bd5f5cdfa37df4c67e77ae4
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:26d2e66c320a6ca0b60373ea61dfd0e4fc18f30f48f6bd5ccc744e40dd18268f
hotfix: false
---

# 2026-11-08 — compute.unifyai.us in the standard's languages, in production

The same digests as staging run 42491696406, exercised there
([`2026-11-08-staging-site-i18n-std.md`](2026-11-08-staging-site-i18n-std.md)).
Released by the UnifyAI CI/CD agent.

## What shipped

Site `26d2e66c…` (`2c7669f`) → `5925c7c1…` (`fc0edb0`): #110, plus
docs #139 and #146. compute-api unchanged (`fd586e94…`, `e7fe722`). No
price, migration, env var or Terraform change.

## Before / after

- Before (status run 42491627269): `fd586e94…` / `26d2e66c…`, PREVIOUS
  `fd586e94…` / `bd9edfdb…`.
- Staging-first gate: "staging ran exactly these digests" (run
  42491696406). Provenance passed for both digests.
- Backup `/data/backups/20261108T145332Z-42491954565`, copied to
  `s3://unify-compute-production-backups-871376691052/production/releases/20261108T145332Z-42491954565/`.
- Smoke passed in full, including `compute.unifyai.us` resolving to the ALB
  and HSTS.
- After: `fd586e94…` / `5925c7c1…`; PREVIOUS `fd586e94…` / `26d2e66c…`,
  same schema, so `mode=rollback` is a site-only step.

## What was exercised

- `check-i18n.sh https://compute.unifyai.us/` and `… /privacy`: **PASS**,
  all 11 locales. Before the release: FAIL (10 locale paths 404, `/`
  without hreflang, canonical or switcher).
- Byte comparison with staging: `/`, `/vi`, `/ar`, `/privacy`,
  `/vi/privacy` identical on `compute.unifyai.us` and
  `site.staging.compute.unifyai.us`.

## What this does not prove

Nothing signed in was run on production; the app host is unchanged by this
release and was covered by smoke only.
