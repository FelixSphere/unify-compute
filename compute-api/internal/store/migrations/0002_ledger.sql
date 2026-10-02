-- Usage ledger, console holds and GPU settlement state (docs/spec/metering.md,
-- task C5). The engine that writes these lives in internal/ledger.
--
-- usage_events is the source of truth for what a customer owes. It is
-- append-only: no code path updates or deletes a row, and the trigger below
-- refuses it, so a bug cannot rewrite history that has already been settled
-- against the console. holds and metering_attempts are mutable state about
-- where settlement has got to; they can always be rebuilt by reading the
-- ledger and asking the console.
--
-- Every statement is re-runnable (IF NOT EXISTS / OR REPLACE): store's
-- TestMigrateConcurrent drops only 0001's tables and schema_migrations and
-- migrates again, so a later migration must tolerate its objects existing.
--
-- No foreign key to jobs: the hold is placed before the job row exists
-- (metering.md H2: a refused hold means the job is never persisted).

CREATE TABLE IF NOT EXISTS usage_events (
    -- Sent to the console as settle's event_id, which it dedupes on across
    -- all holds, so it must be globally unique and never reused.
    id                  text        PRIMARY KEY CHECK (id ~ '^ue_[0-9A-HJKMNP-TV-Z]{26}$'),
    -- Write order. ULIDs order only to the millisecond, and a job's final
    -- event can share a timestamp with the interval before it.
    seq                 bigint      GENERATED ALWAYS AS IDENTITY UNIQUE,
    job_id              text        NOT NULL,
    -- gpu_seconds: quantity in seconds. storage_byte_seconds: bytes × seconds
    -- stored. egress_bytes: bytes sent. Only gpu_seconds is written today.
    meter               text        NOT NULL CHECK (meter IN ('gpu_seconds', 'storage_byte_seconds', 'egress_bytes')),
    attempt             integer     CHECK (attempt >= 1),
    from_at             timestamptz NOT NULL,
    to_at               timestamptz NOT NULL CHECK (to_at >= from_at),
    quantity            bigint      NOT NULL CHECK (quantity >= 0),
    -- Exact decimal string as priced: USD per hour (GPU, snapshotted at
    -- submit), per GB-month (storage) or per GB (egress).
    rate                text        NOT NULL CHECK (rate ~ '^[0-9]+(\.[0-9]+)?$'),
    -- GPU: cumulative across attempts, as sent in settle. The quota is
    -- computed once on the cumulative quantity (one rounding), and amount is
    -- the difference from the previous event, so the amounts of a job sum to
    -- exactly cumulative_amount. Storage and egress are rounded once per
    -- daily charge (R5), so their per-event amount stays null.
    cumulative_quantity bigint      CHECK (cumulative_quantity >= 0),
    cumulative_amount   bigint      CHECK (cumulative_amount >= 0),
    amount              bigint,
    final               boolean     NOT NULL DEFAULT false,
    created_at          timestamptz NOT NULL DEFAULT now(),
    CHECK ((meter = 'gpu_seconds') = (attempt IS NOT NULL)),
    CHECK ((meter = 'gpu_seconds') = (cumulative_amount IS NOT NULL AND cumulative_quantity IS NOT NULL AND amount IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS usage_events_job_idx ON usage_events (job_id, meter, seq);

CREATE OR REPLACE FUNCTION usage_events_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'usage_events is append-only';
END;
$$;

CREATE OR REPLACE TRIGGER usage_events_no_update BEFORE UPDATE OR DELETE ON usage_events
    FOR EACH ROW EXECUTE FUNCTION usage_events_append_only();
CREATE OR REPLACE TRIGGER usage_events_no_truncate BEFORE TRUNCATE ON usage_events
    FOR EACH STATEMENT EXECUTE FUNCTION usage_events_append_only();

-- One console hold per job (the GPU meter's). Amounts are quota units.
CREATE TABLE IF NOT EXISTS holds (
    hold_id              text        PRIMARY KEY CHECK (hold_id ~ '^hold_[0-9A-HJKMNP-TV-Z]{26}$'),
    job_id               text        NOT NULL UNIQUE,
    user_id              bigint      NOT NULL,
    token_id             bigint      NOT NULL,
    sku                  text        NOT NULL CHECK (sku <> ''),
    rate                 text        NOT NULL CHECK (rate ~ '^[0-9]+(\.[0-9]+)?$'),
    max_runtime_seconds  bigint      NOT NULL CHECK (max_runtime_seconds > 0),
    -- The hold request as first sent; a retried create must repeat it
    -- exactly or the console answers idempotency_conflict.
    original_quota       bigint      NOT NULL CHECK (original_quota > 0),
    expires_at           timestamptz NOT NULL,
    -- quota(max_runtime_seconds): the hold is never extended past it.
    ceiling_quota        bigint      NOT NULL,
    current_quota        bigint      NOT NULL,
    -- pending: hold sent, outcome unknown. refused: 402 at submit.
    -- open: held. closing: terminal, final settle or release not yet
    -- confirmed. closed: done. expired: the console swept it (R6).
    state                text        NOT NULL DEFAULT 'pending' CHECK (state IN (
                             'pending', 'refused', 'open', 'closing', 'closed', 'expired')),
    settled_quota        bigint      NOT NULL DEFAULT 0,
    settled_event_id     text,
    -- Persisted before the extend call so a retry after a lost response
    -- reuses the id and the console reserves once.
    pending_extend_id    text        CHECK (char_length(pending_extend_id) <= 64),
    pending_extend_quota bigint,
    extend_refused_at    timestamptz,
    -- Why the last settle or extend did not go through: refused (402) or
    -- unavailable (transport error, 5xx, 429, bridge disabled).
    last_failure         text        CHECK (last_failure IN ('refused', 'unavailable')),
    cancel_code          text        CHECK (cancel_code IN ('balance_exhausted', 'billing_unavailable')),
    -- Accrued but never settled: the hold expired under it (R6) or the
    -- console refused it. Non-zero only in those cases.
    unbilled_quota       bigint      NOT NULL DEFAULT 0,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    CHECK ((pending_extend_id IS NULL) = (pending_extend_quota IS NULL))
);

CREATE INDEX IF NOT EXISTS holds_active_idx ON holds (state) WHERE state IN ('open', 'closing');

-- One row per SkyPilot cluster lifetime (metering.md "attempt").
CREATE TABLE IF NOT EXISTS metering_attempts (
    job_id      text        NOT NULL,
    attempt     integer     NOT NULL CHECK (attempt >= 1),
    started_at  timestamptz NOT NULL,
    -- Last time the cluster was observed UP with the job still the
    -- customer's. Metering never runs past it (I3).
    up_through  timestamptz NOT NULL,
    -- Metering stops here even if the cluster stays up: the moment
    -- cumulative seconds reach max_runtime_seconds, or compute-api's own
    -- cancel.
    cap_at      timestamptz NOT NULL,
    cap_reason  text        NOT NULL,
    -- When the backend accepted our request to stop the job at cap_at.
    stop_requested_at timestamptz,
    ended_at    timestamptz,
    end_reason  text,
    PRIMARY KEY (job_id, attempt),
    CHECK ((ended_at IS NULL) = (end_reason IS NULL))
);
