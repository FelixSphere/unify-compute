# ADR-0009 — Host everything on AWS: one module, staging and production

- **Status:** Proposed
- **Date:** 2026-10-02
- **Supersedes, once accepted:** the part of [ADR-0004](0004-staging-environment.md)
  that keeps `compute.unifyai.us` on OpenAI Sites, and the statements in
  `README.md`, `AGENTS.md` and `.claude/agents/cicd.md` that the marketing site
  is hosted there. Everything else in ADR-0004 (staging's shape, its own
  console, CI validates and never applies) stands.
- **Related:** ADR-0001 (UnifyAPI owns accounts), ADR-0002 (compute is a
  separate process), ADR-0004 (staging).

## Context

On 2026-10-02 the operator asked to move the whole project to AWS, borrowing
Unify Data's approach. Today:

- The marketing site is served by OpenAI Sites. In Route53 (`unifyai.us`,
  `Z03655182YLBJGN9TBHEN`), `compute.unifyai.us` is a `CNAME` to
  `custom-domains.chatgpt.site` (TTL 300), with `_cf-custom-hostname` and
  `_openai-site-verification` TXT records beneath it.
- `deploy/aws/staging/` was defined and CI-validated in September and
  deliberately never applied. No production stack existed in any form.
- Unify Data runs in the same account (871376691052, us-east-1) as one
  hand-built EC2 behind an ALB, with an ACM certificate, a Route53 alias,
  SSM-only access, IMDSv2 and encrypted EBS, deployed by GitHub Actions through
  OIDC (`unifydata-deploy`: `ssm:SendCommand` on one instance id). It has no
  infrastructure as code, no staging environment, and its database backups
  live on the same disk as the database.

We copy Unify Data's runtime shape and its release mechanism, not its lack of
IaC or of staging, and not its backups.

## Decision

### One module, two environments

```
deploy/aws/modules/compute-stack/   VPC, ALB, cert, DNS, instance, data volume, logs, backups
deploy/aws/envs/staging/            caller: own console, bridge readable
deploy/aws/envs/production/         caller: no console, bridge dark
deploy/aws/shared/github-oidc/      GitHub Actions deploy role per environment
```

Each root has its own state in the existing bucket
(`unifyai-tfstate-871376691052`, native S3 locking):
`unify-compute/staging/terraform.tfstate` (unchanged from the old root),
`unify-compute/production/terraform.tfstate`,
`unify-compute/shared/github-oidc/terraform.tfstate`.

The bucket was empty on 2026-10-02, so moving the staging resources into a
module needed no `moved {}` blocks: nothing had an address yet. Had anyone
applied the old root, every address `X` would now be `module.stack.X`, with the
per-app resources (`target_group.compute_api`, `.console`) now `for_each`
instances keyed `api`, `site`, `console`, and the Route53 alias records keyed
by hostname.

| | Staging | Production |
|---|---|---|
| compute-api | `staging.compute.unifyai.us` | `api.compute.unifyai.us` |
| Site | `site.staging.compute.unifyai.us` | `site.compute.unifyai.us` (always), and `compute.unifyai.us` once cut over |
| Console | `console.staging.compute.unifyai.us`, on the instance, own empty DB | `app.unifyapi.ai` — not ours, not on this instance |
| Containers | compute-api, site, console, Postgres, Redis | compute-api, site, Postgres |
| `COMPUTE_BRIDGE_SECRET` | read from SSM if present | always empty (`bridge_enabled = false`) |
| SSM parameters | `postgres_password`, `redis_password`, `session_secret`, `bridge_secret` | `postgres_password` |
| Snapshots | off (disposable by design) | daily, 14 kept |
| Pre-release dump bucket | yes, force-destroyable | yes |
| ALB deletion protection / log retention | off / 30 days, bucket force-destroyable | on / 90 days, bucket not force-destroyable |
| VPC | `10.70.0.0/16` | `10.71.0.0/16` |

Production's compute-api is deliberately dark: the console bridge is not
released on `app.unifyapi.ai`, and that console belongs to the UnifyAPI CI/CD
agent. Turning the bridge on in production is a reviewed change to
`bridge_enabled`, which replaces the instance; it is not a parameter someone
creates.

### Containers

The instance runs `deploy/compose` under a systemd unit, as staging was
already designed to. The compose files are split:

