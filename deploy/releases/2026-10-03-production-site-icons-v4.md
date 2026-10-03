---
environment: production
action: release
outcome: success
run: 37083501562
commit: ea56779b42498682244718a0e362c8115d5e5953
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:ec4bd964372acf9724365273204c556414a3090ee68035037b0c41b21e7d9902
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:7b9bb6596a8cba954dccd8ff8bd4922eaf23d43d54b0ea9fe8a9b7315bace4ea
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:ec4bd964372acf9724365273204c556414a3090ee68035037b0c41b21e7d9902
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:cd7077d3462abe68b17d33b39437e34121f3a2d7333aee04b9844f854f9bd018
hotfix: false
---

# 2026-10-03 — site-icons-v4: shared tight-cropped tab icons

Operator-approved: "快点发布，要统一用现在的asset风格" ("ship it quickly, and
use the current asset style throughout").

## What shipped

- site: `7b9bb659…`, built from main `ea56779` (publish run 37083160105). From
  the previous site build `a8562dd`: #36 (`9328ba5` shared tight-cropped tab
  icon, `885c141` docs). The tab icons are now `/icon-v4.svg`,
  `/favicon-32-v4.png` and `/favicon.ico?v=4`, from the brand kit. The v3
  icon files are no longer in the image.
- compute-api: unchanged (`ec4bd964…`, a8562dd). No pricing change.

## Before / after

- Before: compute-api `ec4bd964…`, site `cd7077d3…`.
- Backup: /data/backups/20261003T004748Z-37083501562, copied to s3://unify-compute-production-backups-871376691052/production/releases/20261003T004748Z-37083501562/ (compute.dump and TOC, SHA256SUMS, PREVIOUS_RELEASE).
- All containers running with no restart-count change after the stability window.

## What was exercised (from outside, after the run)

- Staging-first gate: `staging ran exactly these digests: …/runs/37083281875`.
- Release smoke on api.compute and compute.unifyai.us, then `served-by-aws.sh`: compute.unifyai.us resolves to the ALB, `/healthz` is the site container, and the ALB sends HSTS.
- Rollback is now possible. A read-only `mode=status` (run 37083685170) reports PREVIOUS = compute-api `ec4bd964…` + site `cd7077d3…`. Both are in ECR. The rollback itself was not run.
- Assets on https://compute.unifyai.us, sha1 compared with `~/unifyai-brand-kit/icons/favicon/`:
  - `/icon-v4.svg` 200, `b4250db5bd5f66ce57f11fb437a8987a14073426` = `icon.svg`
  - `/favicon-32-v4.png` 200, `ab76e0d79241193611508e7843572f5bb809b9a5` = `favicon-32.png`
  - `/favicon.ico?v=4` 200, `af697574d2066e89219a005e1e719da1deebe42f` = `favicon.ico`
- The HTML links `/favicon.ico?v=4`, `/icon-v4.svg` and `/favicon-32-v4.png`,
  and no v3 icon.
- Title "Unify Compute — On-demand inference & GPU compute"; manifest name
  "Unify Compute"; HubSpot loader once; `/privacy` 200; HSTS once. No
  "unifyAI", no "UnifyAI Compute" and no FelixSphere link on `/` or `/privacy`
  (only `contactus@felixsphere.com`).
- `node deploy/site/browser-check.mjs https://compute.unifyai.us`: 15 ok, 0 FAIL. Links navigate
  client-side, and there are no console errors.

## What this does not prove

- That browsers which cached the old tab icon show the new one at once. The
  `?v=4` / `-v4` names bust the HTML-referenced URLs, but a browser's own
  favicon cache is outside our control.
