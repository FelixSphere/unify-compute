# Staging

**Status: running since 2026-10-02.** Instance `i-06125c23285803d7a` (AMI
pinned; it replaced `i-0780af3b0bfdc3383` in a rebuild for #34). The first instance's boot could not pull from private GHCR. The first release
(run 37054858851) started it from ECR by the "first start" path, and run
37055453977 passed smoke and produced the manifest production was gated on.
Records are in `deploy/releases/`. This page is the procedure and the
acceptance criteria (`.claude/agents/cicd.md`).

Staging is `deploy/aws/envs/staging/`, a caller of the module in
`deploy/aws/modules/compute-stack/` that production
(`deploy/aws/envs/production/`, applied 2026-10-02) shares — see
[ADR-0009](adr/0009-host-everything-on-aws.md), Proposed. Staging is applied
first, always.

## Hostnames

| Host | Serves | Routed by |
|---|---|---|
| `staging.compute.unifyai.us` | compute-api (`/compute/v1/*`, `/healthz`) | ALB host rule → port 8080 |
| `site.staging.compute.unifyai.us` | marketing site container (`/healthz`) | ALB host rule → port 8081 |
| `console.staging.compute.unifyai.us` | staging Unify API console (SPA, `/api`, `/v1`) | ALB host rule → port 3000 |

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
  bridge secret. Production has no console container: its compute-api uses
  the production console's bridge on `app.unifyapi.ai` (see "Production
  console bridge").
- `skypilot` — the SkyPilot API server, `docker-compose.skypilot.yml`
  ([ADR-0010](adr/0010-gpu-workers-in-the-shared-account.md)). Installed on the
  running host on 2026-10-03; see "GPU workers and SkyPilot" below.

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

## Production console bridge

**Status: on since 2026-10-03** (operator-approved, cross-product; record
`deploy/releases/2026-10-03-production-console-bridge.md`). Production
compute-api verifies `sk-` keys, and will place and settle holds, against the
real Unify API account system (ADR-0001). GPUs are still off, so a valid
submit ends at `503 upstream_unavailable` before any hold is placed
(`submitWired`: placement and the backend check come before
`JobService.Submit`).

| | Value |
|---|---|
| `CONSOLE_URL` | `https://app.unifyapi.ai` |
| compute side secret | us-east-1 `/unify-compute-production/bridge_secret` (SecureString) |
| console side secret | ap-southeast-5 `/unifyapi/console/compute_bridge_secret` (SecureString), the console's `COMPUTE_BRIDGE_SECRET` |
| rebuilt host | `bridge_enabled = true` in `envs/production/main.tf`; user-data renders both lines |
| running host | `scripts/console-bridge-host.sh` over SSM, under the ops lock |

The two parameters hold **one value**, 64 hex characters, generated once and
never printed. It is distinct from both staging secrets. `staging-secrets.sh`
does not create it: a value created there would match nothing.

Why `app.unifyapi.ai` and not some other origin: there is no edge in front of
it. It is an A alias straight to the console's ALB in ap-southeast-5, with no
WAF in that region and no CloudFront. Measured from the production host on
2026-10-03, both `X-Compute-*` headers arrive intact (a fresh timestamp is
answered `invalid_signature`, not `invalid_timestamp`). It takes 0.22 s to
connect and 0.66 s for a cold TLS request. compute-api's bridge timeout is
10 s. `api-edge.unifyapi.ai` (CloudFront) is the wrong choice: its origin is
the stopped us-east-1 stack.

The console side belongs to the UnifyAPI release owner. Its procedure is the
same-image swap (`infra/scripts/console-swap.sh` in FelixSphere/unifyapi):
add the variable to `/opt/unifyapi/.env` and the compose file, then swap to the
image already running. That way the instance is never stopped and started (no
user_data apply).

### Rollback: one step per side, compute first

```bash
# compute (us-east-1). Keyed routes go back to 503 console_unavailable.
sed 's/^MODE=.*/MODE=off/' scripts/console-bridge-host.sh > /tmp/bridge-off.sh
scripts/ssm-run.sh i-07dd2f6d313a0dafd /tmp/bridge-off.sh "console bridge off" 900

# console (ap-southeast-5, UnifyAPI's host). The routes go back to an empty 404.
#   cd /opt/unifyapi && sed -i '/^COMPUTE_BRIDGE_SECRET=/d' .env \
#     && sed -i '/- COMPUTE_BRIDGE_SECRET=/d' docker-compose.yml \
#     && bash ./console-swap.sh "$(grep -m1 'image: ghcr.io/felixsphere/unifyapi-console:' docker-compose.yml | sed -E 's/^ *image: *//')"
```

Both edit lines rather than restoring a backup, so they stay correct after
later releases have changed the image pins. To make a rollback survive a
rebuild, also set `bridge_enabled = false` in a PR.

### Known risk: one rate-limit bucket, spent before the signature check

The console's bridge limiter (`COMPUTE_BRIDGE_RATE_LIMIT`, default 50/s,
burst 100) is one bucket per console process, and it takes a token
**before** it checks the HMAC. Measured 2026-10-03: 200 unsigned requests
from inside the host got 62 `429`s. Anyone on the internet can therefore
starve compute's bridge calls with about 50 requests a second. The
per-IP global limiter deliberately does not cover these routes. There is no
money risk: a starved call is `console_unavailable`, and no hold is placed or
settled. It is an availability risk to fix in the console (verify first, or
limit per IP before the shared bucket) before compute takes paying traffic.
It is reported to UnifyAPI.

