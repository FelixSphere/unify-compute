-- Bonus credit on top-ups, and customers' refund requests (ADR-0013;
-- docs/spec/accounts.md "Top-up", "Refund request").
--
-- No foreign keys, for 0006's reason: a money record outlives what it names.
-- Re-runnable (IF NOT EXISTS / DROP … IF EXISTS) for store's
-- TestMigrateConcurrent.

-- topup_bonus: the bonus a top-up earns (positive), and the share a refund or
-- dispute takes back or a won dispute restores.
ALTER TABLE balance_entries DROP CONSTRAINT IF EXISTS balance_entries_kind_check;
ALTER TABLE balance_entries ADD CONSTRAINT balance_entries_kind_check CHECK (kind IN ('topup', 'topup_bonus', 'hold', 'extend',
    'settle', 'release', 'refund', 'dispute', 'dispute_reversal', 'admin_credit', 'admin_debit', 'writeoff'));

-- Fixed when the top-up is created, so a change of tiers never changes a
-- payment already in flight. bonus_reversed_cents is what refunds and open
-- disputes have taken back of it.
ALTER TABLE topups ADD COLUMN IF NOT EXISTS bonus_cents integer NOT NULL DEFAULT 0 CHECK (bonus_cents >= 0);
ALTER TABLE topups ADD COLUMN IF NOT EXISTS bonus_reversed_cents integer NOT NULL DEFAULT 0;
ALTER TABLE topups DROP CONSTRAINT IF EXISTS topups_bonus_reversed_check;
ALTER TABLE topups ADD CONSTRAINT topups_bonus_reversed_check CHECK (bonus_reversed_cents >= 0 AND bonus_reversed_cents <= bonus_cents);

-- The bonus a dispute took back, so a won dispute restores exactly that.
ALTER TABLE topup_disputes ADD COLUMN IF NOT EXISTS bonus_cents integer NOT NULL DEFAULT 0 CHECK (bonus_cents >= 0);

CREATE TABLE IF NOT EXISTS refund_requests (
    id           text        PRIMARY KEY CHECK (id ~ '^rr_[0-9A-HJKMNP-TV-Z]{26}$'),
    account_id   bigint      NOT NULL,
    user_id      bigint      NOT NULL,
    topup_id     text        NOT NULL,
    amount_cents integer     NOT NULL CHECK (amount_cents > 0),
    reason       text        NOT NULL DEFAULT '' CHECK (length(reason) <= 500),
    status       text        NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'refunded', 'declined', 'cancelled')),
    -- The operator's message on a decline, which the customer is emailed.
    resolution   text        NOT NULL DEFAULT '',
    -- The Stripe event whose refund closed it.
    refund_event text,
    created_at   timestamptz NOT NULL,
    resolved_at  timestamptz,
    CHECK ((status = 'open') = (resolved_at IS NULL)),
    CHECK (status <> 'refunded' OR refund_event IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS refund_requests_account_idx ON refund_requests (account_id, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS refund_requests_one_open_idx ON refund_requests (topup_id) WHERE status = 'open';
