# Notifications: customer email for job outcomes

- **Status:** Spec, nothing built. Compute sends no mail today. No code in this
  repository builds, renders or sends email, and this document does not add
  any.
- Depends on: [`job-api.md`](job-api.md) (job states, `error.code`, the
  `usage` object), [`metering.md`](metering.md) (what is billed, money format,
  holds H1–H7), [ADR-0008](../adr/0008-zero-balance-policy.md) (zero-balance
  policy, **Proposed**), and the shared UnifyAI email kit
  (`~/unifyai-brand-kit/email/README.md`).
- Layout: [`email/base.html`](email/base.html), a verbatim copy of the kit's
  `base.html` taken on 2026-10-01 (the revision without the FelixSphere
  tagline). Copy it again when the kit changes rather
  than editing it here.

## What exists and what does not

| Piece | State |
|---|---|
| Layout (`docs/spec/email/base.html`) | Copied from the kit. Not wired to anything. |
| Logo PNGs (`public/email/`) | Served by the marketing site at `https://compute.unifyai.us/email/…` once this branch is released. On the local production build (`wrangler dev`) the kit's `@2x` file names answer `307` to the `%402x` spelling, which then returns `200`. Image proxies normally follow that redirect, but check the live URLs once before the first send. |
| Customer email addresses | **Compute does not have them.** The UnifyAPI console owns accounts and contact details (ADR-0001). |
| Delivery path | **Open.** See [Delivery](#delivery-open). |
| Job web page for the CTA to open | **Does not exist.** See [Open points](#open-points). |
| Sender name and address | **Not decided.** See [Open points](#open-points). |

## Messages

Three messages, all transactional, all from terminal job states that
`job-api.md` already defines. No new state or `error.code` is introduced here.

| Message | Fires on | Source in the specs |
|---|---|---|
| Job finished | `status` becomes `succeeded` | `job-api.md` status diagram; `metering.md` B6 |
| Job failed | `status` becomes `failed` | `job-api.md` status diagram; `metering.md` B7 (after `UP`) and B8 (before `UP`) |
| Billing stopped the job | `status` becomes `cancelled` with `error.code = balance_exhausted` or `billing_unavailable` | `job-api.md` "Metering and settlement"; `metering.md` H4, H5, B12, B13; ADR-0008 |

Out of scope:

- **Customer cancels.** The customer made the cancel request, so no email is sent.
- **`interrupted`.** `metering.md` B9 bills `interrupted`, but nobody has asked
  for an email on it. This is an open point.
- **Hold refused at submit.** The `402` goes straight back to the caller and no
  job is persisted (`job-api.md` `POST /jobs`), so there is no job to email
  about.
- **Extension refused before the cancel.** H4 says compute "emits a job event"
  at the first refusal, which gives the customer about 50 minutes to top up. An
  email at that moment would be the most useful warning, but it is a fourth
  message. It is listed under open points and not specified here.
- **Storage arrears and deletion** (ADR-0008). ADR-0008's open point 1 already
  covers these.

### Shared slots

These values are the same in every message.

| Slot in `base.html` | Value |
|---|---|
| `{{lang}}` / `{{dir}}` | `en` / `ltr` (compute has no other locale) |
| `{{asset_base}}` | `https://compute.unifyai.us/email` |
| `{{product_name}}` | `Compute`, as the brand owner asked. See the naming note under open points. |
| `{{product_url}}` / `{{product_domain}}` | `https://compute.unifyai.us` / `compute.unifyai.us` |
| `{{footer_reason}}` | "You're receiving this because a job you submitted with your UnifyAPI key changed state." |
| Focal element | Option (a), the CTA button: amber `#F5A623` with **ink `#0E0E0E` text**, exactly as in `base.html`. Never white on amber. Amber appears only on this button. |
| `{{cta_url}}` | The job's page, `…/jobs/{id}`. That page does not exist yet (open points). The raw link under the button is the same URL. |

All three messages use these detail rows. Values are mono, as in the layout.

| Label | Value | Source |
|---|---|---|
| Job | `job_01J8Q6ZK3R7Y9W2X4V6T8S0A1B` | `id` |
| GPU | `a10g-24gb-x1 · NVIDIA A10G` | `sku`, then the SKU's `gpu` (`job-api.md` SKU object) |
| GPU-seconds | `1830` | `usage.gpu_seconds`, an integer |
| Cost | `USD 0.6100` | The GPU charge, a decimal string in USD with 4 places, never a float (`job-api.md` "Money is a decimal string"). Take it from `usage.cost_usd.gpu` once the `metering.md` cost breakdown lands. Until then, use `usage.settled_cost_usd`, which is GPU-only today. |

Cost shows only the GPU charge. The job's storage and egress keep accruing
after the terminal state and are collected daily (`metering.md` R5), so a total
in the email would be wrong by the next morning. The safety line says so.

**Plain-text part (required for every message).** The kit requires a
`text/plain` alternative with the same content: the eyebrow, headline, intro,
CTA label with the full URL on its own line, every detail row as
`Label: value`, the safety line, and the footer (product · domain, the
reason, and the company address line). The footer carries no FelixSphere
tagline, as UI-STANDARD requires. The plain text
must read correctly by itself with images blocked. The subject names the
product and the action, and the preheader adds information the subject does
not carry.

### 1. Job finished

| Part | Content |
|---|---|
| Subject | `Compute job finished: {job id}` |
| Preheader | `Ran {gpu_seconds} GPU-seconds on {gpu}. Outputs are ready to download.` |
| Eyebrow | `JOB FINISHED` |
| Headline | `Your job finished.` |
| Intro | `{job id} exited successfully. Its outputs are kept until you delete them, and storage is charged daily while they exist.` |
| CTA label | `View job` |
| Detail rows | Job, GPU, GPU-seconds, Cost (shared rows above). `Status` row: `succeeded`, with the success dot `#1E9E63` beside the word. Status colour is used only for the status word, as the kit says. |
| Safety line | `You're getting this because a job you submitted finished. Cost is the GPU charge only; storage and egress for its outputs are billed daily from your UnifyAPI balance. If you don't recognise this job, revoke the key that submitted it in the UnifyAPI console.` |

### 2. Job failed

| Part | Content |
|---|---|
| Subject | `Compute job failed: {job id}` |
| Preheader | One sentence with the `error.message` from the Job. If the job never reached `UP`, add: `No GPU time was charged.` |
| Eyebrow | `JOB FAILED` |
| Headline | `Your job failed.` |
| Intro | B7, non-zero exit after `UP`: `{job id} exited with an error after {gpu_seconds} GPU-seconds. Logs are kept for 7 days.` B8, before `UP`: `{job id} could not start, so no GPU time was charged and its hold was released in full.` |
| CTA label | `View job and logs` |
| Detail rows | Job, GPU, GPU-seconds, Cost (B8 shows `0` and `USD 0.0000`). `Status`: `failed`, with the danger dot `#D92D20`. `Reason`: the Job's `error.code` as returned, e.g. `capacity_exhausted` (`metering.md` B8). The email prints the code and does not translate it. |
| Safety line | `You're getting this because a job you submitted failed. Logs are retained 7 days after the job ends. If you don't recognise this job, revoke the key that submitted it in the UnifyAPI console.` |

### 3. Billing stopped the job (hold or settlement failed)

One template with two variants keyed on `error.code`. Both are `cancelled` jobs
that compute itself stopped (`job-api.md`; `metering.md` H4 and H5).

| Part | `balance_exhausted` (H4, B12) | `billing_unavailable` (H5, B13) |
|---|---|---|
| Subject | `Compute job stopped: balance exhausted` | `Compute job stopped: billing unavailable` |
| Preheader | `Your UnifyAPI balance could not cover more GPU time for {job id}.` | `We couldn't reach billing for {job id}, so we stopped it before it overspent its hold.` |
| Eyebrow | `JOB STOPPED` | `JOB STOPPED` |
| Headline | `Your job was stopped: balance exhausted.` | `Your job was stopped: billing unavailable.` |
| Intro | `The hold for {job id} could not be extended, so we stopped it before it ran past what your balance covers. You are charged only for the GPU-seconds it ran.` | `Billing could not be reached for long enough that {job id}'s hold was about to run out, so we stopped it. You are charged only for the GPU-seconds it ran. This was an outage on our side, not a problem with your balance.` |
| CTA label | `View job` | `View job` |
| Detail rows | Job, GPU, GPU-seconds, Cost. `Status`: `cancelled`, with the warning dot `#E0A100`. `Reason`: `balance_exhausted`. | Same, with `Reason`: `billing_unavailable`. Cost is what the ledger accrued. The final settle is retried until the console answers (B13), so the email must not say "charged". It says `Cost (pending settlement)`. |
| Safety line | `You're getting this because a job you submitted was stopped for billing. Top up your UnifyAPI balance before resubmitting. If you don't recognise this job, revoke the key that submitted it in the UnifyAPI console.` | `You're getting this because a job you submitted was stopped for billing. Nothing is needed from you. Resubmit when you're ready. If you don't recognise this job, revoke the key that submitted it in the UnifyAPI console.` |

The focal element stays the job link in both variants. The top-up action lives
in the UnifyAPI console. A second button would put amber on the page twice, so
the safety line names the action as text instead.

## Delivery (open)

Compute cannot email a customer today. It never sees an address: the console
owns contact details (ADR-0001), and ADR-0008's consequences say this "must be
resolved before the first paid customer". The named options are listed below.
None is chosen here.

- **The console sends.** A new bridge verb asks the console to email a user.
  ADR-0008 open point 1 names it as `POST /api/compute/v1/notify`, "not
  proposed here, only named", and `metering.md` lists it as optional. The
  console's existing sender is `console/common/email.go` over SES, per the kit
  README. Because of ADR-0002, the console-side half would be new AGPL code in
  the console repository, and compute would send it only the template data.
- **Compute sends.** Compute would need the address from the console, which
  is new data crossing the bridge, plus its own mail provider and sending
  domain.
- **Neither, for now.** Job events and webhooks (ADR-0007 P1) carry the same
  information, and email waits.

Whichever path is chosen, the `{{…}}` values above are the contract. The
rendered HTML and the plain-text part come from `base.html` with those slots
filled.

## Open points

Each of these is for the user or the brand owner to decide.

1. **Sender display name and address.** The kit's rule is `<product name>
   <no-reply@…>`, e.g. `Compute <no-reply@compute.unifyai.us>`. No mail domain
   is set up for `compute.unifyai.us`, and if the console sends, its own SES
   identity may dictate the address.
2. **Delivery path.** See the section above.
3. **Product name in the header and sender.** The brand owner asked for
   `Compute`. The kit README's rule is "`{{product_name}}` is the product's
   current name", and the current name is not uniform. The repository and
   docs say `unify-compute`, the kit README lists `unify-compute`, the
   marketing site's title says `unifyAI`, and the unmerged
   `feat/cross-terminal` branch calls it `UnifyCompute`. `Compute` does not
   contradict any of these, since it reads as the descriptor after the
   UnifyAI lockup, but they should be settled together.
4. **Where `{{cta_url}}` points.** No customer UI shows a job yet. It could be
   a future console page, a compute dashboard, or the API URL. The API URL
   needs a key and is not a page.
5. **More messages.** An early warning at the first refused extension (H4, about
   50 minutes before the cancel), and `interrupted` (B9).
