# ADR-0010 — GPU workers in the shared account

- **Status:** Proposed
- **Date:** 2026-10-03, amended 2026-10-03 (IMDS closed to containers), amended
  2026-10-03 (start small: GPU spend hard-capped, see [below](#amendment-start-small-2026-10-03))
- **Related:** ADR-0003 (SkyPilot through its API server), ADR-0006 (upstreams
  are SkyPilot clouds), ADR-0009 (everything on AWS), `docs/spec/backend.md`
  (C4: compute-api drives SkyPilot clusters over HTTP).

## Context

C4's backend drives a SkyPilot v0.13.0 API server, and until now there was no
API server and nothing it could launch into. The operator decided on
2026-10-03:

- GPU workers run in the **same AWS account** as everything else,
  871376691052, us-east-1. That account also runs the production stacks of
  Unify API (console), Unify Data, the gateway and SelfAI.
- The first real workload is the `batch-infer-vllm` template on
  `a10g-24gb-x1`, which is AWS `g5.xlarge` (one A10G, 24 GB).

So the question is not whether to share the account but how to make sharing
it safe. "Safe" has three parts: a worker, or the identity that launches it,
must not be able to touch another product's resources; customer code on a
worker must not be able to reach anything of ours; and a bug or an outage must
not turn into an open-ended GPU bill.

## Decision

Everything below is defined in `deploy/aws/modules/gpu-workers/` (per
environment), `deploy/aws/modules/compute-stack/` (the host side),
`deploy/compose/docker-compose.skypilot.yml`, `scripts/skypilot-host-setup.sh`
and `deploy/aws/shared/gpu-budget/`. Staging has it; production calls the same
module behind `gpu_workers_enabled = false`.

### Network: a VPC per environment that holds only workers

| Environment | Workers VPC | Compute host VPC |
|---|---|---|
| staging | `unify-compute-staging-workers`, 10.73.0.0/16 | 10.72.0.0/16 |
| production (reserved) | `unify-compute-production-workers`, 10.74.0.0/16 | 10.71.0.0/16 |

Ranges in use in the account on 2026-10-03: 10.20 (console), 10.60 (gateway),
10.70 (Unify Data), 10.71/10.72 (compute), 10.80/10.81 (SelfAI), 172.31
(default VPC). One public subnet per zone that offers g5 (us-east-1a, b, c,
d, f; not e). An internet gateway, **no NAT gateway**, and **no peering**.

SkyPilot reaches workers over their public IP, and the workers' security
group admits SSH (tcp/22) only from the compute host's Elastic IP. Peering was
rejected: it would put a customer's container one route and one
security-group edit away from Postgres and the console. With public IPs there
is no network path from a worker into any of our VPCs at all. Egress is open,
because jobs pull images, models and data. A NAT gateway would cost more per
month than the host and buy nothing here.

The security group is pre-created. SkyPilot's own (`sky-sg-*`) opens SSH to
`0.0.0.0/0`. Given a named group that already has rules, SkyPilot leaves it
alone (`sky/provision/aws/config.py`, `_configure_security_group`), and the
launch role cannot create or edit one.

### Who launches: a dedicated role, assumed by the host, held by one container

The SkyPilot API server runs as a container on the compute host, in the
compose stack. Its AWS identity is a **dedicated launch role**,
`unify-compute-<env>-skypilot`. Only the host's instance role may assume it.
No static keys exist anywhere.

The instance role is not given the launch permissions directly, for two
reasons. A separate role keeps the launch boundary in one policy, readable on
its own, rather than mixed into the host's SSM and ECR policy. And CloudTrail
then shows every EC2 call SkyPilot makes as session `skypilot-<env>` of a role
that does nothing else.

How the credentials reach the SkyPilot container:

- A systemd timer on the host assumes the launch role every 20 minutes and
  writes the session, mode 0600, to `/data/skypilot-aws/credentials.json`.
  Only the SkyPilot container mounts that directory, read-only. Its
  `AWS_CONFIG_FILE` sets `credential_process = cat …`, which botocore treats
  as refreshable credentials. Role chaining caps a session at an hour, so
  there are always at least 40 minutes left. SkyPilot never uses IMDS.
- **Other containers cannot reach the launch role** (amended 2026-10-03).
  As first written, the host's IMDS hop limit was 2, so every container on
  the host could obtain the instance role, and with it assume the launch
  role: a compromised staging console could have launched workers.
  [ADR-0005](0005-artifact-storage.md)'s amendment of 2026-10-03 closes
  IMDS to every container (a host rule dropping 169.254.169.254, plus hop
  limit 1). compute-api gets the instance role from a file the host writes,
  in the same way. SkyPilot is unaffected, because it reads the session file and
  never IMDS. Only the host and compute-api hold the instance role now.
  compute-api's copy can still assume the launch role (ADR-0005 open
  question 5), and the launch role's policy below is the bound for that.

The API server is reachable only by compute-api: it has no host port and no
ALB rule. It sits on an internal compose network shared with compute-api
alone, plus an egress network that only it joins. It also requires auth.
Basic auth protects the admin (password in SSM), which is used once, on the
host, to mint a service-account token. compute-api then sends that token as
`Authorization: Bearer` (`SKYPILOT_SERVICE_ACCOUNT_TOKEN`; the URL is
`SKYPILOT_API_SERVER_URL=http://skypilot:46580`, the names C4 uses).

### What the launch role can do, and why it cannot touch other products

The permissions start from SkyPilot v0.13.0's documented minimal policy
(`docs/source/cloud-setup/cloud-permissions/aws.rst`, "Minimal IAM policy").
They are then narrowed to what its AWS provisioner actually calls
(`sky/provision/aws/config.py`, `instance.py`): run, start, stop and terminate
instances, tag on create, re-tag the head node, and `Describe*`. Removed from
the documented policy: `CreateSecurityGroup` and
`AuthorizeSecurityGroupIngress`, `iam:CreateRole`/`AttachRolePolicy`/
`CreateInstanceProfile`, `DeleteTags`, and the spot service-linked role
(`AWSServiceRoleForEC2Spot` already exists in the account).

The isolation rests on tags. Every worker is tagged
`Project=unify-compute-workers` and `Environment=<env>` at launch. SkyPilot's
`aws.labels` sets the tags, and the role refuses to launch without them. The
guarantee follows from four rules in the policy:

1. **It can only launch tagged workers, of allow-listed types, into the
   workers subnets, with the worker profile.** `RunInstances` requires
   `aws:RequestTag/Project` and `aws:RequestTag/Environment`, an
   `ec2:InstanceType` from the allow-list (`["g5.xlarge"]`), and
   `ec2:InstanceProfile` = the worker profile. Subnets and the security group
   are named by ARN. There is an explicit Deny for any other instance type,
   and for any subnet or security group outside the workers VPC.
2. **It can only stop, start or terminate instances that already carry its
   tags.** The Allow requires `ec2:ResourceTag/Project=unify-compute-workers`
   and the environment's tag. An explicit Deny covers terminate, stop, start,
   reboot, attribute and metadata changes, and profile changes on anything
   whose Project or Environment tag differs.
3. **It cannot give another instance those tags.** `CreateTags` is allowed
   only on create (`ec2:CreateAction=RunInstances`) or on an instance that
   already has them. A Deny refuses any request that sets Project to another
   value, and `DeleteTags` is denied outright. So the role cannot pull a
   foreign instance into its pool, and it cannot push a worker out of it.
4. **It cannot hand out identities.** `iam:PassRole` is allowed for the
   worker role only, to `ec2.amazonaws.com`. It has no other IAM write.

No other product's instance carries `Project=unify-compute-workers` (on
2026-10-03 they are tagged `unify-compute`, `UnifyData`, `unify-selfai` or
nothing), and rule 3 means the role cannot change that. So it cannot act on
them. The Deny statements repeat the boundary on purpose: they still hold if
someone later attaches a broader policy to the role. The compute host itself
is `Project=unify-compute`, so the launch role cannot terminate its own host
either.

