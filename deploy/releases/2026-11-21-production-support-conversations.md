---
environment: production
action: release
outcome: success
run: 44957938798
commit: fc8c0e77e9057d2c7badff29fc5875dafe2c3653
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:577de6b8515cd3ef3778fdb37959df49baf9b4f4ee58ea59bae1a66ce30b0988
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:297a79748d5e7d09642a3e664c1416e10453b4208510292e4e4c4f76aa660ac7
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:15cf14fb57d9a6a12e3483a07865463e33ba4944d49cc8c9225ef1510835c56f
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:b31260dab45bb555c0dd2323c4ec175f6aa4654bd22fb34ec5883db2e618071d
hotfix: false
---

# 2026-11-21: support conversations (#159, ADR-0014), on production

Same digests as staging (`2026-11-21-staging-support-conversations.md`,
run 44957025547). The staging-first gate passed. Both images are built from
`72a4d30`; the workflow ran from `fc8c0e7` (main, which adds only the
staging record).

## What shipped

See the staging record. compute-api `15cf14fb…` (792b4ea) → `577de6b8…`
(72a4d30), site `b31260da…` (47f54d8) → `297a7974…` (72a4d30). Migration
`0016_support` (two new tables, additive). No change to `skus.json`,
`platform.json`, compose, Terraform, env vars or SSM parameters.

## Staging exercise (by the operator, before this release)

On the staging host at about 22:55Z, all measured: a customer session
created a conversation (201), a second start got 409 and a blank message
400. `admin support list --json` showed it with no message text or email.
`admin support reply --stdin` printed "replied … message 2"; SES accepted
the reply email, the state went to answered, and the conversation left the
waiting list. The customer GET showed both messages, the reply byte-for-byte
(including `$HOME`, backticks, quotes and Chinese). A customer follow-up
reopened it, customer close returned 200, and a reply to a closed
conversation was refused. The audit row `support_replied` was written,
there were 0 ERROR lines, and the fixture rows were deleted afterwards.
Not observed: the email in a real inbox (the recipient was the SES
simulator), and `/support` in a browser on staging.

## Before / after

- Before (status run 44957852556): `15cf14fb…` / `b31260da…`, matching the
  2026-11-11 record. IMDS lockdown present. Schema 0015, 0 restarts, 0
  ERROR lines, `stripe top-ups enabled livemode=true`,
  `topups` with `bonus_cents > 0`: 0.
- Backup `/data/backups/20261121T224107Z-44957938798` (compute, 30 tables),
  copied to
  `s3://unify-compute-production-backups-871376691052/production/releases/20261121T224107Z-44957938798/`.
  The cutover pruned `/data/backups/20261014T133217Z-38572473686`.
- compute-api and site recreated at 22:41:10Z; postgres untouched (up since
  2026-10-02). Every container had 0 restarts after `up -d`, after 30 s, and
  again at about 22:46Z.
- PREVIOUS_RELEASE is now `15cf14fb…` / `b31260da…`.

## What was exercised

Nothing was written on production: no account, conversation or message was
created and no email was sent.

1. **Release smoke (in the run):** all ok. healthz reports build
   `72a4d30…`; skus 200 and priced; jobs without a key 401 `missing_key`;
   site `/`, `/privacy`, icon cache; app host `/` with CSP enforced, the
   session route reaches compute-api; `compute.unifyai.us` resolves to the
   ALB, serves the site container, carries HSTS.
2. **`make smoke BASE_URL=https://api.compute.unifyai.us`** from outside:
   all ok (made-up `sk-` and `uck_` keys 401, no console `/api/status`).
3. **App pages** on `https://app.compute.unifyai.us`: the 17 pages of
   `deploy/site/smoke.sh`, including `/support` ("Support — Unify
   Compute"), checked for status 200, title, the page script, and no
   inline script or style. All ok. `compute.unifyai.us/` and `/zh` 200.
4. **Anonymous `GET /dashboard/v1/support/conversations`:** 401
   `application/json`, `session_required`.
5. **On the host over SSM (read-only):**
   - compute-api and site run the ECR refs of the two digests above. The
     `.env` and `CURRENT_RELEASE` name the same refs.
   - `schema_migrations` has `0016_support`, applied at 22:41:10Z.
     `support_conversations` and `support_messages` both exist, with 0
     rows each.
   - `admin support list` exits 0 ("no conversation is waiting").
   - compute-api logs since boot: 34 INFO, 3 WARN, 0 ERROR. The three
     WARNs predate this release: CONSOLE_URL is ignored in local mode,
     artifacts are unset, and SKYPILOT_API_SERVER_URL is unset.
   - Existing behaviour: `stripe top-ups enabled livemode=true
     environment=production`, local accounts on the app host, the SES
     mailer configured, `listening` with 4 SKUs and 7 templates, the same
     as before the release. The `/login` page is served (3).

## What this does not prove

- The support flow itself was not run on production. The operator ran it
  on staging, with the same images.
- **`OPERATOR_EMAIL` is not set on production**: it is absent from the
  host's `.env` and from the container's environment, so a customer's
  message sends no operator notice. A conversation waits until someone runs
  `admin support list` on the host. Setting it is a config change for the
  operator to decide (back up `.env`, set it under the ops lock, recreate
  compute-api). The Slack bridge (`unifyai-workspace`) is not built.
- `/support` was checked as a served page, not rendered in a browser with a
  session.
- Stripe was checked through its boot line only. No webhook arrived during
  the check, and none was sent.

## Rolling back

`mode=rollback` returns to `15cf14fb…` / `b31260da…`. That image ignores
the 0016 tables, so no dump restore is needed (and none for 0015 while
`topups` with `bonus_cents > 0` is 0). Any conversations written in the
meantime stay in the tables, unused.
