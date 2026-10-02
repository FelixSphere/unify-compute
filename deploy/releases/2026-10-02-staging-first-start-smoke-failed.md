---
environment: staging
action: release
outcome: failed
run: 37054858851
commit: dda289da92cff3e1a468ec9c2a003157e431bc03
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:ec4bd964372acf9724365273204c556414a3090ee68035037b0c41b21e7d9902
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:cd7077d3462abe68b17d33b39437e34121f3a2d7333aee04b9844f854f9bd018
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:10cf58392547078e2134f5ff0e1a0c9b0416ca7e186746f07e243697ed58ce4a
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:a918fe89b1cc2227ad486ef9391526529c90aa770fb0d2b4f0526ed5d6b84c0a
hotfix: false
---

# 2026-10-02 — go-live: first staging release (smoke failed on one probe)

The first `release` run of `release.yml` anywhere. Staging's host
(`i-0780af3b0bfdc3383`) had never run a container: its boot pull of the private
GHCR images got `unauthorized`. The `previous_*` refs above are what that boot
wrote into the env file; they never ran.

## What happened

- Provenance: both digests built from main `a8562dd`, CI green.
- Copy to ECR: both digests copied by crane and proved from ECR
  (`ok … -> 871376691052.dkr.ecr.us-east-1.amazonaws.com/…`).
- Host: `first start: the stack has never run on this host`, ECR login with
  the instance role, `BACKUP /data/backups/20261002T193406Z-37054858851
  none-first-start`, unit started. After the 30 s stability window, all five
  containers (compute-api, console, postgres, redis, site) were running with 0
  restarts. RELEASED reported the ECR refs of the two digests.
- Smoke: every probe passed except one. `/icon-v3.svg` serves
  `Cache-Control: public, max-age=3600`, and smoke wanted at least 86400. The
  Caddyfile sets the hour on purpose, so the smoke floor was the bug (#32).

No rollback. The host had passed its own checks, and with a first start there
is nothing to roll back to. No manifest was uploaded. The same digests were
re-released after #32; that run is
[2026-10-02-staging-first-release](2026-10-02-staging-first-release.md).