Checked before merge, with no AWS change: IAM Access Analyzer
`validate-policy` reports no findings on the launch, reaper and worker
policies. `iam simulate-custom-policy` against the rendered launch policy
allows: terminating a staging worker, launching a tagged g5.xlarge with the
worker profile into the workers subnet, re-tagging a worker's Name, and
tag-on-create. It denies: terminating a production worker from the staging
role; terminating, stopping or rebooting the compute host, an untagged
instance, a UnifyData or a SelfAI instance; tagging the host or an untagged
instance into the pool; re-tagging a worker out of it; launching p4d.24xlarge,
launching untagged, launching with `skypilot-v1` or with no profile; launching
into the console's or the default VPC's subnets; spot requests;
`CreateSecurityGroup`; `iam:CreateRole`; passing the host's role; `DeleteTags`;
and any S3 access.

### What a worker is: an identity that is denied everything

Left to its defaults, SkyPilot attaches its own role, `skypilot-v1`, to every
node and creates it if it can, with `AmazonEC2FullAccess` and
`AmazonS3FullAccess` (`sky/provision/aws/config.py`, `_configure_iam_role`).
Customer code shares the node and can read IMDS. With that role, any customer
could terminate every instance in this account. `aws.remote_identity:
NO_UPLOAD` does not avoid it: it falls back to the default for the template
(`sky/backends/backend_utils.py`, `write_cluster_config`).

