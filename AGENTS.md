# unify-compute — orientation for agents

Canonical conventions for every AI coding agent working here (Claude Code,
Cursor, Copilot, Codex). The agent-specific entry files (`CLAUDE.md`,
`.cursor/rules/agents.mdc`, `.github/copilot-instructions.md`) only point at
this file. Edit conventions here, nowhere else.

The structure mirrors `FelixSphere/unifyapi` on purpose: same entry files, same
release-owner rule, same ADR habit. An agent that has worked in one UnifyAI
repository should find nothing surprising in the other.

## Where you are

```
FelixSphere/unify-compute      private · proprietary · this repo — compute product
FelixSphere/unifyapi           private · proprietary · marketing site unifyapi.ai + AWS infra
FelixSphere/unifyapi-console   public  · AGPL-3.0    · fork of QuantumNous/new-api — accounts, keys, balance
FelixSphere/unifyai-gateway    private · proprietary · enterprise AI gateway (Bifrost)
FelixSphere/skypilot           public  · Apache-2.0  · fork of skypilot-org/skypilot — orchestration base
```

This repository holds three things, and they have different rules:

| Path | What it is | Rules |
|---|---|---|
| `app/`, `components/`, `public/` | Marketing site at **compute.unifyai.us** (vinext static export, served by the `deploy/site/` container on AWS production since 2026-10-02; `.openai/hosting.json` stays as the DNS rollback target) | Site conventions below |
| `compute-api/` | The compute service: authenticated job API in front of SkyPilot, with its own accounts, balance and card top-ups (ADR-0011) | Go; direct dependencies are pgx, `golang.org/x/crypto` and `golang.org/x/text`; permissive licences only |
| `deploy/`, `docs/` | Compose profiles, the AWS staging and production stacks, release records, ADRs and specs | Terraform validated in CI, never applied by CI |

## What is decided (do not re-litigate without a new ADR)

- **Compute owns its accounts, API keys, balance and payments**, as Unify
  Data does: its own sign-up, `uck_` keys, ledger, and Stripe Checkout
  top-ups on FelixSphere LLC's Stripe account. It supersedes ADR-0001
  (UnifyAPI owning accounts), which stays in `docs/adr/` as history.
  [ADR-0011](docs/adr/0011-compute-owns-accounts-and-billing.md)
- **The compute service is a separate, proprietary process** that talks to the
  AGPL console over HTTP only. No code, schema, or component crosses that line.
  [ADR-0002](docs/adr/0002-compute-service-is-separate-and-proprietary.md)
- **SkyPilot is the orchestration foundation**, consumed through the
  `FelixSphere/skypilot` fork pinned to a tested upstream release.
  [ADR-0003](docs/adr/0003-skypilot-orchestration-foundation.md)
- **Staging is one AWS stack in the UnifyAI account**, same shape as the
  gateway's hosted profile, at `staging.compute.unifyai.us`.
  [ADR-0004](docs/adr/0004-staging-environment.md)
- **Upstreams are SkyPilot clouds.** A SKU maps to an ordered provider list,
  its trust tier is a SKU attribute, and the deployment's `UPSTREAMS` says
  what it may launch into. No provider abstraction of our own.
  [ADR-0006](docs/adr/0006-upstreams-are-skypilot-clouds.md)
- **Templates are sold through a brokered marketplace**: UnifyAI is the
  seller of record, account listings are reviewed before they are served,
  and public pages show no prices.
  [ADR-0012](docs/adr/0012-a-brokered-template-marketplace.md)

Current status (say what exists rather than describing targets as shipped).
What each host runs right now is the newest record in `deploy/releases/`;
this list does not repeat image digests, because they go stale here first.

- **Staging and production both run on AWS** (ADR-0009, **Proposed**, but
  applied): one module, `deploy/aws/modules/compute-stack/`, with callers in
  `deploy/aws/envs/staging` (VPC 10.72/16, `i-06125c23285803d7a`,
  `staging.compute.unifyai.us`) and `deploy/aws/envs/production` (VPC
  10.71/16, `i-07dd2f6d313a0dafd`), plus `shared/ecr`, `shared/github-oidc`,
  `shared/ses-compute` and `shared/gpu-budget`. Both hosts are IMDS-locked
  (hop limit 1). The marketing site has been served from production's ALB
  since 2026-10-02 (Route53 change `C07235422LT740FCOOAEW`); the OpenAI Sites
  project and its TXT records are kept only as the DNS rollback path.
