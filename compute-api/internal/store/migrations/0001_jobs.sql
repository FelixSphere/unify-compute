-- Jobs and idempotency keys (docs/spec/job-api.md).
--
-- owner is the billing identity the console bridge resolves a key to, as an
-- opaque string chosen by the caller. Every customer-facing read is scoped to
-- it; token_id records which key submitted the job and is not a scope.

CREATE TABLE jobs (
    id          text        PRIMARY KEY CHECK (id ~ '^job_[0-9A-HJKMNP-TV-Z]{26}$'),
    owner       text        NOT NULL CHECK (owner <> ''),
    token_id    bigint,
    sku         text        NOT NULL CHECK (sku <> ''),
    status      text        NOT NULL DEFAULT 'queued' CHECK (status IN (
                    'queued', 'provisioning', 'running',
                    'succeeded', 'failed', 'interrupted', 'cancelled')),
    spec        jsonb       NOT NULL CHECK (jsonb_typeof(spec) = 'object'),
    metadata    jsonb       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(metadata) = 'object'),
    error       jsonb,
    -- Where the job actually ran (provider, region, instance). Filled by the
    -- backend once SkyPilot has placed it; null until then.
    placement   jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    started_at  timestamptz,
    finished_at timestamptz,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CHECK ((status IN ('succeeded', 'failed', 'interrupted', 'cancelled')) = (finished_at IS NOT NULL))
);

-- Serves the customer list: owner scope, newest first, keyset cursor.
CREATE INDEX jobs_owner_created_idx ON jobs (owner, created_at DESC, id DESC);

-- The backend's poll loop reads non-terminal jobs; terminal ones are the bulk
-- of the table and never need this index.
CREATE INDEX jobs_active_idx ON jobs (status)
    WHERE status IN ('queued', 'provisioning', 'running');

CREATE TABLE idempotency_keys (
    owner        text        NOT NULL,
    key          text        NOT NULL CHECK (char_length(key) BETWEEN 1 AND 255),
    request_hash bytea       NOT NULL,
    job_id       text        NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (owner, key)
);

CREATE INDEX idempotency_keys_created_idx ON idempotency_keys (created_at);
