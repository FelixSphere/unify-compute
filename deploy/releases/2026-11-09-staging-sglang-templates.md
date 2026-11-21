---
environment: staging
action: release
outcome: success
run: 42584482012
commit: 322f70535498ffe666d5ec922d3731a0dad078c9
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:b7474c942012f43d362265171e480c24f4d93a0d085640916f32b2d187bdf042
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:ed5de01aba3f5695882a8f56bad0a1eaed8583d19585cb76dcb088307e77dca4
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fd586e94c7fe65a844ccae5f8ee4f44f39ff15591bd5f5cdfa37df4c67e77ae4
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:5925c7c10f3c5b6555f57507c2ec512ccf5bb3721cccc46a48c257a8ee1bccb0
hotfix: false
---

# 2026-11-09: SGLang batch templates (#145), on staging

Released by the unify-compute CI/CD agent when #145 was handed over. Production
was **not** released. The operator allowed production only if every staging
check passed, and one of them (`missing_input`, below) could not be run.

## What shipped

Both images were built from `322f705` (merge of #145) by publish-image.yml
runs 42583467545 (compute-api) and 42583480383 (site), tag
`sglang-templates-2026-11-09`. CI on `322f705`: run 42583027452, green.

compute-api `fd586e94…` (`e7fe722`) → `b7474c94…` (`322f705`).
`e7fe722..322f705 -- compute-api`:

- `c24a542`: `batch-infer-sglang` in `config/templates.json`.
- `adccb29`: `batch-infer-lora-sglang` (`--lora-paths`, `MAX_LORA_RANK` 64).
- `3604e22`: `batch-extract-sglang`, with a second declared input
  `/mnt/schema`.

All three run `lmsysorg/sglang:v0.5.19-cu129-runtime@sha256:fe08f6f5…`
(Apache-2.0) on `a10g-24gb-x1` with a 3600 s default runtime. Only the
template file and tests changed. Go code, `skus.json`, `platform.json`,
schema, env vars and Terraform did not.

Site `5925c7c1…` (`fc0edb0`) → `ed5de01a…` (`322f705`): `1352be7`, the
template strings in the ten locale files. The other commits in the range
change only docs and records (#149, `docs/spec/templates.md`,
`THIRD-PARTY-LICENSES.md`).

## Before / after

- Before (status run 42583505772): `fd586e94…` / `5925c7c1…`, which matches
  the 2026-11-08 records. IMDS lockdown present.
- The first dispatch, run 42584384101, failed in "Install crane" with an HTTP
  504 while downloading crane. That was before the ECR copy, the instance
  lookup and the cutover, so the host was not touched. 42584482012 is a fresh
  dispatch of the same digests.
- Backup `/data/backups/20261109T081516Z-42584482012` (compute 29 tables,
  console 59), copied to
  `s3://unify-compute-staging-backups-871376691052/staging/releases/20261109T081516Z-42584482012/`.
- The host passed its 30 s stability check. Smoke passed in full: `/healthz`
  reports build `322f705…`, SKUs are priced, `missing_key` 401, and the site,
  icon cache and app-host CSP probes pass.
- The host's PREVIOUS_RELEASE is now `fd586e94…` / `5925c7c1…`, with the same
  schema, so `mode=rollback` works as a plain step back.

## What was exercised

1. `GET https://staging.compute.unifyai.us/compute/v1/templates` lists
   **7** templates, all `available: true`: batch-embed,
   batch-extract-sglang, batch-infer-lora, batch-infer-lora-sglang,
   batch-infer-sglang, batch-infer-vllm, finetune-lora. compute-api parses
   `templates.json` at boot and stops on an invalid one, so it parsed the new
   file.
2. Site, `https://site.staging.compute.unifyai.us`: `/marketplace` and
   `/zh/marketplace` both return 200 and show "7 templates" / "7 个模板" and
   the three new titles. `/marketplace/<id>`, `/zh/marketplace/<id>` and
   `/ar/marketplace/<id>` return 200 for all three new ids. Each has a
   translated `<title>` and the right `<html lang dir>` (`zh-Hans`, and `ar`
   with `rtl`).
3. **Not exercised:** `batch-extract-sglang` without `/mnt/schema` →
   `400 missing_input`. In `resolveSubmit`, template resolution runs before
   billing, so any valid `uck_` key reaches it, even on an account with
   compute off. No valid key was available, though. Earlier throwaway keys
   are revoked, and a new account needs its email code, which earlier
   releases recovered over SSM. The AWS credentials in this agent's
   environment were rejected (`InvalidClientTokenId`). A made-up key gets
   `401 missing_key`, as expected.

## What this does not prove

- The `missing_input` contract for `batch-extract-sglang` on a live host
  (item 3).
- No GPU run. None of the three templates has run on a GPU. Their first
  launch pulls `lmsysorg/sglang` (large) onto the worker. Nothing has checked
  that SkyPilot passes the `@sha256` reference through
  `image_id: docker:<ref>` unchanged; this is the same open question as for
  the vLLM templates (`docs/spec/templates.md`).
- Translations were checked from the markup, not read by a speaker.
