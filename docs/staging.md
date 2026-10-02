# Staging

**Status: defined, not applied.** Nothing below is running. This page is the
procedure and the acceptance criteria; the first apply is the CI/CD agent's
call (`.claude/agents/cicd.md`).

Staging is `deploy/aws/envs/staging/`, a caller of the module in
`deploy/aws/modules/compute-stack/` that production
(`deploy/aws/envs/production/`, also not applied) shares — see
[ADR-0009](adr/0009-host-everything-on-aws.md), Proposed. Staging is applied
first, always.

## Hostnames

| Host | Serves | Routed by |
|---|---|---|
| `staging.compute.unifyai.us` | compute-api (`/compute/v1/*`, `/healthz`) | ALB host rule → port 8080 |
| `site.staging.compute.unifyai.us` | marketing site container (`/healthz`) | ALB host rule → port 8081 |
| `console.staging.compute.unifyai.us` | staging UnifyAPI console (SPA, `/api`, `/v1`) | ALB host rule → port 3000 |

All three are children of `compute.unifyai.us`, which stays a CNAME to OpenAI
Sites for the marketing page until the production cutover in ADR-0009. One
certificate covers all three names. Any other Host gets a fixed `404`.

## What runs on the instance

`deploy/compose/docker-compose.yml` plus `docker-compose.console.yml`
(`COMPOSE_FILE` in the instance's `.env`, which is rendered from SSM at boot),
the same pair the local profile uses:

- `compute-api` — `COMPUTE_API_IMAGE`, published from this repository.
- `site` — `SITE_IMAGE`, the marketing site container.
- `postgres` — default database `compute`; the console overlay creates
  `console` on first boot.
- `console`, `redis` — the console overlay. Own database, own admin, own
  bridge secret. Production has neither.
- `skypilot` — compose profile, off by default until the backend exists.

All images are pinned by digest in `deploy/aws/envs/staging/variables.tf`
(ours and the console) or in the compose files (Postgres, Redis).

## First-time apply

Done by a person or the CI/CD agent with credentials for the UnifyAI AWS
account (871376691052). Terraform >= 1.10 — the S3 backend uses native
lockfiles rather than a DynamoDB table.

The order below is not cosmetic. Steps 0 and 1 each fix a way this stack would
otherwise have booted into a state that `terraform apply` reports as success:

```bash
# 0. the image must exist, with arm64. On 11 Sep 2026 the pinned
#    ghcr.io/felixsphere/unify-compute-api did not exist in the registry at
#    all, while variables.tf pinned it as the default. The instance is a
#    t4g.small: an image that is missing, or amd64-only, produces a host whose
#    `docker compose up` can never succeed — and terraform will not notice.
gh workflow run publish-image.yml --repo FelixSphere/unify-compute \
  -f tag=dev -f reason="first staging apply"
gh run watch --repo FelixSphere/unify-compute
#    then pin the DIGEST the run prints into compute_api_image in
#    deploy/aws/envs/staging/variables.tf. The site image (agent M1) needs the
#    same: publish, then pin site_image by digest. CI's `pinned-images` job
#    asserts digest + exists + arm64 for every pin on every push, and is red
#    until both are done.

# 1. secrets. Use the script, not `aws ssm put-parameter` by hand.
#    The old runbook said `openssl rand -base64 32`; base64 contains "/",
#    these values are interpolated raw into
#      postgresql://unify:PASS@postgres:5432/console
#    and a "/" ends the URL authority. Roughly one generated password in two
#    would have produced a console that never passes its health check, with
#    nothing in the bootstrap log to say why.
scripts/staging-secrets.sh            # --env staging; hex, idempotent, verifies

# 2. remote state. The bucket existed (empty) on 2026-10-02; the script is
#    idempotent and also asserts versioning, encryption and the public block.
scripts/tf-backend-bootstrap.sh

# 3. plan, READ IT, apply
terraform -chdir=deploy/aws/envs/staging init
terraform -chdir=deploy/aws/envs/staging plan -out=staging.tfplan
#    expect exactly 49 to add (a read-only plan on 2026-10-02 showed this):
#    1 VPC, 1 IGW, 2 subnets, 1 route table + 2 associations,
#    2 security groups, 1 ACM cert + 3 validation records + 1 validation,
#    1 ALB, 3 target groups, 3 attachments, 2 listeners, 3 listener rules,
#    3 Route53 alias records, 1 IAM role + profile + 2 policies,
#    1 EBS volume, 1 volume attachment, 1 instance,
#    1 logs bucket + policy + encryption + lifecycle + public-access-block.
#    1 Elastic IP + association,
#    1 backups bucket + encryption + lifecycle + public-access-block +
#    ownership controls, 1 SSM String parameter (backup_bucket).
#    Anything destroying module.stack.aws_ebs_volume.data is a stop.
terraform -chdir=deploy/aws/envs/staging apply staging.tfplan

# 4. pin the AMI. var.ami_id defaults to "", which follows the moving SSM
#    "latest" pointer; that pointer changes within days and `ami` forces
#    instance replacement. The UnifyAPI console stack armed a production
#    destroy this way.
terraform -chdir=deploy/aws/envs/staging output -raw ami_id_in_use   # → ami_id in envs/staging/variables.tf

# 5. verify from OUTSIDE. A green apply is not evidence; neither is `docker ps`.
make smoke BASE_URL=https://staging.compute.unifyai.us \
           CONSOLE_URL=https://console.staging.compute.unifyai.us
curl -sf https://site.staging.compute.unifyai.us/healthz
```

If step 5 fails, read the bootstrap log before touching anything:

```bash
aws ssm send-command --instance-ids "$(terraform -chdir=deploy/aws/envs/staging output -raw instance_id)" \
  --document-name AWS-RunShellScript \
  --parameters 'commands=["tail -80 /var/log/unify-compute-bootstrap.log","systemctl status unify-compute --no-pager","docker ps -a"]' \
  --query 'Command.CommandId' --output text
# then: aws ssm list-command-invocations --command-id <id> --details
```

`send-command` is used rather than `start-session` deliberately: it needs no
session-manager-plugin, so it works from a machine that does not have one.

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
- `GET /healthz` on the site host returns `200`.
- No path on the compute host reaches the console, and vice versa.
- `http://` on any of the three names answers `301` to `https://`, and HTTPS
  responses carry `strict-transport-security: max-age=31536000`.

## Releasing to staging

Image swap over SSM, same as the console's production procedure: `docker pull`
the new digest first, edit the image line in `/opt/unify-compute/docker-compose.yml`,
`docker compose up -d compute-api`, smoke, then commit the pin. Terraform is
for the instance, not for releases.
