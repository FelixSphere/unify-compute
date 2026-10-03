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
| `compute-api/` | The compute service: authenticated job API in front of SkyPilot, billed through UnifyAPI | Go, stdlib-only so far, permissive licences only |
| `deploy/`, `docs/` | Compose profile, AWS staging stack, ADRs and specs | Terraform validated in CI, never applied by CI |

## What is decided (do not re-litigate without a new ADR)

- **UnifyAPI owns accounts, API keys, and the balance.** Compute does not get
  its own users or wallet. One key, one balance, two kinds of spend.
  [ADR-0001](docs/adr/0001-unifyapi-owns-accounts-and-billing.md)
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

Current status (say what exists rather than describing targets as shipped):

- The marketing site is live at https://compute.unifyai.us on AWS
  production. Staging and production both run (see below).
- **Production runs Compute's own accounts (C1, 2026-10-03).** Sign-up and
  login are open at https://app.compute.unifyai.us, and the site's Log in /
  Sign up link there. The job API takes `uck_` keys only (`sk-` keys from
  Unify API are `401 missing_key`). New accounts cannot run jobs until an
  operator enables compute and credits a balance (D3), and GPUs are off in
  production (no SkyPilot), so a submit ends at `403 compute_not_enabled` or
  `503`. Card top-ups are live on Unify API's Stripe account (live key, own
  webhook endpoint). No top-up has run yet.
- `compute-api/` on `main` runs jobs end to end where it has a job store
  (`DATABASE_URL`): keys verified through the console bridge, quota and a
  console hold at submit, launch on SkyPilot, GPU metering settled every
  300 s, job events and webhooks, get/list/cancel/logs/events/artifacts
  (`internal/jobs`). It has run only against the fakes in its tests
  (`internal/api/e2e_test.go`), never on a real cloud or against the real
  console. Without a job store it is the old dark build (`501` after
  validation); without `CONSOLE_URL` every keyed job route is
  `503 console_unavailable`; without `SKYPILOT_API_SERVER_URL` submit is
  `503 upstream_unavailable`. Not built: resuming live jobs after a restart
  (`docs/spec/backend.md` open question 2), webhook registration, storage
  and egress metering.
- GPU spend is capped while there are no customers (ADR-0010, "Start small"):
  one GPU job across all accounts and a 4 h runtime ceiling
  (`compute-api/config/platform.json`), workers reaped at 5 h, and a USD 50
  monthly budget whose action denies new launches at 100%
  (`deploy/aws/shared/gpu-budget`, applied 2026-10-03: action `b4302f8f-c1df-40b6-8157-d71a203ae18a`, STANDBY, automatic, on `unify-compute-staging-skypilot`; alerts to the operator's address, pending its email confirmation). Both
  hosts run compute-api 5f964c1 with these caps. The G-instance quota to
  request is 4 vCPU; it is 0 today, so nothing can launch.
- The console bridge's server half (`unifyapi-console` #225) is live on
  production `app.unifyapi.ai` since 2026-10-03 (v0.1.144, which lacks
  console #233 and #243). Production compute-api calls it, so **production
  compute is connected to the Unify API account system**: an `sk-` key from
  app.unifyapi.ai is verified there, and a made-up one is `401 invalid_key`.
  GPUs are still off in production, so a valid submit ends at
  `503 upstream_unavailable` before any hold. Record:
  `deploy/releases/2026-10-03-production-console-bridge.md`.
- The AWS Terraform — one module (`deploy/aws/modules/compute-stack/`) with
  staging and production callers under `deploy/aws/envs/`, plus GitHub OIDC
  deploy roles in `deploy/aws/shared/github-oidc/` and the runtime image
  registry (ECR) in `deploy/aws/shared/ecr/`. Moving the site and
  everything else onto it is
  [ADR-0009](docs/adr/0009-host-everything-on-aws.md), **Proposed**.
  Applied on 2026-10-02: `shared/ecr`, `shared/github-oidc`, staging (VPC
  10.72/16, instance `i-06125c23285803d7a`) and production (VPC 10.71/16,
  instance `i-07dd2f6d313a0dafd`). Both run compute-api `d5344254…` (6c1d796) and site `ca42d6f6…`
  (ec3fb28); records are in `deploy/releases/`. **Both run Compute's own
  accounts** (ADR-0011, `ACCOUNTS_BACKEND=local`, set in each host's `.env`
  over SSM; user-data does not render it, so a rebuild would come back on
  console accounts). Staging has no Stripe keys. Staging also runs SkyPilot and artifact storage. Its console container
  (UnifyAPI trial build `e3cef17`) still runs, but compute-api no longer
  calls it. Production's `.env` still holds
  `CONSOLE_URL` and the bridge secret, ignored in local mode and kept for
  rollback. Production has no SkyPilot. Both hosts are IMDS-locked (hop limit 1). `compute.unifyai.us` was cut over
  from OpenAI Sites to production's ALB on 2026-10-02 (Route53 change
  `C07235422LT740FCOOAEW`). The Sites project and its two TXT records remain
  as the rollback path.
- GPU workers and the SkyPilot API server
  ([ADR-0010](docs/adr/0010-gpu-workers-in-the-shared-account.md),
  **Proposed**): `deploy/aws/modules/gpu-workers/`, called by staging, and
  `deploy/compose/docker-compose.skypilot.yml`. **Applied on staging
  2026-10-03**: workers VPC 10.73/16, launch role, reaper, and SkyPilot on the
  host. A staging job reaches SkyPilot and fails with `capacity_exhausted`,
  because the account's G/VT quota is 0 vCPU (AWS: `VcpuLimitExceeded`). The
  request for 8 is open (`d81c8fcd…`); 4 is what "Start small" asks for.
  Production has the module behind `gpu_workers_enabled = false`. The GPU
  budget (`deploy/aws/shared/gpu-budget`) is applied: USD 50/month, and it stops
  new launches by itself at 100% (dry run proven on 2026-10-03).

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

For unify-compute the rule is not yet load-bearing: the staging stack under
`deploy/aws/envs/staging/` is **defined and CI-validated but has never been applied**,
and nothing is in production either. When that changes, staging comes first —
do not let the first production deployment also be the first time the stack runs.

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
machine with credentials. CI has no AWS credentials and must stay that way until
remote state with locking exists.

## Before you hand off

```bash
npx oxlint app lib && npm run build
(cd compute-api && go vet ./... && go test ./...)
make compose-config tf-validate tf-test
```

Anything you could not finish, say so explicitly and say what is left. A
partial change reported as done is worse than an unfinished one.