- **Both hosts run Compute's own accounts** (ADR-0011,
  `ACCOUNTS_BACKEND=local`). Since #76 first boot writes it, together with
  `APP_ORIGIN`, SES and Stripe settings, so a rebuilt host comes back the
  same. Sign-up and login are open at https://app.compute.unifyai.us. The job
  API takes `uck_` keys only; an `sk-` key from Unify API is
  `401 missing_key`. A new account cannot run jobs until an operator enables
  compute and credits it (D3), so a submit from a fresh account ends at
  `403 compute_not_enabled`. Card top-ups are live in production (Stripe
  Checkout, own webhook endpoint); staging has no Stripe keys.
- **The console bridge is retired from the request path.** The console's
  bridge half (`unifyapi-console` #225) is still deployed on app.unifyapi.ai,
  and production's `.env` still holds `CONSOLE_URL` and the bridge secret,
  ignored in local mode and kept for rollback. Staging's console container
  still runs but nothing calls it.
- `compute-api/` on `main`, with a job store (`DATABASE_URL`): quota, platform
  caps and a ledger hold at submit, launch on SkyPilot, GPU metering settled
  every 300 s, job events and outbound webhooks, get/list/cancel/logs/events/
  artifacts, resuming live jobs after a restart (#42), templates and the
  marketplace (M1–M3), and the session dashboard API under `/dashboard/v1`.
  It has run jobs only against the fakes in its tests
  (`internal/api/e2e_test.go`), never to completion on a real GPU. Without
  `SKYPILOT_API_SERVER_URL` submit is `503 upstream_unavailable`. Not built:
  customer webhook registration, storage and egress metering, applying Stripe
  refunds and disputes (the webhook only logs them; in progress as W3 of
  the 2026-10-14 UX wave).
- **No GPU has run a job yet.** GPU workers and the SkyPilot API server
  (ADR-0010, **Proposed**, applied on staging 2026-10-03: workers VPC
  10.73/16, launch role, reaper) run on staging only; production has the
  module behind `gpu_workers_enabled = false` and no SkyPilot. The account's
  G/VT vCPU quota was still 0 at the 2026-10-09 release, so a staging job
  fails `capacity_exhausted`; the request is open and "Start small" asks for 4.
- GPU spend is capped while there are no customers (ADR-0010, "Start small"):
  one GPU job across all accounts and a 4 h runtime ceiling
  (`compute-api/config/platform.json`), workers reaped at 5 h, and a USD 50
  monthly budget (`deploy/aws/shared/gpu-budget`, action
  `b4302f8f-c1df-40b6-8157-d71a203ae18a`) that denies new launches at 100%
  by itself (dry run proven 2026-10-03).
- Marketplace: M2 is released on both hosts (2026-10-14); on production
  account listings are closed (`ENVIRONMENT=production`, ADR-0012 §4). M3
  (the dashboard Marketplace and My listings pages) is merged, not released.

## Licence boundary — the expensive mistake

`unifyapi-console` is AGPL-3.0. This repository is proprietary. Copying a
handler, a GORM model, a migration, or a React component from the console into
`compute-api/` or `app/` makes this product a derivative work. That is a
rewrite, not a header cleanup.

What is allowed: calling the console's HTTP API. What is not: importing its Go
packages, vendoring its files, or "adapting" its code. The console-side half of
the bridge is written **in the console repo**, as new files there, under AGPL.

Every dependency added to `compute-api/` must be MIT, Apache-2.0, or BSD, and is
recorded in `THIRD-PARTY-LICENSES.md` in the same commit. SkyPilot is
Apache-2.0 and carries a NOTICE obligation that the packaging step must honour.

## Commands

Site (root):

```bash
npm ci
npm run dev            # vinext dev server
npm run lint           # oxlint — CI scopes this to app/ lib/ (see below)
npm run format         # oxfmt; app/ predates the config and is not yet clean, so CI does not check it
npm run build          # the vinext export (dist/client), then build:app
npm run build:app      # app-host pages only: plain Vite, dashboard/ -> dist/app
```

Service:

```bash
cd compute-api
go test ./...
go vet ./...
go build ./cmd/compute-api
```

Environment:

```bash
make compose-config    # validate deploy/compose against .env.example values
make tf-validate       # fmt -check + init -backend=false + validate on the staging stack
make smoke BASE_URL=https://staging.compute.unifyai.us
```

## Conventions

- Branch per task: `<type>/<short-description>`. Commits follow Conventional
  Commits (`feat:`, `fix:`, `docs:`, `chore:`, `refactor:`, `test:`), in
  English, and explain *why* — the next agent has none of your context.
- Decisions go in `docs/adr/NNNN-title.md` with a `Status:` line. Changing a
  decision is a new ADR that supersedes the old one; edit the old status, never
  its reasoning.
- Prose states what is true and marks what is not yet known. A doc that
  describes an unbuilt feature in the present tense will be believed.
- No comments explaining *what* code does. Comment only non-obvious *why*.
- Don't add abstractions, flags, or error handling for cases that cannot happen.
- Site copy must not promise pricing, availability, or capabilities that do not
  exist yet. "In development" stays until the CI/CD agent says otherwise.
- `components/ui/` and `hooks/` are generated shadcn output. Leave them
  alone; lint is scoped to `app/ lib/` for that reason.

## One agent, one worktree

Two agents must never edit the same checkout. Use `git worktree add` per task,
pick a port per agent for `npm run dev` and for `compute-api`, and say which
one you used when you hand off.

## Releases go to staging first

**Across UnifyAI, every change reaches production through staging** — not just
significant ones. Adopted 16 Sep 2026, after twelve consecutive console releases
went straight to production unverified.

For unify-compute both stacks exist, and `release.yml` refuses a
production release of digests that have not run on staging first.

When you hand a change over, say:

- the merged commit or tag, and the delta in a sentence per commit;
- **what to exercise** — the specific behaviour your change alters, not "verify
  it works". A change that deletes data gets its deletion run, not read about;
- anything that must happen before the cutover: a migration, a seed, a
  parameter, an env var, an ordering constraint with another agent's change.

**The hotfix exception** is narrow: a live outage, a security issue, or data
being lost. Then it ships, marked `HOTFIX`, with what was skipped recorded
afterwards. "My feature is done and I want it live" is not a hotfix; neither is
a release someone is waiting on, nor a fix for a bug that is not currently
hurting anyone. If staging is merely inconvenient, that is not the exception —
that is the rule working.

The rule across all UnifyAI products, and the table of where staging actually
exists, is in `~/unifyai/AGENTS.md`.

## Who may deploy

**Only the unify-compute CI/CD agent releases**, to staging and to production.
Any agent may take work through review, get it green, and merge it. Merging is
not releasing. When your change is on `main`, say so, name what is waiting to
ship, and notify the CI/CD agent. The procedure is `.claude/agents/cicd.md`.

The one exception is a genuine hotfix (live outage, security issue, data being
lost). Then ship it, mark it `HOTFIX`, and tell the CI/CD agent immediately.
Wanting a feature live is not a hotfix.

Terraform is validated in CI and applied by a person or the CI/CD agent from a
machine with credentials; state is in S3 with a lockfile. CI never applies
Terraform. `release.yml` assumes a GitHub OIDC role
(`deploy/aws/shared/github-oidc`) that can push to ECR and send the SSM
cutover command to the tagged instances, nothing more; widening it is a
reviewed change.

## Before you hand off

```bash
npx oxlint app lib && npm run build
(cd compute-api && go vet ./... && go test ./...)
make compose-config tf-validate tf-test
```

Anything you could not finish, say so explicitly and say what is left. A
partial change reported as done is worse than an unfinished one.
