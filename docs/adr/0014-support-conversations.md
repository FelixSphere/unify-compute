# ADR-0014 — Customer support conversations

- **Status:** Accepted (operator's request, 2026-11-21; the design below is
  this ADR's)
- **Date:** 2026-11-21
- **Builds on:** [ADR-0011](0011-compute-owns-accounts-and-billing.md)
  (Compute owns its accounts, so it owns the conversation with them) and
  [ADR-0002](0002-compute-service-is-separate-and-proprietary.md) (nothing is
  taken from the AGPL console).
- **Related:** [`spec/accounts.md`](../spec/accounts.md) ("Support
  conversations", "Admin CLI", "Emails").

## Context

A Unify Compute customer could reach a person only by email: the dashboard's
Help menu has a `mailto:` link, and refund requests, bonus credit and listing
reviews are forms about one thing each. On 2026-11-21 the operator asked that
a customer conversation handed to a person reach him on Slack and that he can
answer from the Slack thread, as it already works for Unify API and Unify
Data (`FelixSphere/unifyai-workspace`, `scripts/incident/slack.py`). That
bridge needs three things from a product: the conversations waiting for a
person, their messages, and a staff reply path that tells the customer.
Compute had none of them.

Configuration cannot provide it (AGENTS.md, "Configuration before code"):

- No environment setting, Terraform variable or SSM parameter of either host
  turns on a conversation; there is no table or route behind one.
- HubSpot live chat (portal on the marketing site, `app/shell.tsx`) is a
  setting away from being shown, but only on `compute.unifyai.us`: the app
  host's CSP admits no third-party script (`deploy/site/Caddyfile`,
  `smoke.sh` fails on one), a visitor there is anonymous rather than an
  account, and the HubSpot token the workspace holds has contact scopes
  only, so nothing could read or answer a chat from the bridge.
- The existing `mailto:` stays. It cannot be listed, threaded or answered
  through a product path.

## Decision

1. **A conversation belongs to one user.** `support_conversations` and
   `support_messages` (migration 0016). What a member writes to support is
   not shown to the account's other members, and the reply email goes to
   the person who wrote. A user has at most one conversation that is not
   closed; writing again continues it. A closed one is history, and the next
   message starts a new conversation.

2. **The state says whose turn it is.** `open`: the newest message is the
   customer's and a person owes an answer. `answered`: the newest message is
   staff's. `closed`: ended by either side. "Waiting for a person" is exactly
   `state = 'open'`, which is what the operator's tooling polls.

3. **The customer's side is five session routes** under `/dashboard/v1`
   (`GET|POST /support/conversations`, `GET …/{id}`, `POST …/{id}/messages`,
   `POST …/{id}/close`) and one page, `/support`, in the sidebar and the Help
   menu. Any signed-in user may write, whatever the account's status: a
   suspended account most needs to reach a person. Writes are limited to 20
   an hour per user, messages to 4,000 characters of plain text.

4. **The operator's side is the admin CLI**, the only admin surface this
   product has: `admin support list|show|reply|close`. `list --json` and
   `show --json [--after N]` exist for tooling; `list` carries no customer
   text and no address. `reply --stdin` takes the message from standard
   input, so text from Slack is never quoted by a shell. There is no web
   inbox: nothing else here has one, and the operator answers from Slack.

5. **A staff reply emails the user that there is an answer**, without the
   answer. It is read, and replied to, on the Support page, so the
   conversation stays in one place and no reply-by-email path has to be
   built or secured. A reply whose email fails is saved and the CLI exits 1
   saying so. Reply and close write `audit_events` (`support_replied`,
   `support_closed`) in the same transaction, with the operator's name; the
   customer sees only "Unify Compute".

6. **The operator address is emailed when a conversation starts waiting**
   (new, or the customer answers an answered one), with the conversation id
   and no customer text. It is the fallback for the Slack watch, which runs
   on one Mac and is silent while that Mac sleeps.

7. **No AI first responder.** Every message is a person's to answer. Unify
   API's and Unify Data's hand-off rules ("the AI gave up", "no answer for 3
   minutes") have nothing to decide here: a conversation is a hand-off from
   its first message.

## Consequences

- The Slack bridge for Compute is a watch in `unifyai-workspace` that runs
  `admin support list --json` on the production host over SSM and replies
  with `admin support reply --stdin`. It is not part of this repository and
  not built by this ADR's change.
- Messages are stored as written and shown as text (`textContent`,
  `white-space: pre-wrap`); nothing renders them as markup.
- A customer who pastes a secret into a message has put it in the database.
  The form says not to; nothing scans for it.
- The reply email is English, like every other Compute email.
- There is no attachment, no read receipt and no notification inside the
  dashboard other than the Support page itself, which polls every 30 s
  while it is open.
- Staff accounts are not marked: Compute has no staff role. Tooling that
  wants to tell an internal account from a customer does it by address.

## Not decided

- Retention. Conversations are kept indefinitely; closing an account does
  not delete them (the same rule as money records, without that reason).
  Legal review of the privacy policy should say how long.
- An AI first responder, if volume ever asks for one.