- `docker-compose.yml` — compute-api, site, Postgres (+ the off-by-default
  `skypilot` profile). On its own, this is production.
- `docker-compose.console.yml` — console and Redis, and the init script that
  creates the console's database. Staging and local add it via `COMPOSE_FILE`.

Separate files rather than a compose profile because a profile would leave
the `${CONSOLE_IMAGE:?}` guard either failing production for a service it
never runs or not protecting staging, depending on compose's handling of
inactive profiles. Postgres' default database is now `compute`; the console
overlay creates `console`.

Every image is pinned by digest: ours and the console in
`deploy/aws/envs/<env>/variables.tf`, Postgres and Redis in the compose
files. CI's `pinned-images` job requires a digest, a pullable manifest and
`linux/arm64` for each. The site image (`ghcr.io/felixsphere/unify-compute-site`,
built by agent M1) is not published yet, and neither is a digest of
compute-api; the job is red until they are. That is the true state — neither
environment can boot — and the job turning green is the signal that it can.

### Instance

Unchanged from staging's design and now shared: `t4g.small` AL2023 arm64 with
a pinned AMI, SSM-only (no SSH ingress, no key pair), IMDSv2 required,
encrypted root and data volumes, the data volume `prevent_destroy`, Docker
from dnf and docker compose v5.6.0 pinned by checksum, secrets read from SSM
at boot with `require_param`/`require_url_safe_param`. The compose files and
their `.env` live in `/opt/unify-compute/compose/`, the systemd unit's
working directory, which is where the release pipeline
(`scripts/release/host.sh`) runs `docker compose`. Additions:

- `docker.service` and the stack's unit `RequiresMountsFor=/data`. With the
  `nofail` fstab entry, a reboot where the volume attached late let Docker
  restart Postgres on an empty `/data` on the root disk.
- A plan-time precondition on EC2's 16 KB user-data limit; both compose files
  are embedded and staging renders at about 13 KB.
- A precondition that `name` is `unify-compute-<environment>`: the deploy
  roles and the release pipeline find the instance by that `Name` tag.
- If `/unify-compute-<env>/ghcr_pull_user` and `ghcr_pull_token` both exist
  and are non-empty, the boot logs in to GHCR before the first pull, the same
  rule the release pipeline applies.
- An Elastic IP, so egress keeps one address across a stop/start (an
  instance-type change, an AWS maintenance event). It adds no cost: AWS bills
  every in-use public IPv4 at the same $0.005/h, and the EIP takes the place of
  the auto-assigned address that the estimate below already counts.

**User data never restarts a running host.** It is a first-boot script, and
the image pins are in it. Letting Terraform act on a change to it has two
possible outcomes, and both are wrong for production.
`user_data_replace_on_change = true` turns every pin bump into a new
instance. With `false`, EC2 still stops and starts the instance to write the
attribute. The UnifyAPI console stack learned this the hard way: minutes of
502s, a rotated egress IP, and the script does not even re-run. So the
instance has `lifecycle { ignore_changes = [user_data] }`. The Terraform pins
are only the **rebuild baseline**. Releases swap images on the running host
over SSM (M3's `release.yml` → `scripts/release/host.sh`, which rewrites
`COMPUTE_API_IMAGE` / `SITE_IMAGE` in the `.env`). A deliberate rebuild, to
apply a user-data change or move the baseline, goes like this:

1. Set the pins in `envs/<env>/variables.tf` to what is released now
   (`deploy/releases/`). Otherwise the rebuild lands on an older baseline.
2. Stop `unify-compute.service` over SSM. Detaching a mounted volume can hang.
3. `terraform apply -replace='module.stack.aws_instance.this'`. Staging goes
   first, always.

### Edge

One ALB per environment, one ACM certificate per environment covering every
hostname it serves (DNS-validated in Route53), host-header rules to one target
group per app, a fixed `404` for any other Host, HTTP→HTTPS `301`, TLS 1.3
policy, `drop_invalid_header_fields`, and `Strict-Transport-Security:
max-age=31536000` added by the ALB's HTTPS listener. No `includeSubDomains` and
no `preload`: both are commitments about names this stack does not own.

Public subnets with the instance security group open only to the ALB's, and
**no NAT gateway**: at this size a NAT gateway (~USD 33/month + USD 0.045/GB per
environment) would cost more than the instance and buy nothing.

