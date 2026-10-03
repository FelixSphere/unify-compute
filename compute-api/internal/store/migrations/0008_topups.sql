-- Stripe Checkout top-ups (ADR-0011 §4, docs/spec/accounts.md "topups",
-- "stripe_events" and "Top-up"; task B1). internal/payments writes both
-- tables; only the Stripe webhook moves a top-up to credited and writes its
-- balance entry (0006's balance_entries, kind topup).
--
-- No foreign key to accounts, for 0006's reason: a money record outlives what
-- it names, and closing keeps it. Re-runnable (IF NOT EXISTS / OR REPLACE)
-- for store's TestMigrateConcurrent.

CREATE TABLE IF NOT EXISTS topups (
    -- Checkout's client_reference_id and metadata.topup_id.
    id                         text        PRIMARY KEY CHECK (id ~ '^tu_[0-9A-HJKMNP-TV-Z]{26}$'),
    account_id                 bigint      NOT NULL,
    user_id                    bigint      NOT NULL,
    amount_cents               integer     NOT NULL CHECK (amount_cents > 0),
    currency                   text        NOT NULL DEFAULT 'usd' CHECK (currency = 'usd'),
    -- open → credited | expired | failed. refunded, partially_refunded,
    -- disputed and lost are B2's; the check admits them so B2 needs no
    -- migration to change a constraint.
    status                     text        NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'credited', 'expired', 'failed',
                                               'refunded', 'partially_refunded', 'disputed', 'lost')),
    -- Null only between the insert and Stripe's answer; a top-up whose
    -- session could not be created is deleted, since nothing was charged.
    stripe_checkout_session_id text        UNIQUE,
    stripe_customer_id         text,
    stripe_payment_intent_id   text,
    stripe_charge_id           text,
    receipt_url                text,
    refunded_cents             integer     NOT NULL DEFAULT 0 CHECK (refunded_cents >= 0),
    disputed_cents             integer     NOT NULL DEFAULT 0 CHECK (disputed_cents >= 0),
    created_at                 timestamptz NOT NULL,
    paid_at                    timestamptz,
    -- Checkout's own expires_at.
    expires_at                 timestamptz,
    CHECK (status IN ('open', 'expired', 'failed') OR paid_at IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS topups_account_idx ON topups (account_id, created_at);
CREATE INDEX IF NOT EXISTS topups_payment_intent_idx ON topups (stripe_payment_intent_id) WHERE stripe_payment_intent_id IS NOT NULL;

-- At most one open top-up per account. Creating another first expires the
-- open one's Checkout Session at Stripe, so an old tab cannot pay it too.
CREATE UNIQUE INDEX IF NOT EXISTS topups_one_open_idx ON topups (account_id) WHERE status = 'open';

-- One row per Stripe event that concerned compute, inserted in the same
-- transaction as its effect: a redelivered event conflicts here and changes
-- nothing. Events of other products sharing the Stripe account are answered
-- 200 and not stored.
CREATE TABLE IF NOT EXISTS stripe_events (
    event_id       text        PRIMARY KEY CHECK (event_id ~ '^evt_'),
    type           text        NOT NULL,
    livemode       boolean     NOT NULL,
    topup_id       text,
    -- What the webhook did: credited, expired, failed, already_credited,
    -- awaiting_payment, recorded (refunds and disputes until B2), rejected
    -- (did not match its top-up; logged as an error).
    outcome        text        NOT NULL,
    payload_sha256 bytea       NOT NULL CHECK (octet_length(payload_sha256) = 32),
    received_at    timestamptz NOT NULL,
    processed_at   timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS stripe_events_topup_idx ON stripe_events (topup_id) WHERE topup_id IS NOT NULL;