So `remote_identity` names a pre-created profile, `unify-compute-<env>-worker`,
whose role has one inline policy: `Deny * on *`. It exists only so SkyPilot
attaches it instead of making its own. The launch role cannot launch
without it (rule 1).

### Money: quota as the hard cap, a budget, and a reaper

> Amended by [Start small](#amendment-start-small-2026-10-03): the budget is
> USD 50 and stops launches by itself, the quota to request is 4 vCPU, the
> runtime ceiling is 4 h and the reaper's age 5 h. The text below is the
> original decision.

- **Concurrency cap.** IAM cannot count running instances. The hard cap is
  the account's EC2 quota *Running On-Demand G and VT instances* (vCPU,
  `L-DB2E81BA`). It applies to the whole account, and no other product runs G
  instances (checked 2026-10-03). On 2026-10-03 it is **0**, and so is the
  spot equivalent (`L-3819A6DF`). The request to file is in
  `docs/staging.md`. compute-api's own quotas (C8) are the per-customer cap
  above that. The reaper also reports whenever more than
  `max_concurrent_workers` (1) workers exist.
- **Budget.** One AWS Budget, `unify-compute-gpu-workers`, covers spend
  tagged `Project=unify-compute-workers` in both environments: USD 300 a
  month by default, with alerts at 50% and 80% actual and 100% forecast. It
  lives in its own stack because a budget cannot AND two tags. It sees
  nothing until `Project` is activated as a cost allocation tag. That was
  Inactive on 2026-10-03; activation is account-wide, so it is a documented
  step and not Terraform.
- **Reaper.** A Lambda, scheduled every 15 minutes by EventBridge, lists this
  environment's workers. It terminates any launched more than 26 hours ago:
  the 24-hour platform ceiling on `max_runtime_seconds` (upstreams.md R9)
  plus 2 hours for provisioning and teardown retries. It publishes a report
  to SNS whenever it terminates something, fails, or sees more workers than
  the limit. Its role can terminate only this environment's workers. It does
  not run on the host on purpose: it exists for the cases where the host,
  SkyPilot or compute-api is not running. Two alarms catch a reaper that
  errors or stops being invoked.

### Cost model

On-demand, us-east-1, from the AWS Price List API on 2026-10-03:

| Item | USD per worker-hour |
|---|---|
| g5.xlarge, Linux on-demand | 1.006 |
| Root volume, SkyPilot's default 256 GB gp3 at USD 0.08/GB-month | ~0.028 |
| Public IPv4 | 0.005 |
| **Worker, before data transfer** | **~1.039** |

Data transfer out is extra (USD 0.09/GB after the first 100 GB a month).
Provisioning is not billed to the customer (metering.md I1), but it costs
us: SkyPilot's launch-to-`UP` time, a few minutes per attempt, about
USD 0.05–0.10. `a10g-24gb-x1` is priced at USD 1.20/h in the illustrative
catalogue. That leaves about USD 0.16/h, or 13%, before transfer, failed
attempts and idle time. That price is a placeholder, not a decision.

The spot price for g5.xlarge was USD 0.40–0.57/h across zones on 2026-10-02.
Spot needs its own quota and a `spot-instances-request` permission the
launch role does not have.

Fixed cost: staging's host grows from t4g.small to t4g.medium (+USD 0.0168/h,
about USD 12/month), because the SkyPilot server is a Python process tree
capped at 2 GB and the small instance has 2 GB in total. The reaper, its
alarms and SNS cost cents.

## Consequences

- A worker cannot autodown itself: its role can do nothing. Teardown is
  compute-api's explicit `/down` (backend.md), and the reaper backs it up at
  26 hours. Backend.md treats SkyPilot's autodown as a backstop. Here that
  backstop is the reaper, which is coarser: a 1-hour job orphaned by a dead
  compute-api runs, at our cost, until hour 26 (about USD 26 on a g5.xlarge).
  Letting a node terminate *only itself* needs a condition binding the
  resource to `ec2:SourceInstanceARN`. That is not verified, so it is not
  here.