### Deploy access: GitHub Actions OIDC, no static keys

`deploy/aws/shared/github-oidc/` creates `unify-compute-staging-deploy` and
`unify-compute-production-deploy`. Each is assumable only with
`aud = sts.amazonaws.com` and, by `StringEquals`,
`sub = repo:FelixSphere@164092079/unify-compute@1364156790:environment:<env>` —
so only a job that declares that GitHub environment, under its protection
rules, gets the role. Permissions: `ssm:SendCommand` on instances tagged
`Name=unify-compute-<env>` and `Project=unify-compute` (the pipeline looks the
instance up by `Name`) with the `AWS-RunShellScript` document, plus `ssm:GetCommandInvocation`/`ListCommandInvocations`/`ListCommands`
and `ec2:DescribeInstances` to find the instance and read results. No SSM
parameter reads: the commands run with the instance's own role.

Two facts checked on 2026-10-02 shaped this:

- The account **already has** the GitHub OIDC provider (created 2026-08-01 for
  Unify Data). The stack reads it with a data source; `create_oidc_provider`
  exists only for an account without one. Check with
  `aws iam list-open-id-connect-providers`.
- FelixSphere repositories issue **immutable OIDC subjects** (owner and repo
  ids, not names): `gh api repos/FelixSphere/unify-compute/actions/oidc/customization/sub`
  returns `use_immutable_subject: true`. A trust policy on
  `repo:FelixSphere/unify-compute:environment:staging` would never match;
  Unify Data's and UnifyAPI's roles use the immutable form too.

