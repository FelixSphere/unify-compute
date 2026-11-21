---
environment: staging
action: release
outcome: success
run: 42491696406
commit: fc0edb0188b9beda3c057af3df646c64e2ad7179
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fd586e94c7fe65a844ccae5f8ee4f44f39ff15591bd5f5cdfa37df4c67e77ae4
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:5925c7c10f3c5b6555f57507c2ec512ccf5bb3721cccc46a48c257a8ee1bccb0
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fd586e94c7fe65a844ccae5f8ee4f44f39ff15591bd5f5cdfa37df4c67e77ae4
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:26d2e66c320a6ca0b60373ea61dfd0e4fc18f30f48f6bd5ccc744e40dd18268f
hotfix: false
---

# 2026-11-08 — compute.unifyai.us in the standard's languages, on staging

The operator's request of 2026-11-08 ("已合并，发布产品") to release #110.
Released by the UnifyAI CI/CD agent.

## What shipped

Site `26d2e66c…` (`2c7669f`) → `5925c7c1…` (`fc0edb0`), published by
publish-image.yml run 42491501458 (tag `site-i18n-std-2026-11-08`).
`2c7669f..fc0edb0`:

- `fc0edb0` (#110): the marketing site in the UI standard's languages
  (English at `/`, ten prefixed locales including `vi` and RTL `ar`),
  hreflang/canonical on every page, a language switcher, a localized
  sitemap, and `npm run check:i18n` added to CI.
- `cdc9cf1` (#146): AGENTS.md "configuration before code". Docs only.
- `bd51eb3` (#139): release records for smooth-load-fix. Docs only.

compute-api is unchanged (`fd586e94…`, `e7fe722`). No price
(`skus.json` untouched), migration, env var or Terraform change.

## Before / after

- Before (status run 42491503994): `fd586e94…` / `26d2e66c…`, the same
  pair as production (status run 42491627269), so staging was at parity.
- Backup `/data/backups/20261108T145019Z-42491696406`, copied to
  `s3://unify-compute-staging-backups-871376691052/staging/releases/20261108T145019Z-42491696406/`.
- Only `site` changed; the host's 30 s stability check passed (no restart,
  no replacement). Smoke passed in full, including `/healthz` build
  `e7fe722`, skus priced, `missing_key` 401, the app host's CSP.

## What was exercised

`~/unifyai-brand-kit/check-i18n.sh https://site.staging.compute.unifyai.us/`
and the same with `/privacy`: **PASS** for all 11 locales (lang, `dir=rtl`
on ar, every hreflang + x-default, canonical, every switcher label). The
same check against production before this release: FAIL, 10 of 11 locale
paths 404.

## What this does not prove

The signed-in app host is not part of this change and was covered only by
smoke. Language rendering was checked by markup, not read by a speaker.
