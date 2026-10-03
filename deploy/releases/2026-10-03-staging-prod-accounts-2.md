---
environment: staging
action: release
outcome: success
run: 37145649225
commit: ec3fb28d3859d9163ced3d2aee82c08ef4b73f2c
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:d5344254507f4a39eec78076e948d21c98d8446696650da393824006d98701da
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:ca42d6f6be43504b8156e14ee63bb2551366f8e0902a8dc9d1d92f0df4eb2204
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:d5344254507f4a39eec78076e948d21c98d8446696650da393824006d98701da
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:48aa37ffeca00fcbd85889d3a8d7b780eeea3d15071606ab4bf7c9bc02736146
hotfix: false
---

# 2026-10-03 — prod-accounts-2 on staging: site Log in / Sign up → app (C4)

Site `ca42d6f6…` from main `ec3fb28` (#72, publish run 37144802906). It is a
site-only change. Log in / Sign up now point to
`https://app.compute.unifyai.us/login` and `/signup`, and `/start` step 02 is
"Create an account". The marketing image is shared, so staging's site links
to **production's** app as well; that is expected. compute-api is unchanged
(`d5344254…`). Smoke passed.
