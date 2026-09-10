# ADR-0004 — Staging is one AWS stack with its own console

- **Status:** Accepted
- **Date:** 2026-09-10

## Context

Compute needs somewhere to develop the console bridge and the SkyPilot backend
end to end before either touches production. The marketing site at
`compute.unifyai.us` is hosted on OpenAI Sites and has no backend; UnifyAPI's
production console at `app.unifyapi.ai` is live with real customer balances and
belongs to another release owner.

The gateway repository already has a working shape for a small hosted
environment in the UnifyAI AWS account: one Graviton instance behind an ALB,
Docker Compose from the repository, secrets in SSM, an EBS data volume with
`prevent_destroy`, SSM instead of SSH, Route53 records in the `unifyai.us`
zone.

## Decision

Staging is that shape, under `deploy/aws/staging/`:

- Hostnames `staging.compute.unifyai.us` (compute-api) and
  `console.staging.compute.unifyai.us` (staging console), both children of the
  existing `compute.unifyai.us` name. The apex record stays pointed at OpenAI
  Sites.
- One `t4g.small` running `deploy/compose/docker-compose.yml`: console
  (pinned fork image), Postgres, Redis, compute-api. SkyPilot's API server is a
  compose profile, enabled once the backend exists.
- **Its own console with an empty database.** Staging never points at the
  production console. The bridge endpoints do not exist there yet, and a
  compute bug that places holds against real balances is a production
  incident, not a staging one.
- Terraform is validated in CI (`fmt`, `init -backend=false`, `validate`) and
  applied by a person or the CI/CD agent from a machine with credentials. CI
  has no AWS credentials.
- One environment, not per-branch stacks. Agents share it; the CI/CD agent
  decides what is running on it.

## Consequences

- Staging costs roughly the gateway's hosted profile: ~USD 15–25/month for the
  instance and ALB. GPU spend from SkyPilot is separate and only starts when a
  provider is configured.
- The staging console needs its own admin bootstrap and its own bridge secret.
  Seeding steps are in `docs/staging.md`.
- Terraform state is local until remote state with locking exists. `ci.yml`
  rejects committed state files.
- The stack has **not been applied** as of this ADR. The first apply is a
  deliberate act recorded in `.claude/agents/cicd.md`.