## GPU workers and SkyPilot

**Status: applied to staging on 2026-10-03** (47 added, 2 changed, 0
destroyed; SkyPilot set up over SSM, ending `SKYPILOT_SETUP_DONE all`). The only
step left is step 0, the quota (request `d81c8fcd…`, CASE_OPENED). A
staging job fails today with `capacity_exhausted`, and AWS says
`VcpuLimitExceeded … limit of 0`. The quota to ask for is now 4 vCPU, not 8
(ADR-0010, "Start small"); answer the open case rather than filing another.
The budget and its automatic stop (step 4) were applied on 2026-10-03:
**USD 50/month**, with alerts to the operator's address (AWS asked it to
confirm the subscription by email). The action is
`b4302f8f-c1df-40b6-8157-d71a203ae18a`: STANDBY, approval AUTOMATIC, 100% of actual
spend, `unify-compute-gpu-budget-stop-launches` onto
`unify-compute-staging-skypilot`. The dry run below passed: `DryRunOperation`
before, `UnauthorizedOperation` with the policy attached by hand, then
detached. The start-small Terraform (0 add, 2 change) is applied, and the reaper
reports `max_age_hours` 5. Records:
`deploy/releases/2026-10-03-staging-start-small.md`, Record:
`deploy/releases/2026-10-03-staging-gpu-ready.md`. The design and the isolation argument are in
[ADR-0010](adr/0010-gpu-workers-in-the-shared-account.md).

