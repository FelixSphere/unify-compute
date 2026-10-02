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
| `app/`, `components/`, `public/` | Marketing site at **compute.unifyai.us** (vinext static export; still served by OpenAI Sites via `.openai/hosting.json` until the DNS cutover to the `deploy/site/` container on AWS) | Site conventions below |
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

- The marketing site is live. Nothing else is deployed yet.
- `compute-api/` is a skeleton: health, SKU catalogue, and the job API routes
  with the contract from `docs/spec/job-api.md`. Job submission returns
  `501 not_implemented` until the SkyPilot backend and the console bridge land.
- The console bridge in `docs/spec/console-billing-bridge.md` is a spec. The
  console fork does not implement it yet.
- The AWS Terraform — one module (`deploy/aws/modules/compute-stack/`) with
  staging and production callers under `deploy/aws/envs/`, plus GitHub OIDC
  deploy roles in `deploy/aws/shared/github-oidc/` — validates in CI and has
  **not been applied**. Moving the site and everything else onto it is
  [ADR-0009](docs/adr/0009-host-everything-on-aws.md), **Proposed**. No DNS
  record for any `*.compute.unifyai.us` stack hostname exists, and
  `compute.unifyai.us` is still OpenAI Sites.

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
npm run build
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
make compose-config tf-validate
```

Anything you could not finish, say so explicitly and say what is left. A
partial change reported as done is worse than an unfinished one.
