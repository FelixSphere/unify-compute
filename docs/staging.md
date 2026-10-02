# Staging

**Status: applied 2026-10-02, not serving.** The stack exists (instance
`i-0780af3b0bfdc3383`, AMI pinned), but its first `compose up` failed: the
GHCR packages are private and the host had no credential (`unauthorized`).
No container has ever run on it. Hosts now pull our images from ECR with the
instance role instead ([ADR-0009](adr/0009-host-everything-on-aws.md),
"Runtime registry: ECR"); staging comes up on the first release after
`shared/ecr`, `shared/github-oidc` and this stack are applied with that
change. Every hostname answers 502 until then. No release has run
(`mode=status` has, once, successfully). This page is the
procedure and the acceptance criteria (`.claude/agents/cicd.md`).

Staging is `deploy/aws/envs/staging/`, a caller of the module in
`deploy/aws/modules/compute-stack/` that production
(`deploy/aws/envs/production/`, not applied) shares — see
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

- `compute-api` — `COMPUTE_API_IMAGE`, published from this repository to
  GHCR and pulled from its ECR copy.
- `site` — `SITE_IMAGE`, the marketing site container, likewise.
- `postgres` — default database `compute`; the console overlay creates
  `console` on first boot.
- `console`, `redis` — the console overlay. Own database, own admin, own
  bridge secret. Production has neither.
- `skypilot` — compose profile, off by default until the backend exists.

All images are pinned by digest in `deploy/aws/envs/staging/variables.tf`
(ours and the console) or in the compose files (Postgres, Redis). Ours are
pinned as ECR refs (`871376691052.dkr.ecr.us-east-1.amazonaws.com/unify-compute-{api,site}@sha256:…`)
with the GHCR build's digest; the console is public on GHCR and Postgres and
Redis are public on Docker Hub.

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
#    until both are done. Pin the digest under the ECR repository of the same
#    name (871376691052.dkr.ecr.us-east-1.amazonaws.com/unify-compute-api@…):
#    that is what the host pulls, and the module refuses a GHCR pin. Apply
#    deploy/aws/shared/ecr and shared/github-oidc before this stack. A fresh
#    instance's boot pull succeeds only if a release has already copied those
#    digests into ECR; if none has, the boot fails its pull and the first
#    release over SSM brings the stack up (host.sh's "first start").

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

## Releasing

**Status: written, never run.** `.github/workflows/release.yml` exists; no
environment has been released through it, because no environment exists
(staging is not applied, production is not defined). The first run of it is
also the first test of it. Record that in the first release record.

Only the CI/CD agent dispatches it (`.claude/agents/cicd.md`). Merging to
`main` runs `ci.yml` and stops.

### What a release is

A set of two image digests, one per deployable:

| Env key on the host | Named, gated and recorded as | Pulled by the host from | Published by |
|---|---|---|---|
| `COMPUTE_API_IMAGE` | `ghcr.io/felixsphere/unify-compute-api@sha256:D` | `871376691052.dkr.ecr.us-east-1.amazonaws.com/unify-compute-api@sha256:D` | `publish-image.yml` |
| `SITE_IMAGE` | `ghcr.io/felixsphere/unify-compute-site@sha256:D` | `871376691052.dkr.ecr.us-east-1.amazonaws.com/unify-compute-site@sha256:D` | `publish-image.yml` (`publish-site`) |

Both are always named, even when only one changed, so every release record
and every staging-first comparison is of a complete set. Tags are refused: a
tag can be repointed, a digest cannot. The dispatch inputs, the manifest
artifact and the release record use the GHCR refs; only the host's `.env`
holds the ECR refs. Same digest either way.

### Release, in order

```bash
# 0. nothing else in flight, and read what the host runs now. `status` is
#    read-only; its output is the "before" in the record.
gh run list --repo FelixSphere/unify-compute --workflow release.yml --limit 5
gh workflow run release.yml --repo FelixSphere/unify-compute --ref main \
  -f environment=staging -f mode=status -f reason="before <release name>"

# 1. staging
gh workflow run release.yml --repo FelixSphere/unify-compute --ref main \
  -f environment=staging \
  -f compute_api_image=ghcr.io/felixsphere/unify-compute-api@sha256:<d> \
  -f site_image=ghcr.io/felixsphere/unify-compute-site@sha256:<d> \
  -f reason="<release name>"
gh run watch --repo FelixSphere/unify-compute <run id>

# 2. exercise on staging what this release changes, then write the record
#    (deploy/releases/README.md). The run summary has the field block.

# 3. production: the SAME digests. Refused otherwise.
gh workflow run release.yml --repo FelixSphere/unify-compute --ref main \
  -f environment=production \
  -f compute_api_image=<same> -f site_image=<same> \
  -f reason="<release name>"
```

