-- Refunds and disputes on Stripe top-ups (docs/spec/accounts.md, "Top-up",
-- "topup_disputes" and "payment_emails"; task B2). internal/payments writes
-- all of it from the Stripe webhook, in the transaction that stores the
-- event.
--
-- No foreign keys, for 0006's reason. Re-runnable (IF NOT EXISTS, and a
-- constraint dropped only when it is still there) for store's
-- TestMigrateConcurrent.

-- 0005 allowed a negative balance only on a suspended account, because only
-- a chargeback was meant to drive it negative. A refund issued at Stripe is
-- money already returned: when it is larger than what is left (refunded from
-- the Stripe dashboard, or a settle that landed between the CLI's check and
-- the refund), the debit is recorded in full and the balance goes negative
-- without suspending the account. A negative balance still refuses every
-- hold, so it cannot run anything until it is paid back. 0005's constraint
-- was unnamed; it is found by its definition.
DO $$
DECLARE c text;
BEGIN
    FOR c IN SELECT conname FROM pg_constraint
        WHERE conrelid = 'accounts'::regclass AND contype = 'c'
          AND pg_get_constraintdef(oid) LIKE '%available >= 0%'
    LOOP
        EXECUTE format('ALTER TABLE accounts DROP CONSTRAINT %I', c);
    END LOOP;
END
$$;

-- One row per Stripe dispute on a top-up. The dispute's own id keys it, so
-- charge.dispute.created and charge.dispute.closed find the same row
-- whatever order they arrive in, and a dispute is debited and reversed at
-- most once each.
CREATE TABLE IF NOT EXISTS topup_disputes (
    id                text        PRIMARY KEY CHECK (id ~ '^(dp|du)_'),
    topup_id          text        NOT NULL,
    account_id        bigint      NOT NULL,
    amount_cents      integer     NOT NULL CHECK (amount_cents > 0),
    -- open → won | lost. Stripe's warning_closed (an inquiry that never
    -- became a chargeback) is recorded as won: the money came back.
    status            text        NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'won', 'lost')),
    reason            text        NOT NULL DEFAULT '',
    -- Whether this dispute moved the account from active to suspended, so a
    -- won dispute lifts only a suspension it caused.
    suspended_account boolean     NOT NULL DEFAULT false,
    opened_event_id   text        NOT NULL,
    closed_event_id   text,
    created_at        timestamptz NOT NULL,
    closed_at         timestamptz,
    CHECK ((status = 'open') = (closed_at IS NULL))
);

CREATE INDEX IF NOT EXISTS topup_disputes_account_open_idx ON topup_disputes (account_id) WHERE status = 'open';

-- Emails a Stripe event causes, queued in the event's transaction and sent
-- after it commits: refund issued, account suspended, and the operator's
-- notices. The key is the event id, the message kind and the recipient, so
-- a message is sent at most once per event. claimed_at marks a send in
-- flight; a claim older than 15 minutes is taken to have died with its
-- process. W2's general email idempotency table can absorb this one.
CREATE TABLE IF NOT EXISTS payment_emails (
    event_id   text        NOT NULL,
    kind       text        NOT NULL CHECK (kind ~ '^[a-z_]+$'),
    recipient  text        NOT NULL CHECK (recipient <> ''),
    account_id bigint      NOT NULL,
    detail     jsonb       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(detail) = 'object'),
    created_at timestamptz NOT NULL,
    claimed_at timestamptz,
    sent_at    timestamptz,
    PRIMARY KEY (event_id, kind, recipient)
);

CREATE INDEX IF NOT EXISTS payment_emails_unsent_idx ON payment_emails (created_at) WHERE sent_at IS NULL;
