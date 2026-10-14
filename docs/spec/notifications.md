# Notifications: Unify Compute customer email for job outcomes

- **Status:** Built for Compute's own accounts (ADR-0011,
  `ACCOUNTS_BACKEND=local`), task W2, 2026-10-14. Not yet released. Compute
  sends the three job outcome emails below and "Top-up received"
  (`accounts.md`, "Emails") itself, through SES, each once. With console
  accounts nothing here is sent: the addresses belong to the console.
- Depends on: [`job-api.md`](job-api.md) (job states, `error.code`, the
  `usage` object), [`metering.md`](metering.md) (what is billed, money format,
  holds H1–H7), [ADR-0008](../adr/0008-zero-balance-policy.md) (zero-balance
  policy, **Proposed**), [ADR-0011](../adr/0011-compute-owns-accounts-and-billing.md) (Compute's
  own accounts), and the shared UnifyAI email kit
  (`~/unifyai-brand-kit/email/README.md`).
- Layout: [`email/base.html`](email/base.html), a verbatim copy of the kit's
  `base.html` taken on 2026-10-01. The mailer renders it from
  `compute-api/internal/mailer/layout.html`.

## What exists and what does not

| Piece | State |
|---|---|
| Messages | Built: `compute-api/internal/mailer/messages_jobs.go` (job succeeded, job failed, job stopped for billing, top-up received). Golden files in `internal/mailer/testdata/job-*.golden` and `topup-received*.golden`. Low balance (W6): `messages_balance.go`, `low-balance.*.golden`; when it fires is `accounts.md`, "Low-balance warning". |
| Sending | Built: SES v2 raw email with the instance role (`internal/mailer`), multipart plain text plus HTML; `MAIL_OUTBOX_DIR` `.eml` files with `ENVIRONMENT=development`. |
| Recipients | Every `active` user of the account with role `owner` (`notify.Notifier.Owners`). Today an account has one user, its owner. A `pending` user (address not verified) gets nothing. |
| Trigger: jobs | The terminal transition in `internal/jobs` (`backendStore.UpdateStatus`, `endUntracked`), only in the process that drives jobs. The send runs in the background and never holds up or fails the transition. |
| Trigger: top-up | After the webhook's credit commits (`internal/payments/webhook.go`), after the receipt lookup, so the email carries Stripe's `receipt_url` when the lookup found one. |
| Sent once | Built: the `notifications` table (migration `0011_notifications.sql`) and `internal/notify`. See [Sent once](#sent-once). |
| Job page the CTA opens | `{APP_ORIGIN}/jobs/{job_id}`, e.g. `https://app.compute.unifyai.us/jobs/job_…` (the dashboard's job page; `deploy/site/Caddyfile` rewrites it to `job.html`). The operator's 2026-10-01 decision named `compute.unifyai.us/jobs/{id}`; the job page was built on the app host instead. |
| Sender | `Unify Compute <no-reply@compute.unifyai.us>` (`SES_SENDER`), as the account emails. |
| Not built | An email at the first refused extension (H4), `interrupted`, the storage arrears messages of ADR-0008, refunds and disputes (W3), invitations (W4). |

## Messages

The tables in this section are the copy as first specified for the console
to send. The built text is in the golden files
(`compute-api/internal/mailer/testdata/`), and
[Copy changes from the original spec](#copy-changes-from-the-original-spec)
lists where it differs and why.

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
| `{{product_name}}` | `Unify Compute`, the full product name (user decision, 2026-10-01). Used in the footer line. |
| `{{product_short}}` | `Compute`, the header descriptor after the UnifyAI lockup. |
| `{{product_url}}` / `{{product_domain}}` | `https://compute.unifyai.us` / `compute.unifyai.us` |
| `{{footer_reason}}` | "You're receiving this because a job on your Unify Compute account ended." |
| Focal element | Option (a), the CTA button: amber `#F5A623` with **ink `#0E0E0E` text**, exactly as in `base.html`. Never white on amber. Amber appears only on this button. |
| `{{cta_url}}` | `{APP_ORIGIN}/jobs/{job_id}`, the dashboard's job page. The raw link under the button is the same URL. |

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
reason, and the last line "© UnifyAI · Operated by FelixSphere LLC · 6 Karen
Ct, CA 94010, United States", as in `docs/spec/email/base.html`). FelixSphere
LLC is the sender of record (UI-STANDARD, 2026-10-03); the footer carries no
FelixSphere tagline or logo. The plain text
must read correctly by itself with images blocked. The subject names the
product and the action, and the preheader adds information the subject does
not carry.

### 1. Job finished

| Part | Content |
|---|---|
| Subject | `Unify Compute: job {job id} finished` |
| Preheader | `Ran {gpu_seconds} GPU-seconds on {gpu}. Outputs are ready to download.` |
| Eyebrow | `JOB FINISHED` |
| Headline | `Your job finished.` |
| Intro | `{job id} exited successfully. Its outputs are kept until you delete them, and storage is charged daily while they exist.` |
| CTA label | `View job` |
| Detail rows | Job, GPU, GPU-seconds, Cost (shared rows above). `Status` row: `succeeded`, with the success dot `#1E9E63` beside the word. Status colour is used only for the status word, as the kit says. |
| Safety line | `You're getting this because a job you submitted finished. Cost is the GPU charge only; storage and egress for its outputs are billed daily from your Unify API balance. If you don't recognise this job, revoke the key that submitted it in the Unify API console.` |

### 2. Job failed

| Part | Content |
|---|---|
| Subject | `Unify Compute: job {job id} failed` |
| Preheader | One sentence with the `error.message` from the Job. If the job never reached `UP`, add: `No GPU time was charged.` |
| Eyebrow | `JOB FAILED` |
| Headline | `Your job failed.` |
| Intro | B7, non-zero exit after `UP`: `{job id} exited with an error after {gpu_seconds} GPU-seconds. Logs are kept for 7 days.` B8, before `UP`: `{job id} could not start, so no GPU time was charged and its hold was released in full.` |
| CTA label | `View job and logs` |
| Detail rows | Job, GPU, GPU-seconds, Cost (B8 shows `0` and `USD 0.0000`). `Status`: `failed`, with the danger dot `#D92D20`. `Reason`: the Job's `error.code` as returned, e.g. `capacity_exhausted` (`metering.md` B8). The email prints the code and does not translate it. |
| Safety line | `You're getting this because a job you submitted failed. Logs are retained 7 days after the job ends. If you don't recognise this job, revoke the key that submitted it in the Unify API console.` |

### 3. Billing stopped the job (hold or settlement failed)

One template with two variants keyed on `error.code`. Both are `cancelled` jobs
that compute itself stopped (`job-api.md`; `metering.md` H4 and H5).

| Part | `balance_exhausted` (H4, B12) | `billing_unavailable` (H5, B13) |
|---|---|---|
| Subject | `Unify Compute: job stopped, balance exhausted` | `Unify Compute: job stopped, billing unavailable` |
| Preheader | `Your Unify API balance could not cover more GPU time for {job id}.` | `We couldn't reach billing for {job id}, so we stopped it before it overspent its hold.` |
| Eyebrow | `JOB STOPPED` | `JOB STOPPED` |
| Headline | `Your job was stopped: balance exhausted.` | `Your job was stopped: billing unavailable.` |
| Intro | `The hold for {job id} could not be extended, so we stopped it before it ran past what your balance covers. You are charged only for the GPU-seconds it ran.` | `Billing could not be reached for long enough that {job id}'s hold was about to run out, so we stopped it. You are charged only for the GPU-seconds it ran. This was an outage on our side, not a problem with your balance.` |
| CTA label | `View job` | `View job` |
| Detail rows | Job, GPU, GPU-seconds, Cost. `Status`: `cancelled`, with the warning dot `#E0A100`. `Reason`: `balance_exhausted`. | Same, with `Reason`: `billing_unavailable`. Cost is what the ledger accrued. The final settle is retried until the console answers (B13), so the email must not say "charged". It says `Cost (pending settlement)`. |
| Safety line | `You're getting this because a job you submitted was stopped for billing. Top up your Unify API balance before resubmitting. If you don't recognise this job, revoke the key that submitted it in the Unify API console.` | `You're getting this because a job you submitted was stopped for billing. Nothing is needed from you. Resubmit when you're ready. If you don't recognise this job, revoke the key that submitted it in the Unify API console.` |

The focal element stays the job link in both variants. The top-up action lives
in the Unify API console. A second button would put amber on the page twice, so
the safety line names the action as text instead.

## Sent once

Each message is sent at most once per thing it is about, however often its
trigger fires: Stripe redelivering an event or sending
`async_payment_succeeded` after `completed`, the backend observing an end
again after a restart, or two processes on one database.

The record is the `notifications` table, keyed on
`(account_id, subject_kind, subject_id, message)`:

| Message | `subject_kind` | `subject_id` | `message` |
|---|---|---|---|
| Job finished | `job` | job id | `job-succeeded` |
| Job failed | `job` | job id | `job-failed` |
| Billing stopped the job | `job` | job id | `job-billing-stopped` |
| Top-up received | `topup` | top-up id (`tu_…`) | `topup-received` |
| Low balance | `balance` | the balance entry (`be_…`) that crossed the threshold | `low-balance` |

The row is a **claim, written before the first address is mailed**. A second
trigger finds it and sends nothing. The outcome is then written on the row:

- `sent` (with `sent_at`) once SES accepted at least one address;
- `failed` (with `last_error`) when SES refused every address. A refusal is a
  definite answer, so the next trigger for the same key claims the row again,
  up to 5 attempts (`notify.MaxAttempts`);
- a row left `claimed` means the process stopped between the claim and SES's
  answer. It is never retried: whether the mail went out is unknown, and a
  missing email is better than a second copy. This is Unify Data's rule for
  its own sends (`updates.py`, "A send happens once").

The guarantee is therefore at most once. For jobs, a trigger fires once per
transition, so a refused job email is not retried by itself today; a
top-up's is retried only if Stripe delivers another event for that session.
A sweep that retries `failed` rows is not built.

A mail failure never fails what triggered it: the job transition, the
webhook's `200`, and the credit all stand, and the failure is logged with the
message kind, subject and account.

### For later messages

`internal/notify` knows nothing about message content. A new message picks
a `subject_kind` (`refund`, `invitation`, `balance`, …) and calls:

```go
n.ToOwners(ctx, notify.Key{AccountID: id, Subject: "refund", SubjectID: refundID, Message: "refund-issued"},
	func(ctx context.Context, to string) error { return mail.RefundIssued(ctx, to, r) })
```

or `n.To(ctx, key, []string{addr}, send)` for an address that is not an
owner (an invitation). A message that can recur about the same thing (a
low-balance warning per downward crossing) puts the occurrence in
`subject_id`. No migration is needed for a new kind.

## Copy changes from the original spec

The tables above were written when the console was to send these emails.
What the built messages say instead, because it is what is true for
Compute's own accounts:

- "Unify API console" and "Unify API balance" become the Unify Compute
  dashboard (its Keys and Billing pages), as `accounts.md` says.
- The footer reason is "You're receiving this because a job on your Unify
  Compute account ended." The top-up email uses the account reason.
- No line promises log or output retention or daily storage charges:
  logs are not kept after teardown (`backend.md` open question 4) and storage
  and egress metering is not built. The succeeded intro says the job page
  shows its events and any outputs it uploaded.
- The job failed email adds one plain-language sentence for each known
  `error.code` (`mailer.FailureLine`) and prints the code untranslated as the
  Reason row. The raw `error.message` is not copied into the email: it can
  carry SkyPilot's text, and the job page shows it.
- The CTA label of the failed email is `View job`.
- The status row is the first detail row, the status word with the kit's dot.
- Cost is the settled GPU charge once the hold is closed. While the final
  settle is pending (always for `billing_unavailable`) it is what the ledger
  accrued, labelled `Cost (pending settlement)`.

## Open points

1. **More messages.** An early warning at the first refused extension (H4,
   about 50 minutes before the cancel), and `interrupted` (B9).
2. **Retrying refused job emails.** A `failed` row is retried only when the
   same trigger fires again, which for a job it does not. A sweep over
   `failed` rows is the fix if SES refusals turn out to happen.
3. **Console accounts.** The `notify` bridge verb this spec once proposed is
   moot while every host runs `ACCOUNTS_BACKEND=local`. Nothing is sent to
   console-owned jobs.
