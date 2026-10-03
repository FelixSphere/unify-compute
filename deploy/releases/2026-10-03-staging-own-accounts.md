---
environment: staging
action: release
outcome: success
run: 37142120642
commit: 3871bb5d67dc37852a3c66bff27c6ff9951a6ccc
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:40e08796c20704762896ecef877a2039ce8e2de9524df443db94a366556e5a7f
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:48aa37ffeca00fcbd85889d3a8d7b780eeea3d15071606ab4bf7c9bc02736146
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:fdd7a67c1c72a94ae483df8eb4e1eab4ba3785288fb891c54506ff5ecac3fb34
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:6981df3526a829868b3360d938b3240217f0e9d19790c303505b0cdcbf587f63
hotfix: false
---

# 2026-10-03 — own-accounts: staging switches to Compute's own accounts (ADR-0011)

Operator-approved ("合并，发布"). It was held mid-round while the direction was
reconsidered, and resumed once the operator confirmed direction A (Compute keeps its own accounts).

## What shipped

Both images are built from main `3871bb5` (compute-api publish run 37141478846,
site 37141346510). Over `5f964c1`: ADR-0011 (#54), mailer (#55), accounts
and migration 0005 (#56), local biller and admin CLI with 0006 (#59),
dashboard auth (#60), app pages (#61, #62), `uck_` keys with 0007 (#63),
dashboard backend routes (#66), OpenAPI title (#64), site Log in / Sign up →
`/start` (#53), and #67 (compose passes the accounts and mail settings).
No change under `compute-api/config/`, so no price change.

## Applied before the release

- `shared/ses-compute` (`dmarc_report_email` = operator's address): **10
  added**. Identity `compute.unifyai.us` verified for sending, DKIM SUCCESS,
  MAIL FROM `bounce.compute.unifyai.us` SUCCESS, configuration set
  `unify-compute`. All within about a minute.
- `envs/staging`: **12 added, 2 changed, 1 destroyed** (plan said 5
  destroys). #57's SES policy and 3 SSM params, and #58's certificate (now
  with `app.staging`), validation records, app alias and `/dashboard/v1/*`
  rule. The instance was not touched.
  - The re-created validation records were left as **deposed objects with
    the same Route53 ids as the live records**. The next plan proposed
    destroying them, which would have deleted the live validation CNAMEs.
  - Fixed without touching DNS: `state rm` and `import` of the 3 records
    (live values checked against ACM first), then a state-only
    `allow_overwrite` update. The plan is now clean, with no deposed objects.
- SES policy test from the staging host (instance role, configuration set
  `unify-compute`, to `success@simulator.amazonses.com`): `no-reply@` bare and
  with the display name both returned message ids. `other@` got AccessDenied.
- **Found and fixed (#67):** the compose file did not pass
  `ACCOUNTS_BACKEND`, `APP_ORIGIN`, `ENVIRONMENT`, `SES_SENDER` or
  `SES_CONFIGURATION_SET` into compute-api. Setting them in `.env` alone
  would have changed nothing, silently.
- Host change over SSM, under the ops lock (previous files in
  `/data/backups/20261003T175222Z-manual-own-accounts-env/`): installed
  3871bb5's `docker-compose.yml`, and set `ACCOUNTS_BACKEND=local`,
  `APP_ORIGIN=https://app.staging.compute.unifyai.us`, `SES_SENDER='Unify
  Compute <no-reply@compute.unifyai.us>'`, `SES_CONFIGURATION_SET=unify-compute`
  and `ENVIRONMENT=staging`. Nothing was recreated by that step. The GitHub
  staging variable `APP_ORIGIN` is set.

## The release

Backup `/data/backups/20261003T175322Z-37142120642`, copied to S3. Smoke
passed, including the new app-host probes: `/` is the app, the CSP is
enforced, `/dashboard/v1/session` is compute-api's 404 JSON, and
`/compute/v1/skus` is not served there. The boot log shows `accounts_backend
local`, `mailer sends through SES` (from `"Unify Compute"
<no-reply@compute.unifyai.us>`, set `unify-compute`), `/dashboard/v1 served on
the app host`, and `ACCOUNTS_BACKEND=local: CONSOLE_URL is ignored`.
`schema_migrations`: 0001 to 0007.

## Account flow: what was exercised

1. **Pages** (`/`, `/login`, `/signup`, `/verify`, `/forgot-password`,
   `/keys`): 200, with the enforced CSP (`default-src 'none'; script-src
   'self'; …`), `Referrer-Policy: no-referrer`, COOP and CORP same-origin, and no
   cookie. Headless Chrome recorded **no third-party requests**. The only
   console error is the expected 401 from `/dashboard/v1/me` on the dashboard
   without a session.
2. **Signup through SES** to `success@simulator.amazonses.com`: 202
   `verification_sent` (about 210 ms, synchronous send). The same address again
   also gave 202 (the signup-attempt mail), with nothing revealing it already existed.
   The `unify-compute` configuration set now reports `Send`.
   **No 6-digit code appears anywhere in compute-api's log.**
3. A wrong `Origin` gets 403 `csrf_failed`. `/dashboard/v1/me` on the API host
   gets 404.
4. **`sk-` keys are refused** on staging: 401 `missing_key` ("… Bearer
   uck_…"). A made-up `uck_` key gets 401 `invalid_key`.
5. `admin audit`: "balance invariant holds".
6. `check-imds.sh`: `CHECK_IMDS_OK`. `make smoke` (with the console): 19 ok
   after the fix in this record's PR.

## Account flow: NOT exercised yet, and why

Login → reauth → create a `uck_` key → `403 compute_not_enabled`, then
`admin account enable-compute`, `admin balance credit --usd 5`, a submit
with its local hold and release, and #66's `/billing`, `/usage`,
`/jobs/{id}`. All of these need a **verified** account. Verification needs
the emailed 6-digit code, and no inbox this agent can read exists. The
admin CLI has no account-create verb, `balance credit` refuses unverified
accounts, and codes are stored salted and hashed. Reading codes from logs
or activating a user by SQL would bypass exactly what this release
introduces, so neither was done. The next step needs the operator (see the PR
and report).

## Also found

- The app pages link to `https://compute.unifyai.us/terms`, which is **404**
  on the marketing site.
- `scripts/smoke.sh` assumed the Unify API bridge for its "made-up key"
  check. It is now mode-aware: in local mode it also proves a made-up `uck_`
  key is `invalid_key`.

## What this does not prove

- That a real person receives and can use the code (the simulator only
  accepts the mail).
- Anything about the local biller on a real job.
