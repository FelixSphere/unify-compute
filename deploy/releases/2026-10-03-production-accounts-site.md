---
environment: production
action: release
outcome: success
run: 37145795756
commit: ec3fb28d3859d9163ced3d2aee82c08ef4b73f2c
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:d5344254507f4a39eec78076e948d21c98d8446696650da393824006d98701da
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:ca42d6f6be43504b8156e14ee63bb2551366f8e0902a8dc9d1d92f0df4eb2204
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:d5344254507f4a39eec78076e948d21c98d8446696650da393824006d98701da
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:48aa37ffeca00fcbd85889d3a8d7b780eeea3d15071606ab4bf7c9bc02736146
hotfix: false
---

# 2026-10-03 — production site: Log in / Sign up → app.compute.unifyai.us (C4)

Released after production's accounts were live and checked
([2026-10-03-production-accounts-live](2026-10-03-production-accounts-live.md)).
Same digests as staging run 37145649225 (gate passed). Smoke and
`served-by-aws.sh` passed.

`node deploy/site/browser-check.mjs https://compute.unifyai.us`: **42 ok, 0 FAIL**.

- At 1280 px: the header Log in → `/login`, header Sign up → `/signup`, hero Sign up
  → `/signup`, footer Log in and Sign up.
- At 375 px: Sign up collapses into the menu, the menu has Log in and an amber
  primary Sign up, and there is no horizontal scroll.
- `/start` step 02 is "Create an account", linking to sign-up. It mentions `uck_`,
  `403 compute_not_enabled`, `503 upstream_unavailable` and `read -rs
  UNIFY_KEY`, and no longer says accounts are closed.
- The footer Terms link goes to `https://felixsphere.com/legal/terms`, and there
  is no `/terms` 404 link.
- HubSpot loads once, and there are no console errors or failed requests.

`app.compute.unifyai.us/login` and `/signup` return 200 (live since the accounts
release). The privacy page names FelixSphere LLC and links to
felixsphere.com, as #68 intends.