What one run does (`plan` has no AWS credentials; `deploy` has them, through
OIDC, only inside the GitHub environment named by `environment`):

| Step | Refuses when |
|---|---|
| `plan`: runs from main | dispatched from any other ref |
| `plan`: in-flight check | another `release.yml` run is queued, waiting or running |
| `plan`: inputs | a digest is not `<its repository>@sha256:<64 hex>` |
| `plan`: provenance (`scripts/release/provenance.sh`) | the digest has no linux/arm64; no `sha-<commit>` tag points at it; that commit is not on main; CI did not pass on it |
| `plan`: staging first (`scripts/release/staging-first.sh`), production only | no successful staging run of `release.yml` uploaded a manifest with exactly these digests |
| `deploy`: copy to ECR (release only) | `crane copy` GHCR → ECR fails, or what ECR then serves for the digest does not hash to it, its `sha256-<digest>` tag or ECR's `DescribeImages` names another digest, it lacks linux/arm64, or a platform manifest is missing. A digest already in ECR is verified, not copied again |
| `deploy`: find the instance | not exactly one running instance tagged `Name=unify-compute-<env>` |
| `deploy`: `status` | — (records the before state) |
| `deploy`: cutover on the host (`scripts/release/host.sh` via `scripts/ssm-run.sh`) | see below |
| `deploy`: smoke (`scripts/release/smoke.sh`) | any probe fails |
| `deploy`: served by AWS, production only | after `DNS_CUTOVER=done`: `compute.unifyai.us` does not resolve to the ALB or does not serve compute-api |
| `deploy`: manifest artifact, last | — (this is what makes the digests eligible for production) |

On the host, under the operations lock `/run/lock/unify-compute-ops.lock`:

1. read the current digests from the compose env file; refuse a target that
   is not this account's ECR registry and our two repositories; if the
   target equals what runs, verify health and stop (`NOOP`). The unit's
   `WorkingDirectory` must be the compose directory. No containers at all is
   refused, except a **first start** (no release history, empty Postgres
   data directory: the stack never ran, as on staging after its failed
   boot), which skips the dump in step 3, starts the stack through
   `unify-compute.service`, and leaves no `PREVIOUS_RELEASE`;
2. log in to ECR with the instance role, `docker pull` the target digests,
   before anything is touched;
3. back up: every Postgres database with `pg_dump -Fc`, each read back with
   `pg_restore -l`, plus `PREVIOUS_RELEASE` and the env file, to
   `/data/backups/<stamp>-<run_id>/`; everything but the env file (it holds
   the secrets) is copied to `s3://<backup_bucket>/<env>/releases/`. No
   bucket, no cutover;
4. write the two keys into the env file atomically, `docker compose up -d`;
5. wait up to 240 s for every container to be running and, where it has a
   healthcheck, healthy, and for `http://127.0.0.1:8080/healthz` to answer;
   then 30 s more, and fail if any container's restart count rose or any
   container was replaced. Both readings are printed either way;
6. check each service runs exactly the digest asked for;
7. on any failure in 4–6: restore the env file, `up -d` again, verify that,
   and fail the job. **The database is not restored automatically**: if the
   new compute-api migrated it, the dump from step 3 is the way back, and
   restoring it is a decision, not a reflex;
8. on success: `/data/releases/PREVIOUS_RELEASE` ← what ran before,
   `CURRENT_RELEASE` ← the target, one line appended to `history.log`; prune
   local backups to the newest 10 (never fatal).

Smoke, from the runner. The ALB routes by Host, so compute-api is probed at
the environment's `API_ORIGIN` and the site at its `PUBLIC_ORIGIN`:
`/healthz` 200 naming compute-api **and the build the provenance step found**;
`/compute/v1/skus` 200 JSON with at least one priced SKU;
`/compute/v1/jobs` without a key 401 with compute-api's `missing_key`;
`/` 200 with `<title>Unify Compute`; `/privacy` 200; `/icon-v3.svg` 200 with
`Cache-Control: max-age` of at least a day. Smoke failing does **not** roll back
automatically, because by then the host has passed its own checks and the
fault may be in front of it. The summary prints the rollback command.