- A deny-all worker works for the first workload because of
  [ADR-0005](0005-artifact-storage.md): inputs are `https://` URLs fetched
  with curl in the run step, and outputs are uploaded by the job through
  presigned URLs that compute-api mints. Neither needs AWS credentials on the
  node. `batch-infer-vllm` reads `/mnt/in/requests.jsonl` and writes
  `/mnt/out/results.jsonl` that way. Its image (`vllm/vllm-openai`, Docker
  Hub) and model (Hugging Face, not gated) need only the open egress. An
  `s3://` input or mount would need credentials on the node, and that would
  be the first narrowing of the worker's Deny.
- The SkyPilot image is upstream's `berkeleyskypilot/skypilot:0.13.0`,
  pinned by digest, which is the multi-arch index with linux/arm64. It is
  built from the same commit as the `FelixSphere/skypilot` pin (tag v0.13.0,
  `b1431e5`), but by upstream, not by us. If ADR-0003's "consumed through the
  fork" has to mean "built by us", `publish-image.yml` has to grow a job that
  builds the fork into GHCR and ECR like our other images.
- A compute-api release now also waits on SkyPilot being healthy: host.sh
  verifies every container in the project. That is wanted, since a broken
  SkyPilot is a broken compute-api, but it couples the two.
- SkyPilot's state (clusters, users, tokens, the SSH key it drives workers
  with) is SQLite on `/data/skypilot`. It survives instance replacement with
  the data volume. It is not in the pre-release `pg_dump`, and staging takes
  no snapshots.
- The G/VT quota is shared with the rest of the account. If another product
  ever runs G instances, our cap becomes theirs too.

## Amendment: Start small (2026-10-03)

**Context.** There are no customers yet, and the operator will not accept a
sudden large bill. AWS answered the request for 8 vCPU of G instances by
suggesting we start smaller. The original decision above *alerted* on spend
and relied on someone reading the mail in time. Budget data lags spend by
hours, so an alert is not a cap. The operator decided that GPU spend is
**hard-capped**, not just alerted.

**Decision.** Four limits, each enforced by a different thing, so that no
one failure removes the cap:

| Limit | Value | Enforced by | Where |
|---|---|---|---|
| Concurrent GPU jobs, all accounts | **1** job, **1** GPU | compute-api admission, under one platform-wide advisory lock (`store.CreateAdmittedPlatform`) | `compute-api/config/platform.json` |
| Runtime of one job | **4 h** (was 24 h; R9 semantics unchanged) | compute-api submit validation and the backend's max_runtime stop | `platform.json` `max_runtime_seconds` |
| Age of any worker | **5 h** = ceiling + 1 h (was 26 h) | the reaper Lambda, outside the host | derived from `platform.json` in `modules/gpu-workers/reaper.tf` |
| Concurrent G instances, whole account | **4 vCPU** = one g5.xlarge | the EC2 quota `L-DB2E81BA` (to be requested; it is 0 now) | `docs/staging.md` |
| Monthly GPU spend | **USD 50** (variable) | an AWS Budgets **action**: at 100% actual, explicit Deny on `ec2:RunInstances` and `ec2:StartInstances` attached to the launch role, automatic, no approval | `deploy/aws/shared/gpu-budget` |

Plus, unchanged in intent but tightened: the launch role starts only
`g5.xlarge` (now also a module validation), and is explicitly denied spot
market launches and the fleet/spot request APIs. Before this, those were
refused only by the 0 spot quota.

How the budget action behaves:

- At 100% of actual spend, AWS Budgets assumes `unify-compute-gpu-budget-action`
  and attaches `unify-compute-gpu-budget-stop-launches` to
  `unify-compute-staging-skypilot`. That role can attach or detach that one
  policy, to the launch roles only. Running workers keep running until
  compute-api's `/down` or the reaper: Stop and Terminate are not denied, so
  teardown keeps working. New launches and restarts fail with
  `UnauthorizedOperation`.
- Alerts by email at 50%, 80% and 100% actual and at 100% forecast. The
  action mails its own subscriber when it runs.
- **It resets monthly.** IAM-policy actions return to standby at the start of
  each budget period, and the policy is detached then.
