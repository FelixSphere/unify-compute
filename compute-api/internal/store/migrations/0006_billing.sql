-- The local biller (docs/spec/accounts.md, "balance_entries" and "Local
-- holds"; ADR-0011; task A6). With ACCOUNTS_BACKEND=local the ledger places,
-- settles, extends and releases its holds here instead of on the console,
-- with the same idempotency and refusals as the console bridge.
--
-- The balance itself is the accounts row's available/held columns, from
-- 0005_accounts.sql (task A1). Nothing here references accounts at
-- migration time (no foreign key, no ALTER), for two reasons. A ledger row
-- must outlive what it names, as an audit row does: closing keeps them, and
-- PurgeUnverified deletes pending accounts in one statement that a foreign
-- key would make fail outright. And 0006 then applies on a database whatever
-- the order 0005 and 0006 reach it in. Nothing writes these tables unless
-- ACCOUNTS_BACKEND=local or an operator runs `compute-api admin balance`.
--
-- Every statement is re-runnable (IF NOT EXISTS / OR REPLACE), for store's
-- TestMigrateConcurrent.

-- One row per movement of an account's balance. available_after/held_after
-- snapshot the account after the movement, so the balance can be audited
-- from the entries alone: for every account the deltas sum to the columns.
CREATE TABLE IF NOT EXISTS balance_entries (
    id              text        PRIMARY KEY CHECK (id ~ '^be_[0-9A-HJKMNP-TV-Z]{26}$'),
    seq             bigint      GENERATED ALWAYS AS IDENTITY UNIQUE,
    account_id      bigint      NOT NULL,
    kind            text        NOT NULL CHECK (kind IN ('topup', 'hold', 'extend', 'settle', 'release',
                                    'refund', 'dispute', 'dispute_reversal', 'admin_credit', 'admin_debit', 'writeoff')),
    available_delta bigint      NOT NULL,
    held_delta      bigint      NOT NULL,
    available_after bigint      NOT NULL,
    held_after      bigint      NOT NULL CHECK (held_after >= 0),
    ref_type        text        NOT NULL,
    ref_id          text        NOT NULL,
    -- Derived from the triggering id (settle:ue_…, extend:ext_…, admin:ae_…),
    -- so a retried effect cannot be written twice even if the verb's own
    -- idempotency check were bypassed.
    idempotency_key text        NOT NULL UNIQUE,
    detail          jsonb       NOT NULL DEFAULT '{}',
    created_at      timestamptz NOT NULL DEFAULT now(),
    CHECK (available_delta <> 0 OR held_delta <> 0)
);

CREATE INDEX IF NOT EXISTS balance_entries_account_idx ON balance_entries (account_id, seq);

CREATE OR REPLACE FUNCTION balance_entries_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'balance_entries is append-only';
END;
$$;

CREATE OR REPLACE TRIGGER balance_entries_no_update BEFORE UPDATE OR DELETE ON balance_entries
    FOR EACH ROW EXECUTE FUNCTION balance_entries_append_only();
CREATE OR REPLACE TRIGGER balance_entries_no_truncate BEFORE TRUNCATE ON balance_entries
    FOR EACH STATEMENT EXECUTE FUNCTION balance_entries_append_only();

-- The biller's side of a hold: what the console kept for the bridge, now
-- ours. The ledger's own view of the same hold stays in holds (0002).
CREATE TABLE IF NOT EXISTS billing_holds (
    hold_id        text        PRIMARY KEY CHECK (hold_id <> ''),
    account_id     bigint      NOT NULL,
    key_id         bigint      NOT NULL,
    -- Null for an R5 storage or egress charge, which has no job.
    job_id         text,
    sku            text        NOT NULL,
    quota          bigint      NOT NULL CHECK (quota > 0),
    settled_quota  bigint      NOT NULL DEFAULT 0 CHECK (settled_quota >= 0 AND settled_quota <= quota),
    -- What went back to available when the hold closed. The console's
    -- release answer reports it again on every repeat.
    released_quota bigint      NOT NULL DEFAULT 0 CHECK (released_quota >= 0),
    -- settled: closed by a final settle. released: closed by release.
    -- expired: swept past expires_at. Only open moves balance.
    status         text        NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'settled', 'released', 'expired')),
    -- Null never expires (the bridge's expires_at = 0).
    expires_at     timestamptz,
    -- The create request and its answer, for idempotency: a replay with the
    -- same request gets the same answer, a different one is a conflict.
    request        jsonb       NOT NULL,
    response       jsonb       NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    closed_at      timestamptz,
    CHECK ((status = 'open') = (closed_at IS NULL)),
    CHECK (status = 'open' OR settled_quota + released_quota = quota)
);

CREATE INDEX IF NOT EXISTS billing_holds_open_idx ON billing_holds (account_id, expires_at) WHERE status = 'open';

-- One row per accepted settle, including one that moved nothing (a final
-- settle with no new usage still closes the hold), so a replayed event_id
-- answers what it answered the first time.
CREATE TABLE IF NOT EXISTS billing_settlements (
    event_id    text        PRIMARY KEY CHECK (event_id <> ''),
    seq         bigint      GENERATED ALWAYS AS IDENTITY UNIQUE,
    hold_id     text        NOT NULL REFERENCES billing_holds (hold_id),
    quota_delta bigint      NOT NULL CHECK (quota_delta >= 0),
    gpu_seconds bigint      NOT NULL,
    final       boolean     NOT NULL,
    response    jsonb       NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS billing_settlements_hold_idx ON billing_settlements (hold_id, seq);

CREATE TABLE IF NOT EXISTS billing_extensions (
    extend_id        text        PRIMARY KEY CHECK (extend_id <> '' AND length(extend_id) <= 64),
    hold_id          text        NOT NULL REFERENCES billing_holds (hold_id),
    additional_quota bigint      NOT NULL CHECK (additional_quota > 0),
    created_at       timestamptz NOT NULL DEFAULT now()
);