What `terraform apply` on `deploy/aws/envs/staging` adds: the workers VPC
`unify-compute-staging-workers` (10.73/16, five public subnets, no NAT),
its security group, the launch role `unify-compute-staging-skypilot`, the
worker role and profile `unify-compute-staging-worker`, and the reaper (Lambda,
EventBridge rule, SNS topic, two alarms). What it changes in place on the
host: the instance role may now assume the launch role, and the instance
type goes t4g.small → t4g.medium, which is a **stop/start of staging** (a few minutes down; the Elastic IP, the data
volume and the stack come back by themselves). The read-only plan on
2026-10-03 was **47 to add, 2 to change, 0 to destroy**. 29 of the adds are
these, and 18 are ADR-0005's artifacts bucket, logs bucket and writer role
(#38), merged but not applied yet. The instance is updated, not replaced.
Production's plan gains nothing from this (`gpu_workers_enabled = false`);
its only pending adds are ADR-0005's 18.

User-data is ignored after first boot, so the running host gets SkyPilot from
the same setup script user-data would run, sent once over SSM. Order matters:
each step depends on the one before.

```bash
# 0. GPU quota. On 2026-10-03 both G/VT quotas were 0 vCPU, so nothing can
#    launch until this is granted. Filed by the operator, not by an agent:
aws service-quotas request-service-quota-increase --region us-east-1 \
  --service-code ec2 --quota-code L-DB2E81BA --desired-value 4
#    (Running On-Demand G and VT instances. 4 vCPU = exactly one g5.xlarge,
#    which is all ADR-0010's "Start small" amendment allows: one GPU job on
#    the whole platform. AWS answered the earlier 8 vCPU request by
#    suggesting a smaller start. The quota is account-wide, so it is the one
#    concurrency cap that holds even if compute-api's own limit does not;
#    asking for more would double the budget overshoot. The cost: when an
#    attempt is torn down and the next comes up, the new launch waits for
#    the old instance to finish terminating. Spot, L-3819A6DF, is not
#    needed: the spot SKU is unavailable and the launch role is denied spot.)
#    If AWS asks for a use case: "Batch inference on one NVIDIA A10G
#    (g5.xlarge) at a time in us-east-1 for a staging environment, launched
#    by SkyPilot, every instance terminated within 5 hours by an automated
#    reaper, launches stopped automatically by an AWS Budgets action at
#    USD 50 a month."
#    State on 2026-10-03 (read-only): quota 0; an earlier request for 8 is
#    open (CASE_OPENED 2026-10-02). Answer that case asking for 4, rather
#    than filing a second request beside it.

# 1. the admin password for the SkyPilot API server (hex, like the others)
scripts/staging-secrets.sh --env staging        # creates skypilot_admin_password, keeps the rest

# 2. cost allocation tag (account-wide, so not in Terraform; not
#    retroactive). Without it the budget in step 4 sees nothing, and its
#    automatic stop never fires (ADR-0010, "Start small"). It read Active on
#    2026-10-03, so this is a no-op unless someone switched it off.
aws ce update-cost-allocation-tags-status \
  --cost-allocation-tags-status TagKey=Project,Status=Active

# 3. staging. Set gpu_alert_email in envs/staging/variables.tf (or -var) so
#    reaper reports reach someone; the address must confirm the SNS mail.
terraform -chdir=deploy/aws/envs/staging plan -out=staging.tfplan
#    expect 47 to add (48 with gpu_alert_email; 18 are ADR-0005's artifacts
#    resources if they are still unapplied), 2 to change, 0 to destroy:
#    module.stack.aws_instance.this updated in place (instance_type only) and
#    module.stack.aws_iam_role_policy.secrets (sts:AssumeRole). Anything
#    replacing aws_instance.this or touching aws_ebs_volume.data is a stop.
#    (On a stack that already has the workers, the start-small change alone
#    is 0 to add, 2 to change: the launch policy gains DenySpot and
#    DenyOtherWaysToLaunch, and the reaper's MAX_AGE_HOURS goes 26 -> 5.)
terraform -chdir=deploy/aws/envs/staging apply staging.tfplan

# 4. the budget and its automatic stop, its own stack. After staging,
#    because the action names staging's launch role; before step 5, so no
#    worker can launch before the stop exists.
terraform -chdir=deploy/aws/shared/gpu-budget init
terraform -chdir=deploy/aws/shared/gpu-budget plan -var alert_email=<address> -out=budget.tfplan
#    5 to add: the budget (USD 50; 50/80/100% actual, 100% forecast), the
#    deny policy, the Budgets execution role and its policy, the action.
#    Read-only plan on 2026-10-03: 5 to add, 0 to change, 0 to destroy.
terraform -chdir=deploy/aws/shared/gpu-budget apply budget.tfplan
#    Check: aws budgets describe-budget-actions-for-budget --account-id <id> \
#      --budget-name unify-compute-gpu-workers  -> one action, STANDBY.

# 5. SkyPilot on the running host, once. Takes the ops lock; assumes the
#    launch role (fails here if step 3 did not happen); writes the overlay,
#    its config and the credential timer; brings the stack up; mints
#    compute-api's service-account token on the host.
terraform -chdir=deploy/aws/envs/staging output -raw skypilot_host_setup > skypilot-setup.sh
scripts/ssm-run.sh "$(terraform -chdir=deploy/aws/envs/staging output -raw instance_id)" \
  skypilot-setup.sh "skypilot setup staging" 900
#    ends with SKYPILOT_SETUP_DONE all

# 6. a release, so host.sh's checks (every container healthy and stable,
#    skypilot now included) and smoke run against the new shape. Same digests
#    as now is fine: it verifies and reports NOOP.
```

What to exercise afterwards, on the host (SSM), before calling it done:

- `docker compose ps`: `skypilot` running and healthy; `compute-api`
  restarted once by step 5.
- `docker compose exec skypilot sky check aws`: AWS enabled, identity is
  `assumed-role/unify-compute-staging-skypilot/skypilot-staging`.
- `docker network inspect compose_skypilot --format '{{range .Containers}}{{.Name}} {{end}}'`
  lists exactly the `skypilot` and `compute-api` containers (the project is
  named after `/opt/unify-compute/compose`). The console, Redis, Postgres and
  the site are not on it.
- `docker compose exec skypilot aws sts get-caller-identity` names the launch
  role, `assumed-role/unify-compute-staging-skypilot/skypilot-staging`
  (credentials from the file, not IMDS).
- `docker compose exec skypilot curl -s -o /dev/null -w '%{http_code}' http://skypilot:46580/api/status`
  answers 401 without the token.
- `systemctl list-timers unify-compute-skypilot-credentials.timer` shows the
  next run within 20 minutes.
- Invoke the reaper once (`aws lambda invoke --function-name
  unify-compute-staging-gpu-reaper out.json`): it reports zero workers and
  `max_age_hours` 5.
- The budget's stop, without waiting for a month's spend. From the skypilot
  container, a dry run the launch role allows:

  ```bash
  T='{Key=Project,Value=unify-compute-workers},{Key=Environment,Value=staging}'
  aws ec2 run-instances --dry-run --instance-type g5.xlarge --image-id <a us-east-1 AMI> \
    --subnet-id <a workers subnet> --security-group-ids <the workers SG> \
    --iam-instance-profile Name=unify-compute-staging-worker \
    --tag-specifications "ResourceType=instance,Tags=[$T]" "ResourceType=volume,Tags=[$T]"
  ```

  answers `DryRunOperation`. Attach `unify-compute-gpu-budget-stop-launches`
  to `unify-compute-staging-skypilot` by hand and rerun: it must now answer
  `UnauthorizedOperation`. Detach it again. The budget action attaches it
  when the time comes.
- The first real launch, after the quota is granted, is the test of
  everything IAM can only simulate. Run one `a10g-24gb-x1` job and confirm
  that the worker has `Project=unify-compute-workers`, `Environment=staging`,
  the `unify-compute-staging-worker` profile, sits in a 10.73 subnet with only
  the workers security group; that `/down` terminates it; and that a
  `UnauthorizedOperation` anywhere in the launch is decoded
  (`aws sts decode-authorization-message`) before any permission is widened.

Changing the SkyPilot version later: edit the digest in
`docker-compose.skypilot.yml` and rerun step 5. Releases do not manage that
image (host.sh changes only `COMPUTE_API_IMAGE` and `SITE_IMAGE`).

## IMDS lockdown

**Status: applied to both hosts on 2026-10-03** (staging, then production:
`IMDS_LOCKDOWN_DONE all`, `CHECK_IMDS_OK`, hop limit 1 by Terraform,
`CHECK_IMDS_OK` again). Staging also passed after the t4g.medium stop/start,
which exercised the boot-ordering path. Until a host
is locked down, `release.yml` refuses to release or roll back on it
(`host.sh`). The decision and the reasoning are in
[ADR-0005](adr/0005-artifact-storage.md), amendment of 2026-10-03.

What it is: no container on the host reaches the instance metadata service.
compute-api is no exception; it reads the instance role from a file the host
refreshes. Three pieces, all installed by `scripts/imds-lockdown.sh`:

| Piece | What | Survives |
|---|---|---|
| `unify-compute-imds-firewall.service` | `iptables -t raw` PREROUTING DROP for 169.254.169.254 (ip6tables for `fd00:ec2::254`) | reboots (enabled, `Before=docker.service`); Docker restarts (`PartOf=docker.service` re-applies it) |
| `unify-compute-api-credentials.timer` | every 2 min, IMDS's answer for the instance role → `/run/unify-compute/compute-api-aws/credentials.json` (tmpfs, uid 65532, 0400) | reboots (rewritten before Docker starts, then every 2 min) |
| `docker-compose.imds.yml` in `COMPOSE_FILE` | compute-api mounts that directory read-only, `INSTANCE_ROLE_CREDENTIALS_FILE` | everything; it is a file in the compose directory |

The fourth piece is Terraform: `http_put_response_hop_limit = 1` in the
module, changed in place by `ModifyInstanceMetadataOptions`, with no
stop/start.

A fresh host gets all of it from user-data (`PHASE=files`, before the stack
first starts). A running host gets it once over SSM. Per host, staging first,
then production, in this order:

```bash
ID=i-06125c23285803d7a          # staging; production is i-07dd2f6d313a0dafd

# 1. lock down. Takes the ops lock, installs the three pieces, recreates
#    compute-api only (a few seconds of 502 on the API host; the site and the
#    console are not touched), and proves from compute-api's network
#    namespace that IMDS no longer answers.
scripts/ssm-run.sh "$ID" scripts/imds-lockdown.sh "imds lockdown <env>" 300
#    last line: IMDS_LOCKDOWN_DONE all

# 2. prove it. Read-only.
scripts/ssm-run.sh "$ID" scripts/release/check-imds.sh "check imds <env>" 120
#    last line: CHECK_IMDS_OK. Every container "IMDS unreachable", the host's
#    control "IMDS token", compute-api's mount, variable and file ok.

# 3. hop limit 1. Read-only plans of this branch (-lock=false), taken while
#    writing it: production 18 to add, 1 to change, 0 to destroy, the change
#    being aws_instance.this metadata_options.http_put_response_hop_limit
#    2 -> 1 and nothing else (the 18 are ADR-0005's, unapplied); staging 47
#    to add, 2 to change, 0 to destroy, where aws_instance.this ALSO changes
#    instance_type t4g.small -> t4g.medium (ADR-0010, unapplied), which IS a
#    stop/start. No user_data diff in either. Read the plan anyway.
terraform -chdir=deploy/aws/envs/<env> plan -out=<env>.tfplan
terraform -chdir=deploy/aws/envs/<env> apply <env>.tfplan
aws ec2 describe-instances --instance-ids "$ID" \
  --query 'Reservations[].Instances[].MetadataOptions.HttpPutResponseHopLimit'   # → 1

# 4. prove it again (step 2). It must still pass.
```

What to exercise on staging, beyond step 2:

- `flock -w 900 /run/lock/unify-compute-ops.lock systemctl restart docker`
  over SSM, wait for `docker compose ps` to settle, then step 2 again. This
  is the Docker-restart path; a few minutes of 502 on staging.
- A reboot of staging (`reboot` over SSM, under the lock), then step 2. This
  is the boot-ordering path: the rule before Docker, the file before
  compute-api.
- `docker compose exec skypilot aws sts get-caller-identity` still names the
  launch role, if SkyPilot is installed by then (its credentials come from
  its own file, not IMDS).
- Once ADR-0005's `.env` lines are on the host and a compute-api with
  `awsv4.File` (this change or later) runs: compute-api's log has `aws
  credentials from file`, and a job's artifact upload works.

Order relative to other changes: lock a host down **before** adding
ADR-0005's artifact settings to it, and release a compute-api built from
this change or later before those settings are used. An older compute-api
ignores `INSTANCE_ROLE_CREDENTIALS_FILE` and, with IMDS closed, has no AWS
credentials at all. Step 1 warns if it finds that combination. The SkyPilot
setup (above) may run before or after; both scripts only append their
overlay to `COMPOSE_FILE`.

If `check-imds.sh` ever fails on a locked-down host, the rule was removed
behind the unit's back. Find out how before re-running step 1. That is what
host.sh's refusal is for.

## SES identity for compute.unifyai.us (ADR-0011, not yet applied)

compute-api's account email (verification codes, resets) goes out through
SES as `Unify Compute <no-reply@compute.unifyai.us>` from both environments.
Two pieces, both **defined and not applied**:

- `deploy/aws/shared/ses-compute`, one stack for both environments: the
  domain identity with Easy DKIM (2048-bit), MAIL FROM
  `bounce.compute.unifyai.us`, `_dmarc.compute.unifyai.us` (`p=none`,
  reports to `dmarc_report_email`), and the configuration set
  `unify-compute` as the identity's default, with reputation metrics and a
  CloudWatch event destination. Seven Route53 records, all new names under
  `compute.unifyai.us`; the `compute.unifyai.us` A alias and every other
  record are untouched, and Route53 refuses to overwrite a record that
  already exists.
- `modules/compute-stack/ses.tf`, in each environment: an instance-role
  policy granting `ses:SendEmail`/`ses:SendRawEmail` on the identity **and**
  the configuration set, only when the From address is
  `no-reply@compute.unifyai.us`, plus three SSM String parameters,
  `/<name>/ses_identity`, `ses_configuration_set` and `ses_sender`. Nothing on
  the host reads them yet (plan tasks A2 and A8 do).

No sandbox exit: account 871376691052 has SES production access in us-east-1
(checked 2026-10-03). A suspension earned by any product in the account stops
these sends too.

```bash
# 0. DMARC reports to an address outside unifyai.us are dropped unless that
#    domain authorises them. For felixsphere.com (Cloudflare DNS, not this
#    repository's), add once:
#      compute.unifyai.us._report._dmarc.felixsphere.com  TXT  "v=DMARC1"
#    Absent on 2026-10-03. Without it the record still applies p=none; only
#    the reports are lost.

# 1. the identity. Read-only plan on 2026-10-03: 10 to add, 0 to change,
#    0 to destroy (identity, mail-from attributes, configuration set, event
#    destination, 3 DKIM CNAMEs, MX + SPF TXT on bounce, DMARC TXT).
terraform -chdir=deploy/aws/shared/ses-compute init
terraform -chdir=deploy/aws/shared/ses-compute plan -out=ses.tfplan \
  -var dmarc_report_email=<operator address>
terraform -chdir=deploy/aws/shared/ses-compute apply ses.tfplan

# 2. wait for verification (DKIM usually within minutes, up to 72 h):
aws sesv2 get-email-identity --email-identity compute.unifyai.us \
  --query '{v:VerifiedForSendingStatus,dkim:DkimAttributes.Status,mf:MailFromAttributes.MailFromDomainStatus,cs:ConfigurationSetName}'
#    expect true, SUCCESS, SUCCESS, unify-compute.

# 3. staging, then production. Read-only plans on 2026-10-03, each: 4 to add,
#    0 to change, 0 to destroy (aws_iam_role_policy.instance_ses and the three
#    SSM parameters). Nothing touches aws_instance.this; anything that does is
#    a stop.
terraform -chdir=deploy/aws/envs/staging plan -out=staging.tfplan
terraform -chdir=deploy/aws/envs/staging apply staging.tfplan
```

What to exercise: from the staging host, a send to
`success@simulator.amazonses.com` with the instance role, From
`no-reply@compute.unifyai.us`, must succeed; the same with From
`other@compute.unifyai.us` must be `AccessDenied`. The real path is the
mailer in A8; until it exists, `aws sesv2 send-email` over SSM proves the
policy. A send denied on `configuration-set/unify-compute` means the second
ARN is missing.

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

**Status: in use since 2026-10-02.** The first releases to staging and
production are recorded in `deploy/releases/`.

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
| `deploy`: cutover on the host (`scripts/release/host.sh` via `scripts/ssm-run.sh`) | the host has no IMDS lockdown, or its rule is missing ("IMDS lockdown" above); otherwise see below |
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
`/` 200 with `<title>Unify Compute`; `/privacy` 200; `/icon-v4.svg` 200 with
`Cache-Control: max-age` of at least an hour (the Caddyfile's value). Smoke failing does **not** roll back
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
| Instance | tag `Name=unify-compute-<env>`; SSM agent; `docker compose`, `flock`, `aws`, `curl`, `iptables`, `nsenter`; the IMDS lockdown (`/usr/local/sbin/unify-compute-imds-firewall`) |
| Compose | `/opt/unify-compute/compose/docker-compose.yml` and `.env`, with the systemd unit's `WorkingDirectory` set to that directory; services `compute-api` (port 8080 on the host), `site`, `postgres` (user `unify`); the image keys `COMPUTE_API_IMAGE` and `SITE_IMAGE` |
| Data volume | mounted at `/data`; `/data/releases` and `/data/backups` are created on first use |
| SSM | `/unify-compute-<env>/backup_bucket` (bucket name). No registry credential: nothing reads `ghcr_pull_user` / `ghcr_pull_token` any more |
| Instance role | `s3:PutObject` on the backup bucket under `<env>/releases/`; ECR pull (`GetAuthorizationToken`, `BatchGetImage`, `GetDownloadUrlForLayer`, `BatchCheckLayerAvailability`) on `unify-compute-api` and `unify-compute-site` |
| ECR (`deploy/aws/shared/ecr`) | repositories `unify-compute-api`, `unify-compute-site`; the deploy role may push to and read them |
| Site image | published with a `sha-<commit>` tag, like compute-api, or provenance cannot place it |