- **Lifting it within the month** is the operator's call. Raise
  `monthly_limit_usd` and apply the budget stack, then reverse the action:
  `aws budgets execute-budget-action --execution-type REVERSE_BUDGET_ACTION`.
  The action id is the stack's `budget_action_id` output. A reversed action is
  not evaluated again that month. Use `RESET_BUDGET_ACTION` to re-arm it.
  Detaching the policy in IAM also works, but leaves the action showing
  "completed" while nothing is attached.
- **Production** is not in `launch_role_names` while
  `gpu_workers_enabled = false`, because its role does not exist. The change that
  enables production's workers adds `unify-compute-production-skypilot` there.

**The overshoot bound.** The cap is not exact, because AWS Budgets sees spend
late. It refreshes about three times a day, so the lag L is typically 8–12 h,
and AWS allows up to 24 h for billing data. Between the moment real spend
crosses USD 50 and the moment the Deny lands, launches continue. After it
lands, a worker that started just before can run to the reaper's age A. So,
with concurrency c and the all-in worker rate r (USD 1.039/h: instance,
256 GB root, public IPv4):

    overshoot ≤ c × r × (L + A)
             = 1 × 1.039 × (12 + 5) ≈ USD 18   (typical lag)
             = 1 × 1.039 × (24 + 5) ≈ USD 30   (AWS's worst stated lag)

**Worst-case GPU compute spend is about USD 68–80 a month** with everything
working. If compute-api's platform limit were bypassed (a bug, or a misuse of
the launch role), the 4-vCPU quota still holds c at 1 account-wide, and the
bound is the same. With the 8 vCPU first proposed, c would be 2 and the
overshoot USD 35–60.

What the bound depends on, stated so nobody reads more into it:

- **The `Project` cost allocation tag must stay active** (`docs/staging.md`).
  It read Active on 2026-10-03. If it is ever switched off, the budget sees
  nothing and the action never fires. Then only
  concurrency limits spend: 1 × 1.039 × 730 h ≈ **USD 758 a month** at most,
  with the reaper ending each worker by hour 5 but not stopping the next
  launch.
- **The quota must be granted at 4, not more.** It is account-wide, so it is
  the only limit that does not depend on compute-api.
- **Data transfer out is not capped by any of this.** Egress is billed per
  GB (USD 0.09 after the first 100 GB). It is tagged to the instance, so the
  budget counts it, but late, and the Deny stops launches, not a running
  worker's traffic. A worker uploading flat out for its 5 hours could move
  terabytes. No open egress limit exists. If customers' code ever runs
  before one does (an egress proxy, or a NetworkOut alarm that the reaper
  acts on), this is the term to watch. Today the only workloads are ours.

**Consequences.** One customer job at a time, platform-wide; a second
account's submit is refused with `403 quota_exceeded` and a
`platform_max_active_jobs` violation, with headroom, the same shape as a
per-owner refusal. A 4-hour job is the longest anyone can buy. An orphaned
worker now costs at most about USD 5 (was about USD 26). Raising any of
these numbers is a reviewed change to `platform.json`, the quota and the
budget together, recorded here.

The per-owner defaults in `quotas.json` stay at 2 jobs and 2 GPUs. Existing
tests pin them, and with the platform cap at 1 they cannot bind. The
effective per-owner limit is 1.

## When to move to a separate account

Move the workers to their own account in an AWS Organization when any of
these becomes true:

- **Production customer workloads.** Untrusted code at scale in the account
  that holds four products' production data is a blast radius that tags
  mitigate but do not remove. An account boundary does not depend on every
  policy being right.
- **Spend that one budget cannot watch.** GPU quota beyond a few instances,
  or a second GPU family, so that quota requests, budgets and Cost Explorer
  need to be separate from the other products'.
- **Compliance.** A customer asks for account-level isolation, or SOC 2 scope
  should exclude the other products.
- **A second operator team.** Someone needs administrative access to workers
  without access to the other products.

The move is mostly mechanical. The launch role becomes a cross-account role
in the workers account, trusted by the compute host's role. The module moves
unchanged, and the SSH path is already over public IPs, so no peering has to
be unwound.

## Not decided here

- ~~The quota to request.~~ 4 vCPU, one g5.xlarge (Start small, 2026-10-03).
- ~~The budget amount (USD 300).~~ USD 50 with an automatic stop (Start
  small). The alert address is still the operator's to set.
- ~~Closing IMDS to every container but compute-api.~~ Done by ADR-0005's
  amendment of 2026-10-03, for compute-api too.
