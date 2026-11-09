---
environment: production
action: release
outcome: success
run: 42585299399
commit: 322f70535498ffe666d5ec922d3731a0dad078c9
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:b7474c942012f43d362265171e480c24f4d93a0d085640916f32b2d187bdf042
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:ed5de01aba3f5695882a8f56bad0a1eaed8583d19585cb76dcb088307e77dca4
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fd586e94c7fe65a844ccae5f8ee4f44f39ff15591bd5f5cdfa37df4c67e77ae4
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:5925c7c10f3c5b6555f57507c2ec512ccf5bb3721cccc46a48c257a8ee1bccb0
hotfix: false
---

# 2026-11-09: SGLang batch templates (#145), on production

The unify-compute CI/CD agent released this on the operator's decision,
relayed by the coordinator. These are the same digests staging ran
(`2026-11-09-staging-sglang-templates.md`, run 42584482012). The
staging-first gate passed ("staging ran exactly these digests").

## Operator's waiver

The operator **waived** staging check 3 (`batch-extract-sglang` without
`/mnt/schema` → `400 missing_input`), which the staging record lists as
not exercised. The operator's reason: the 400 comes from the generic
declared-input check, which #145 did not change, and `e2e_test.go` covers it
(`"missing input"` case). Checked before dispatch:
`e7fe722..322f705` changes nothing in `internal/template/template.go` or
`internal/api/`, and the `missing_input` loop over `t.Inputs` applies to every
template. The check has still not been run against a live host for this
template.

## What shipped

The staging record lists the commits. compute-api `fd586e94…` (`e7fe722`) →
`b7474c94…` (`322f705`): three templates in `config/templates.json`, plus
tests. Site `5925c7c1…` (`fc0edb0`) → `ed5de01a…` (`322f705`): their
strings in the ten locales. No change to prices, migrations, env vars or
Terraform.

## Before / after

- Before (status run 42585203163): `fd586e94…` / `5925c7c1…`, which matches
  the 2026-11-08 record. IMDS lockdown present.
- Backup `/data/backups/20261109T082203Z-42585299399` (compute, 29 tables),
  copied to
  `s3://unify-compute-production-backups-871376691052/production/releases/20261109T082203Z-42585299399/`.
- The host passed its stability check, and smoke passed in full: `/healthz`
  reports build `322f705…`, SKUs are priced, `missing_key` 401, the
  site/icon/app-host CSP probes pass, and `compute.unifyai.us` resolves to
  the ALB.
- PREVIOUS_RELEASE is now `fd586e94…` / `5925c7c1…`, with the same schema,
  so `mode=rollback` is a plain step back.

## What was exercised (read-only, no job submitted)

1. `https://api.compute.unifyai.us/healthz` reports build
   `322f70535498ffe666d5ec922d3731a0dad078c9`. `GET /compute/v1/templates`
   lists **7**, all `available: true`, including the three new ids.
2. `https://compute.unifyai.us/marketplace` and `/zh/marketplace`: 200, with
   "7 templates" / "7 个模板" and the three new titles.
   `/marketplace/<id>` in en, `/zh` and `/ar` returns 200 for all three
   new ids, with a translated `<title>` and `<html lang dir>` (`zh-Hans`, and
   `ar` with `rtl`).

## What this does not prove

- `missing_input` for `batch-extract-sglang` on a live host (waived, above).
- Production has no GPU workers (`gpu_workers_enabled = false`) and no
  SkyPilot, so a submit there ends at `503 upstream_unavailable` whatever the
  template. No template has run on a GPU anywhere. The first launch pulls the
  large `lmsysorg/sglang` image. Whether SkyPilot passes the `@sha256`
  reference through `image_id: docker:<ref>` unchanged is still open
  (`docs/spec/templates.md`).
