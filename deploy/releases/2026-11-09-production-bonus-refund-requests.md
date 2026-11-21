---
environment: production
action: release
outcome: success
run: 42679416679
commit: 792b4ea4008ef24be7cd4b5716972c5cb10abd69
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:15cf14fb57d9a6a12e3483a07865463e33ba4944d49cc8c9225ef1510835c56f
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:a8281c7835a2c2b4646e838f1d6b4882342476eb8c3eda1143a481dc56ee3b9c
previous_compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:b7474c942012f43d362265171e480c24f4d93a0d085640916f32b2d187bdf042
previous_site_image: ghcr.io/felixsphere/unify-compute-site@sha256:ed5de01aba3f5695882a8f56bad0a1eaed8583d19585cb76dcb088307e77dca4
hotfix: false
---

# 2026-11-09: top-up bonus credit and refund requests (#134, ADR-0013), on production

Same digests as staging (`2026-11-09-staging-bonus-refund-requests.md`, run
42678010483, where every staging check passed). The staging-first gate
passed. This is the operator's approval, given knowing that Stripe test mode
was never run against this code and that the bonus/refund wording has had
no legal review (O5).

**From now on, live Checkout charges for USD 50 or more grant bonus credit**
(3% at USD 50, 5% at USD 100, the current maximum top-up). Customers can
ask for a refund from Billing; each request emails the operator, who
refunds with `admin topup refund` or declines with
`admin refund-request decline`.

## What shipped

See the staging record. compute-api `b7474c94…` → `15cf14fb…`, site
`ed5de01a…` → `a8281c78…`, both from `792b4ea`. Migration `0015`
(additive). No change to `skus.json`, `platform.json`, env vars or
Terraform.

## Before / after

- Before (status run 42679307293): `b7474c94…` / `ed5de01a…`, matching
  the sglang-templates record. IMDS lockdown present. Schema `0014`.
  **Production had no top-ups at all** (the `topups` table is empty) and no
  unsent payment emails.
- Backup `/data/backups/20261109T190248Z-42679416679` (compute, 29 tables),
  copied to
  `s3://unify-compute-production-backups-871376691052/production/releases/20261109T190248Z-42679416679/`.
- compute-api, postgres and site had 0 restarts after `up -d` and after
  30 s. Smoke passed in full, including `compute.unifyai.us` → ALB and HSTS.
  `/healthz` reports build `792b4ea…`.
- `0015_bonus_and_refund_requests` applied at 19:02:51Z. PREVIOUS_RELEASE is
  `b7474c94…` / `ed5de01a…`.

## What was exercised

1. **Schema, admin, top-ups (pass, with nothing to list).** Newest
   migration `0015`. `balance_entries_kind_check` includes `topup_bonus`.
   `refund_requests` has 0 rows, and `admin refund-request list` printed
   `no open refund requests`. The only compute-api log line since boot,
   apart from ordinary INFO, is `stripe top-ups enabled livemode=true`: no
   ERROR or WARN. "Existing top-ups still listed with bonus —" **could not
   be checked**: there are none. The served `app.compute.unifyai.us/billing`
   carries `#bonus-note`, `#amount_usd-credit` and `#refund-dialog`.
2. **Checkout session for USD 50: not done.** `POST /dashboard/v1/topups` is
   the only way to create one, and it needs a signed-in owner session on
   production. I have none. Creating one would mean a new production
   account, and a live-mode Stripe Customer created by CreateTopup. The
   line-item text was not seen on Stripe. In tests (against `stripetest`),
   `BonusFor(5000) = 150` and a USD 100 session's name is "Unify Compute
   credit, includes USD 5.00 bonus" (`bonus_requests_test.go`).
3. **Webhook endpoint (pass, idle).** All read-only; Stripe was queried
   from the host, so the key never left it.
   - Stripe: `we_1UMY6T08R23kVnSzvjZa1tSI` →
     `https://app.compute.unifyai.us/dashboard/v1/stripe/webhook`,
     `enabled`, livemode, seven events (the three Checkout completions and
     failures, `checkout.session.expired`, `charge.refunded`,
     `charge.dispute.created` and `.closed`). No event since 16:00Z, and the
     newest events before that have `pending_webhooks=0`.
   - compute-api has logged no `stripe webhook` line in 7 days or since the
     release. ALB access logs for 18:00–19:10Z have no request to the
     webhook path.
   - ALB: 0 target 5xx and 0 ELB 5xx since 18:30Z. The 123 target 4xx at
     19:00Z, before the cutover, are a scanner (34.22.146.232) probing
     `/.git`, `/.aws` and similar on `api.compute`. A similar burst came at
     15:15Z. After the cutover the only 4xx is smoke's own
     `/dashboard/v1/session` 404.

## Rollback

`mode=rollback` goes back to `b7474c94…` / `ed5de01a…` without restoring the
database: 0015 only adds, and the old build never reads the new columns.
It is clean only while no top-up with a bonus exists. After the first such
payment, the old build would not reverse that bonus on a refund or dispute,
nor credit it on a late webhook. Check
`SELECT count(*) FROM topups WHERE bonus_cents > 0` first.

## What this does not prove

- Nothing has gone through Stripe with this code, in test or live mode. The
  first real USD ≥ 50 payment is the first time the bonus credit, its
  `topup_bonus` entry and the line item meet Stripe. Watch the first
  `topup_credited` audit row (`bonus_usd`) and the balance after it.
- No refund request, decline or bonus reversal has run in production.
- Legal review of the bonus/refund wording (O5) is outstanding.
