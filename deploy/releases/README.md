# Release records

One file per release, rollback or failed attempt that touched a host, named
`YYYY-MM-DD-<name>.md` (lowercase, digits, hyphens). **No release has happened
yet; this directory holds only this README.**

A pushed commit is not a release, and neither is a green run until someone
has written down what it shipped and what was checked. The procedure that
produces these runs is in [`docs/staging.md`](../../docs/staging.md#releasing);
the release workflow prints a pre-filled field block in its run summary.

## What CI enforces (`scripts/release/check-records.sh`)

- every file here except this README is named `YYYY-MM-DD-<name>.md` and
  starts with the field block below, with valid values;
- every **successful** `release.yml` run (`release` or `rollback`, from
  `main`) that finished more than 24 hours ago is cited by some record's
  `run:`. After that, every PR fails until the record is merged;
- a run whose reason started with `HOTFIX` has a record with `hotfix: true`
  and a `## What was skipped` section.

Failed runs are not enforced, because CI cannot tell whether one reached the
host. Write one anyway when it did: a failed cutover was automatically rolled
back, and the next person needs to know that it happened and why.

## The field block

```markdown
---
environment: staging            # staging | production
action: release                 # release | rollback
outcome: success                # success | failed | rolled-back
run: 12345678901                # the release.yml run id
commit: <40 hex>                # the workflow commit (the scripts that ran)
compute_api_image: ghcr.io/felixsphere/unify-compute-api@sha256:<64 hex>
site_image: ghcr.io/felixsphere/unify-compute-site@sha256:<64 hex>
previous_compute_api_image: <ref, or none>
previous_site_image: <ref, or none>
hotfix: false                   # true needs a "## What was skipped" section
---
```

For a failed attempt, the image fields are what was **attempted**, and
`outcome: rolled-back` means the host restored the previous images itself.

## What the prose should say

The field block is for the machine. The prose is for the person deciding
the next release:

- **What shipped**: the commits between the previous and this compute-api
  build, and the same for the site, one line each. A price change
  (`compute-api/config/skus.json`) is called out on its own.
- **What was exercised**: the smoke output, plus anything this release
  specifically changes, run rather than read about.
- **Before / after**: the `status` read before dispatch, the backup path and
  its S3 copy, and the container restart counts the cutover printed.
- **What this does not prove**: be specific. "Smoke passed" proves the routes
  answer; it does not prove that a job can be billed.