### HOTFIX

A `reason` starting with `HOTFIX` lets production take digests staging has
not run, with a warning on the run and in its summary. Everything else still
applies: provenance, the backup, the host checks, smoke. It is for a live
outage, a security issue, or data being lost, nothing else. CI then requires a
record with `hotfix: true` and a `## What was skipped` section, and the same
digests go to staging afterwards.

### Rolling back

```bash
gh workflow run release.yml --repo FelixSphere/unify-compute --ref main \
  -f environment=<env> -f mode=rollback -f reason="rollback: <why>"
```

This takes the lock, reads `/data/releases/PREVIOUS_RELEASE` on the host,
backs up, and cuts over to it through the same steps and checks as a release.
It needs no digests and skips the staging-first gate, because those digests
already ran in this environment. Then:

- **It goes one step back, not further.** After a rollback, `PREVIOUS_RELEASE`
  is the release you rolled back from, so a second rollback re-applies it. To
  go further back, run an ordinary release with older digests from an earlier
  record; for production those still need their staging manifest (90 days).
- **It does not touch the database.** If the bad release ran a migration the
  old compute-api cannot read, restore the dump the bad release took, under
  the lock, before or instead of rolling back:

  ```bash
  # on the host, via SSM send-command. B is the directory from the BACKUP
  # line of the bad run's cutover output.
  B=/data/backups/<stamp>-<run_id> flock -w 900 /run/lock/unify-compute-ops.lock sh -c '
    cd /opt/unify-compute/compose &&
    docker compose stop compute-api &&
    docker compose exec -T postgres pg_restore -U unify --clean --if-exists -d compute < "$B/compute.dump" &&
    docker compose start compute-api'
  ```

- **Record it.** A rollback is a release (`action: rollback`) and needs its
  own record.
- **If the host has no `PREVIOUS_RELEASE`** (first release on a new instance),
  rollback refuses. Release the previous digests from the last record instead.

### What the environments must provide (agent M2, not yet built)

Assumptions this pipeline makes; a mismatch is a variable at the top of
`scripts/release/host.sh` or a GitHub environment setting, not a rewrite.

| Where | What |
|---|---|
| GitHub environments `staging`, `production` | secret `AWS_DEPLOY_ROLE_ARN`; variables `PUBLIC_ORIGIN` (the site origin smoke probes), `API_ORIGIN` (compute-api's origin), `AWS_REGION` (default `us-east-1`); production also `DNS_CUTOVER` (`done` after the cutover) and optionally `ALB_DNS_NAME`. Both restricted to `main`. Required reviewers were wanted on production; GitHub refused them on this repository's plan (HTTP 422, 2026-10-02), so the release owner rule is the only gate |
| Deploy role | trusts `token.actions.githubusercontent.com` with `sub` = `repo:FelixSphere@164092079/unify-compute@1364156790:environment:<env>` (this repo issues immutable OIDC subjects; see ADR-0009); may `ec2:DescribeInstances`, `ssm:SendCommand` on `AWS-RunShellScript` and on instances tagged for its env only, `ssm:GetCommandInvocation`; ECR push and read on `unify-compute-api` and `unify-compute-site` (the copy from GHCR) |
| Instance | tag `Name=unify-compute-<env>`; SSM agent; `docker compose`, `flock`, `aws`, `curl` |
| Compose | `/opt/unify-compute/compose/docker-compose.yml` and `.env`, with the systemd unit's `WorkingDirectory` set to that directory; services `compute-api` (port 8080 on the host), `site`, `postgres` (user `unify`); the image keys `COMPUTE_API_IMAGE` and `SITE_IMAGE` |
| Data volume | mounted at `/data`; `/data/releases` and `/data/backups` are created on first use |
| SSM | `/unify-compute-<env>/backup_bucket` (bucket name). No registry credential: nothing reads `ghcr_pull_user` / `ghcr_pull_token` any more |
| Instance role | `s3:PutObject` on the backup bucket under `<env>/releases/`; ECR pull (`GetAuthorizationToken`, `BatchGetImage`, `GetDownloadUrlForLayer`, `BatchCheckLayerAvailability`) on `unify-compute-api` and `unify-compute-site` |
| ECR (`deploy/aws/shared/ecr`) | repositories `unify-compute-api`, `unify-compute-site`; the deploy role may push to and read them |
| Site image | published with a `sha-<commit>` tag, like compute-api, or provenance cannot place it |
