# unify-compute

On-demand inference and GPU compute for everything beyond chat: batch
processing, embeddings, document pipelines, fine-tuning. Consumption billing,
through the customer's existing UnifyAPI account.

> **Status:** the marketing site at https://compute.unifyai.us is live on AWS
> (production, since 2026-10-02). The compute service is a skeleton, and it
> runs on staging and, dark, on production. The console bridge is a spec. `docs/architecture.md` says what exists.

## What is here

| Path | |
|---|---|
| `app/` | Marketing site (vinext static export). Served on AWS by the `site` image in `deploy/site/` (OpenAI Sites kept as the DNS rollback target) |
| `compute-api/` | The compute service: job API in front of SkyPilot, billed through UnifyAPI (Go) |
| `deploy/compose/` | compute-api + site + Postgres; `docker-compose.console.yml` adds console + Redis for staging and local |
| `deploy/aws/` | Terraform: `modules/compute-stack`, `envs/staging`, `envs/production`, `shared/github-oidc`, `shared/ecr`. All applied 2026-10-02 ([ADR-0009](docs/adr/0009-host-everything-on-aws.md), proposed) |
| `docs/architecture.md` | One account plane (UnifyAPI), two execution planes (tokens, compute) |
| `docs/adr/` | Decisions: reuse UnifyAPI accounts, separate proprietary service, SkyPilot, staging shape, upstreams |
| `docs/spec/job-api.md` | The customer-facing job API contract |
| `docs/spec/upstreams.md` | Requirements for buying capacity from other platforms and reselling it under our SKUs |
| `docs/spec/console-billing-bridge.md` | The console ↔ compute contract and the console change list |
| `docs/staging.md` | Staging runbook and acceptance criteria |
| `research/` | The SkyPilot selection research |

## Working here

Start with [AGENTS.md](AGENTS.md); it is the canonical guide for humans and
agents alike. Then:

```bash
npm ci && npm run dev              # site on :3000
make dev                           # compute-api on 127.0.0.1:8080
make smoke                         # probe it
make verify                        # everything CI runs that does not need Docker
```

## Deployment

Merging does not deploy. See "Who may deploy" in AGENTS.md.
