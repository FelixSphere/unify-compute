---
environment: production
action: release
outcome: success
run: 37142414668
commit: 3871bb5d67dc37852a3c66bff27c6ff9951a6ccc
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:40e08796c20704762896ecef877a2039ce8e2de9524df443db94a366556e5a7f
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:48aa37ffeca00fcbd85889d3a8d7b780eeea3d15071606ab4bf7c9bc02736146
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fdd7a67c1c72a94ae483df8eb4e1eab4ba3785288fb891c54506ff5ecac3fb34
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:6981df3526a829868b3360d938b3240217f0e9d19790c303505b0cdcbf587f63
hotfix: false
---

# 2026-10-03 — own-accounts on production: site changes only; accounts stay on Unify API

Same digests as staging run 37142120642 (gate passed). What shipped is in
[2026-10-03-staging-own-accounts](2026-10-03-staging-own-accounts.md).
**`ACCOUNTS_BACKEND` is not set on production** (default `console`), so keyed
routes still go through the Unify API bridge. The production host's compose
file predates #67 and does not even pass the variable. A rebuild, or the
same SSM step as staging, is needed before production can ever switch.

## Applied before the release

`envs/production`: **12 added, 2 changed, 1 destroyed**. SES policy, SSM
params, the certificate with `app.compute.unifyai.us`, validation records,
`app.compute.unifyai.us` alias and `/dashboard/v1/*` rule. The
`compute.unifyai.us` A alias was untouched (still the ALB). The same
deposed-validation-record leftovers appeared as on staging and got the same
fix (state rm + import, live values checked first). The plan is now clean.

## What was exercised

- `https://api.compute.unifyai.us/healthz`: build 3871bb5.
- A made-up `sk-` key: 401 `invalid_key` from the Unify API bridge, unchanged.
  A `uck_` key: 401 ("Unify Compute API keys (uck_) are not accepted by this
  deployment yet"). No key: 401. `/skus`, `/templates`, `/openapi.json`: 200.
  `make smoke` against the API host: 14 ok.
- Site: title, v4 icons byte-identical to the brand kit, HubSpot loader once,
  `/privacy` 200, HSTS once. The browser check is clean. **Log in / Sign up** (6
  links) point to `/start#early-access`, and `/start` gives 200 "Get started — Unify
  Compute". Nothing on `/` or `/start` links to `app.compute.unifyai.us`.
- `app.compute.unifyai.us`: `/login` 200 (the new pages), `/dashboard/v1/me`
  404 text/plain. Production is on console accounts, so the forms cannot
  complete. The GitHub production variable `APP_ORIGIN` is deliberately unset.
- `check-imds.sh`: `CHECK_IMDS_OK`.
