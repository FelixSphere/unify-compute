---
environment: staging
action: release
outcome: success
run: 37151356901
commit: 5d065d00481c86271659673ceefd13e787c22611
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:e5966d7d11199c125b59a2e5b480cecbd840d6b2019d1be33ef8eb17907530a8
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:28991b2696a4dc00011d329e1a989ea6c5eaf6082cb08d63af958994f8bd0b0d
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:d5344254507f4a39eec78076e948d21c98d8446696650da393824006d98701da
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:ca42d6f6be43504b8156e14ee63bb2551366f8e0902a8dc9d1d92f0df4eb2204
hotfix: false
---

# 2026-10-03 — legal-entity on staging (#75, #76, #74)

Operator-approved ("合并，发布"). Production record:
[2026-10-03-production-legal-entity](2026-10-03-production-legal-entity.md).

## What shipped

Both images are built from main `5d065d0` (compute-api publish run 37150798801,
site 37151161062). Over `6c1d796`/`ec3fb28`: #75 (legal entity FelixSphere
LLC, the Malaysian address removed, footers "© {year} UnifyAI", the email footer
line), #76 (first-boot user-data writes the accounts, SES and Stripe settings;
both plans 0/0/0, nothing to apply) and #74 (auth card layout). No change
under `compute-api/config/` and no migration. No `.env` change on either host.

## Brand checks (rendered DOM, headless Chrome; staging and production identical)

- `/privacy`: "Unify Compute is a UnifyAI product operated by FelixSphere LLC,
  6 Karen Ct, CA 94010, United States." in the text, and the footer line
  "Operated by FelixSphere LLC · 6 Karen Ct, CA 94010, United States".
- Copyright on `/`, `/start`, `/privacy`: `<span class="copyright">© 2026
  UnifyAI</span>`, exactly that text.
- App pages (`/login`, `/signup`, `/verify`, `/forgot-password`): the line reads
  `© 2026 UnifyAI · Privacy · Terms`. The copyright is "© 2026 UnifyAI",
  followed by the two links in the same line.
- "Petaling Jaya", "Lebuh Bandar Utama", "Malaysia": absent from every page
  above, and from the compute-api binary on both hosts.
- Email: a staging signup to `success+legal@simulator.amazonses.com` got 202
  (sent through SES; simulator mail cannot be read back). The running binary on
  both hosts contains `render.go`'s footer constant "© UnifyAI · Operated by
  FelixSphere LLC · 6 Karen Ct, CA 94010, United States", which
  `mailer_test.go` asserts in every message.

## Host checks

healthz build 5d065d0, `CHECK_IMDS_OK`, and `admin audit` "balance invariant holds" on
both hosts. `make smoke`: staging 19 ok, production 16 ok. Production site
browser check: 44 ok, 0 FAIL.
