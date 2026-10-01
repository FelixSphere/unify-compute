# Staging

**Status: defined, not applied.** Nothing below is running. This page is the
procedure and the acceptance criteria; the first apply is the CI/CD agent's
call (`.claude/agents/cicd.md`).

## Hostnames

| Host | Serves | Routed by |
|---|---|---|
| `staging.compute.unifyai.us` | compute-api (`/compute/v1/*`, `/healthz`) | ALB host rule → port 8080 |
| `console.staging.compute.unifyai.us` | staging UnifyAPI console (SPA, `/api`, `/v1`) | ALB host rule → port 3000 |

Both are children of `compute.unifyai.us`, which stays a CNAME to OpenAI Sites
for the marketing page. The certificate covers both names.

## What runs on the instance

`deploy/compose/docker-compose.yml`, identical to the local profile, with the
instance's `.env` rendered from SSM at boot:

- `console` — `ghcr.io/felixsphere/unifyapi-console` at the tag in
  `CONSOLE_IMAGE`. Own Postgres database, own admin, own bridge secret.
- `postgres`, `redis` — for the console. compute-api gets its own database on
  the same Postgres.
- `compute-api` — `COMPUTE_API_IMAGE`, published from this repository.
- `skypilot` — compose profile, off by default until the backend exists.

## First-time apply

Done by a person or the CI/CD agent with credentials for the UnifyAI AWS
account. Terraform 1.9.x.

```bash
# 1. secrets — generate with: openssl rand -base64 32
aws ssm put-parameter --type SecureString --name /unify-compute-staging/postgres_password --value …
aws ssm put-parameter --type SecureString --name /unify-compute-staging/redis_password    --value …
aws ssm put-parameter --type SecureString --name /unify-compute-staging/session_secret    --value …
aws ssm put-parameter --type SecureString --name /unify-compute-staging/bridge_secret     --value …

# 2. plan, read, apply
cd deploy/aws/staging
terraform init
terraform plan      # expect: VPC, 2 subnets, ALB + 2 target groups + 2 rules, ACM cert, 3 Route53 records, 1 instance, 1 EBS volume
terraform apply

# 3. verify from outside
cd ../../..
make smoke BASE_URL=https://staging.compute.unifyai.us
```

## After apply: seed the staging console

1. Open `https://console.staging.compute.unifyai.us`, create the root account.
2. Create one test user and one `sk-` key for it, top it up with test quota in
   the admin UI.
3. Set the same `COMPUTE_BRIDGE_SECRET` value in the console's environment
   (compose `.env`) once the console ships the bridge routes. Until then the
   routes are dark and compute-api's submit returns `503 console_unavailable`.

## Acceptance

Staging is healthy when all of these hold, checked by `scripts/smoke.sh`:

- `GET /healthz` on the compute host returns `200` and a JSON body naming the
  build.
- `GET /compute/v1/skus` returns at least one SKU with a price.
- `POST /compute/v1/jobs` without a key returns `401`, not `5xx`.
- `GET /api/status` on the console host returns `"success": true`.
- No path on the compute host reaches the console, and vice versa.

## Releasing to staging

Image swap over SSM, same as the console's production procedure: `docker pull`
the new digest first, edit the image line in `/opt/unify-compute/docker-compose.yml`,
`docker compose up -d compute-api`, smoke, then commit the pin. Terraform is
for the instance, not for releases.