The release pipeline (PR #23) was written assuming the
`repo:FelixSphere/unify-compute:environment:<env>` form. That form is wrong for
this repository. Nothing in the workflow has to change, because it only reads
the role ARN. The difference is entirely in the trust policy here.

The roles live outside the environment states so that destroying and
rebuilding staging does not change the role ARN stored in GitHub, and so the
account-wide provider belongs to neither environment.

### Backups: daily EBS snapshots, and a dump before every release

Production uses compose Postgres, not RDS, with two off-host copies that do
different jobs.

**Schedule.** A DLM policy snapshots production's data volume daily at
05:00 UTC (tag `Backup=unify-compute-production-daily`) and keeps 14. Snapshots
were chosen over a nightly `pg_dump` for three reasons:

- Nothing has to run on the host: no timer, and no dump that silently stops.
- One volume holds the whole Postgres data directory with its WAL, so a
  snapshot is crash-consistent, which is what Postgres recovers from after a
  power loss.
- The snapshot also covers SkyPilot state on the same volume.

**Release undo.** compute-api migrates at startup. A snapshot cannot be
restored per database in seconds; a dump can. So the release pipeline runs
`pg_dump` on every database right before it swaps images. It reads the bucket
name from SSM `/unify-compute-<env>/backup_bucket` and refuses to release
without it. Each environment has that bucket, `unify-compute-<env>-backups-<account>`:

- SSE-S3 encryption, public access blocked, bucket-owner ownership
- no versioning, objects expire after 30 days
- the name is published in SSM by Terraform (a bucket name is not a secret)
- the instance role may only `s3:PutObject`, so it can add dumps but cannot
  read or delete them.

Neither copy leaves this account or region. If that becomes a requirement,
add cross-region replication of the dump bucket.

Restore: create a volume from the chosen snapshot in the data volume's AZ,
stop `unify-compute.service`, swap it in (`terraform import` into
`module.stack.aws_ebs_volume.data` after `terraform state rm` of the old one,
because the old volume is `prevent_destroy`), start the service. Rehearse this
on staging (with `snapshot_backups = true` temporarily) before production has
data worth restoring.

### The DNS cutover from OpenAI Sites is separate and reversible

Production's first apply **does not touch `compute.unifyai.us`**:
`manage_site_dns = false` by default. If it is set to `true` while the CNAME
is still there, Route53 rejects the A record ("conflicting RRSet of type
CNAME") and the apply fails without changing the live record. The certificate
covers `compute.unifyai.us` from the first apply, because ACM validation
records are `_<hash>.compute.unifyai.us` — children of the name, like the
existing TXT records. There are no CAA records on `unifyai.us` or on the CNAME
target, so Amazon may issue for it; re-check with `dig CAA compute.unifyai.us`
before the apply.

**Cutover** (CI/CD agent, after production has passed its smoke checks on
`api.compute.unifyai.us`, and the site has run on staging):

1. Prove the site is served correctly by the ALB before any DNS changes.
   `https://site.compute.unifyai.us/` is the same target group, reachable
   from the first apply. Then check the real name against the ALB:
   ```bash
   ALB=$(terraform -chdir=deploy/aws/envs/production output -raw alb_dns_name)
   curl -sfI --resolve "compute.unifyai.us:443:$(dig +short "$ALB" | head -1)" https://compute.unifyai.us/
   curl -sf  --resolve "compute.unifyai.us:443:$(dig +short "$ALB" | head -1)" https://compute.unifyai.us/healthz
   ```
2. Replace the CNAME with the alias in **one** Route53 change batch, which
   Route53 applies atomically — there is no moment without a record:
   ```bash
   ZONE=Z03655182YLBJGN9TBHEN
   ALB_ZONE=$(terraform -chdir=deploy/aws/envs/production output -raw alb_zone_id)
   aws route53 change-resource-record-sets --hosted-zone-id "$ZONE" --change-batch '{
     "Comment": "compute.unifyai.us: OpenAI Sites -> unify-compute-production ALB",
     "Changes": [
       {"Action": "DELETE", "ResourceRecordSet": {"Name": "compute.unifyai.us.", "Type": "CNAME", "TTL": 300,
         "ResourceRecords": [{"Value": "custom-domains.chatgpt.site."}]}},
       {"Action": "CREATE", "ResourceRecordSet": {"Name": "compute.unifyai.us.", "Type": "A",
         "AliasTarget": {"HostedZoneId": "'"$ALB_ZONE"'", "DNSName": "'"$ALB"'", "EvaluateTargetHealth": false}}}
     ]}'
   ```
3. Hand the record to Terraform: set `manage_site_dns = true` in
   `envs/production/variables.tf` (reviewed PR), then
   `terraform import 'module.stack.aws_route53_record.app["compute.unifyai.us"]' Z03655182YLBJGN9TBHEN_compute.unifyai.us_A`
   and confirm `plan` shows no changes. Set the production GitHub variable
   `DNS_CUTOVER=done` and `PUBLIC_ORIGIN` for the site to
   `https://compute.unifyai.us`.
4. Verify from outside once resolvers have the new answer (TTL 300):
   `curl -sfI https://compute.unifyai.us/` shows the ALB's
   `strict-transport-security` header; OpenAI Sites' responses did not carry
   one when checked on 2026-10-02.

Leave the OpenAI Sites project, `.openai/hosting.json` and the two TXT records
in place until the operator decides the rollback window is over; deleting them
is what makes the rollback expensive.

**Rollback** (any time while the OpenAI Sites project still exists):

1. The reverse change batch: `DELETE` the A alias, `CREATE` the CNAME to
   `custom-domains.chatgpt.site.` with TTL 300.
2. `terraform state rm 'module.stack.aws_route53_record.app["compute.unifyai.us"]'`,
   set `manage_site_dns = false` again so the next apply does not recreate it,
   and unset `DNS_CUTOVER`. `site.compute.unifyai.us` keeps serving the AWS
   copy throughout.
3. Expect up to five minutes of mixed answers while the TTL expires. HSTS set by
   the ALB does no harm on the way back: OpenAI Sites serves HTTPS too. If the
   OpenAI-side custom hostname was dropped while the name pointed at AWS, it
   has to be re-verified there before it serves again — the
   `_cf-custom-hostname` TXT record is what that uses.

### Cost estimate (us-east-1 on-demand, October 2026 list prices)

| Item | Staging | Production |
|---|---|---|
| t4g.small (730 h × $0.0168) | $12.26 | $12.26 |
| ALB hours (730 h × $0.0225) | $16.43 | $16.43 |
| ALB LCUs (low traffic, ~0.1–0.5 LCU) | $0.60–2.90 | $0.60–2.90 |
| Public IPv4: instance + 2 ALB nodes (3 × 730 h × $0.005) | $10.95 | $10.95 |
| EBS gp3: 16 GB root + 30 GB data (46 × $0.08) | $3.68 | $3.68 |
| EBS snapshots (incremental; a few GB used) | — | ~$0.25–1.50 |
| ALB log bucket (S3, MB-scale) | <$0.10 | <$0.25 |
| Pre-release dump bucket (30-day expiry, MB-scale today) | <$0.10 | <$0.10 |
| Elastic IP (replaces the auto-assigned IPv4, so it is the "instance" IPv4 above) | $0 extra | $0 extra |
| ACM, SSM parameters and Run Command, Route53 alias queries | $0 | $0 |
| NAT gateway | not used (would be ~$33 + $0.045/GB) | not used |
| **Total** | **~$44–46/month** | **~$45–48/month** |

ADR-0004 estimated staging at USD 15–25/month; that left out the ALB's fixed
hourly charge as distinct from usage, and the public IPv4 charge AWS has
billed since February 2024. Data transfer out and GPU spend through SkyPilot
are separate and not in these numbers. Sharing one ALB between the two
environments would save ~$20/month and is rejected: a listener-rule mistake in
staging would then be a production outage.

## Consequences

- Nothing is applied by this ADR. Applying staging, then production, then
  cutting DNS over are three separate, explicit acts by the CI/CD agent, each
  with its own go. The order is the staging-first rule: production's first
  apply must not be the first time this module runs.
- Before staging can boot: compute-api and the site image published and
  pinned by digest (CI's `pinned-images` turns green), `scripts/staging-secrets.sh`
  run, the shared OIDC stack applied if deploys go through GitHub Actions.
  Production additionally needs `scripts/staging-secrets.sh --env production`.
- The GitHub environments `staging` and `production` must exist in the
  repository, with their protection rules, before the roles are useful. On
  2026-10-02 neither existed. `.github/workflows/release.yml` (PR #23) reads:

  | Setting | Kind | Staging | Production |
  |---|---|---|---|
  | `AWS_DEPLOY_ROLE_ARN` | secret | `deploy_role_arns.staging` from `shared/github-oidc` | `deploy_role_arns.production` |
  | `AWS_REGION` | variable | `us-east-1` (`github_environment` output) | `us-east-1` |
  | `PUBLIC_ORIGIN` | variable | `site_origin` / `api_origin` outputs: `https://site.staging.compute.unifyai.us`, `https://staging.compute.unifyai.us` | `https://site.compute.unifyai.us` before the cutover (`site_origin`), `https://api.compute.unifyai.us` (`api_origin`) |
  | `DNS_CUTOVER` | variable | — | unset until the cutover; `done` after step 3 below |
  | `ALB_DNS_NAME` | variable, optional | `github_environment` output | `github_environment` output |

- **GHCR pull credentials are not Terraform resources.** If the packages are
  private, create both parameters by hand:
  `aws ssm put-parameter --type String --name /unify-compute-<env>/ghcr_pull_user`
  and `--type SecureString --name …/ghcr_pull_token`, the token being a
  `read:packages` PAT. The instance role's prefix scope already covers them.
  Placeholders were asked for, and were not created, for two reasons:
  - `aws_ssm_parameter` writes the decrypted value into state on every
    refresh, and `ignore_changes` does not prevent that. Keeping secrets out
    of state is why no other secret here is a resource.
  - SSM rejects empty values. A placeholder therefore looks "set" to the
    pipeline's `[ -n "$user" ] && [ -n "$token" ]` check, which then tries to
    log in with it, fails under `set -e`, and aborts every release.

  An absent parameter is what means "public packages". That is the default.
- Once accepted, these need updating to say the site is on AWS: ADR-0004's
  status line ("partly superseded by ADR-0009"), `README.md` ("hosted on OpenAI
  Sites"), `AGENTS.md` (the "Where you are" table and Current status),
  `.claude/agents/cicd.md` and `.codex/agents/cicd.toml` (hosting, the old
  `deploy/aws/staging/` paths and apply steps), `THIRD-PARTY-LICENSES.md` (the
  OpenAI Sites plugin), and `vite.config.ts`/`.openai/hosting.json` once the
  rollback window closes. Until the cutover, "the site is on OpenAI Sites"
  remains true and stays written.
- ADR-0005's artifacts bucket (in review on another branch) was written
  against `deploy/aws/staging/`; it now belongs in the module, behind a
  variable if production should not have it yet.
