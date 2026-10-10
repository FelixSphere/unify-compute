# ADR-0012 — A brokered template marketplace

- **Status:** Proposed
- **Date:** 2026-10-10
- **Amends, once accepted:** [ADR-0007](0007-product-surfaces-and-phasing.md),
  the "Anything two-sided" line under *Left*, for templates only. Its
  exclusion of a two-sided **supply** market (host onboarding, bidding, host
  reliability) stands unchanged.
- **Related:** [`spec/templates.md`](../spec/templates.md) (what a template is),
  [`spec/marketplace.md`](../spec/marketplace.md) (the contract this ADR
  decides), ADR-0006 (upstreams; who supplies GPUs), ADR-0010 (GPU workers;
  open egress), ADR-0011 (accounts and balance). The model is Unify Data's
  ADR-0011, *A brokered market through one super tenant* (2026-10-02), applied
  to job templates instead of data sets.

Phase M1 below is built in this change. M2 and M3 are decisions about work
that does not exist yet.

## Context

On 2026-10-10 the operator asked for a Unify Compute marketplace "like Unify
Data's" and chose, when asked:

| Question | Operator's answer |
|---|---|
| What is sold | **Templates.** Not GPU capacity from third-party hosts |
| Who lists | **Brokered, as in Unify Data.** Any account may submit; the super admin approves every listing; ours are approved by construction |
| Money | **The buyer pays GPU time only**, at the SKU's rate. No listing fee, no seller share, no payouts |
| This round | This ADR, the spec, and a public browse page over the templates we already ship |

What exists on 2026-10-10:

- Four curated templates in `compute-api/config/templates.json`, served at
  `GET /compute/v1/templates` and accepted by `POST /compute/v1/jobs`
  (`spec/templates.md`). They are reviewed as code. None has run on a real
  cloud.
- The site lists templates only as a `curl` line on `/start`. The dashboard
  has no template or job-submit UI.
- Any account with compute enabled can already run **any** image through a
  hand-written spec. A third-party template therefore adds no new way to run
  code on our workers. What it adds is a **name we vouch for**, and a buyer
  who puts their own data into someone else's image.
- GPU workers have open egress (ADR-0010). Nothing stops an image from
  sending its inputs elsewhere.
- Admin is the `compute-api admin` CLI. There is no web admin.
- Unify Data's market (Unify Data ADR-0011, 0008, `design/market-browsing.md`):
  - any workspace lists, every listing is `pending` until the super admin
    approves it, and any edit sends it back to `pending`; unlisting is never
    blocked;
  - the platform workspace's own listings are approved automatically;
  - neither side learns who the other is ("Sold through UnifyData");
  - signed-out visitors may browse; looking is not buying.

## Decision

### 1. The marketplace sells templates, and only templates

A listing is a template (`spec/templates.md`): a digest-pinned image, a
command, env defaults, a default SKU, declared inputs and outputs, and the
fields a submit may override. Running a listing is a job like any other:
held, metered and settled at the SKU's rate (`spec/metering.md`).

GPU supply stays ours. We choose the upstreams (ADR-0006); nobody lists GPUs.

### 2. Brokered, with one publisher of record

- **Platform listings** are `templates.json`. Approved by construction,
  because a change to that file is code-reviewed like a SKU change.
- **Account listings** (M2) are submitted by an account, start `pending`,
  and appear only once the super admin approves them with
  `compute-api admin listing approve <id> --reason`. Any change to an
  approved listing's image, command, env, SKU, mounts or overridable fields
  returns it to `pending`. Its title and description do too: buyers trust
  those words. The publisher may always unlist, and unlisting is never blocked.
- **The buyer sees "Unify Compute" as publisher** on every listing. The
  publisher sees counts of runs, never who ran them. This is Unify Data's
  rule, for the same reason: the market is ours, and so is the trust a
  buyer gives a listing.

### 3. Money: GPU time only

Running a listing costs exactly what the same spec costs hand-written. There is no
listing fee, no seller share, no payout ledger and no Stripe Connect. A
publisher gets distribution, not revenue. Paying publishers is a new ADR,
because it brings payouts, tax forms and fraud with it.

### 4. Third-party listings wait for egress control

Account listings (M2) do not open to buyers until **one** of these is true:

- workers running a third-party listing have egress limited to an allow-list
  (model and package registries, our artifact bucket); or
- review requires the image to be built by **our** pipeline from a public
  source repository at a pinned commit, and the reviewer has read that
  source.

Until then a buyer's data would go into an image nobody but its author has
read, on a worker that can send it anywhere. The approval queue may be
built and exercised on staging before either is true; production approval
may not.

### 5. Browsing is public; running needs an account

`/marketplace` and `/marketplace/<id>` on compute.unifyai.us are public and
static, like `GET /templates`. They show what a listing does, what it reads
and writes, the GPU it runs on, what a submit may change, and where the image
comes from. **They show no prices** until the SKU catalogue's prices stop
being placeholders. AGENTS.md forbids site copy that promises pricing.

### Phases

| Phase | What | State |
|---|---|---|
| **M1** | Public browse and detail pages, generated at build time from `templates.json` and `skus.json`; each detail page shows the exact `POST /jobs` call that runs it | **Built in this change** |
| **M2** | `listings` table, submit and unlist on `/dashboard/v1`, review verbs on the admin CLI, `GET /templates` serving approved account listings beside platform ones, the job recording its template id | Not started. Blocked for production by decision 4 |
| **M3** | Dashboard screens: browse, "Run this" form, "My listings" with review state | Not started |

## Consequences

- ADR-0007's *Left* section is amended for templates only, once this ADR is
  accepted. Per AGENTS.md, ADR-0007's status line records that; its reasoning
  is not edited.
- **The site's browse pages are generated at build time**, not fetched at
  runtime. The pages need no CORS, no CSP `connect-src` and no JavaScript.
  The cost: `release.yml` ships a set of image digests, and either image may
  ship alone. **A change to `templates.json` or `skus.json` therefore needs a
  site release as well as an API release**, or the pages describe the
  previous catalogue. The CI/CD agent's handoff for such a change must name
  both. Once M2 exists, the public pages fetch account listings from
  `GET /templates` at runtime, or they go stale between releases. That choice
  is M2's to make.
- The template id is not yet recorded on the job (`spec/templates.md`,
  *Planned*). M2's run counts need it, so M2 adds it first.
- Taxonomy, search and sorting (Unify Data's `market-browsing.md`) wait until
  there are enough listings for a list to be hard to read. Four are not.
- Nothing here changes what a job costs, what a key may do, or what runs on
  a worker.
