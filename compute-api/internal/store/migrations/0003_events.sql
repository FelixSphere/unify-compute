-- Job events, webhooks and their delivery queue (docs/spec/events.md).
--
-- Written to be re-runnable (IF NOT EXISTS, OR REPLACE) and with no foreign
-- key to jobs. TestMigrateConcurrent in store_test.go drops exactly
-- idempotency_keys, jobs and schema_migrations and migrates again; a later
-- migration that references jobs or cannot run twice breaks it. AppendEvent
-- checks the job in the same statement instead.

CREATE TABLE IF NOT EXISTS job_events (
    id         text        PRIMARY KEY CHECK (id ~ '^evt_[0-9A-HJKMNP-TV-Z]{26}$'),
    -- The order key. ULIDs only sort to the millisecond, and a status change
    -- and its billing event can land in the same one.
    seq        bigint      GENERATED ALWAYS AS IDENTITY UNIQUE,
    job_id     text        NOT NULL,
    type       text        NOT NULL CHECK (type ~ '^(job|billing)\.[a-z_]+$'),
    data       jsonb       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(data) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS job_events_job_seq_idx ON job_events (job_id, seq);

-- Append-only: a delivered webhook and a later read of the log must agree.
-- DELETE stays possible so retention can purge.
CREATE OR REPLACE FUNCTION job_events_refuse_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'job_events is append-only';
END $$;

CREATE OR REPLACE TRIGGER job_events_append_only BEFORE UPDATE ON job_events
    FOR EACH ROW EXECUTE FUNCTION job_events_refuse_update();

CREATE TABLE IF NOT EXISTS webhooks (
    id          text        PRIMARY KEY CHECK (id ~ '^wh_[0-9A-HJKMNP-TV-Z]{26}$'),
    owner       text        NOT NULL CHECK (owner <> ''),
    -- The key that registered it; deliveries are limited to that key's jobs.
    -- Null is an owner-wide webhook, which only an operator can create.
    token_id    bigint,
    url         text        NOT NULL CHECK (url ~ '^https?://'),
    secret      text        NOT NULL CHECK (char_length(secret) >= 32),
    event_types text[]      NOT NULL CHECK (cardinality(event_types) > 0),
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS webhooks_owner_idx ON webhooks (owner);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id              bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id        text        NOT NULL REFERENCES job_events (id) ON DELETE CASCADE,
    webhook_id      text        NOT NULL REFERENCES webhooks (id) ON DELETE CASCADE,
    status          text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivered', 'failed')),
    attempts        integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL,
    last_status     integer,
    last_error      text,
    delivered_at    timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (event_id, webhook_id),
    CHECK ((status = 'delivered') = (delivered_at IS NOT NULL))
);

-- The worker's claim query; settled deliveries are the bulk and never polled.
CREATE INDEX IF NOT EXISTS webhook_deliveries_due_idx ON webhook_deliveries (next_attempt_at)
    WHERE status = 'pending';
