-- Compute's own API keys (ADR-0011 §2, docs/spec/accounts.md "api_keys",
-- task A5). With ACCOUNTS_BACKEND=local the job API resolves
-- `Authorization: Bearer uck_…` here; with the console backend nothing reads
-- this table.
--
-- Re-runnable (IF NOT EXISTS) for the reason 0002 gives: store's
-- TestMigrateConcurrent drops 0001's tables and schema_migrations and
-- migrates again, so every object here may already exist.

CREATE TABLE IF NOT EXISTS api_keys (
    -- Fits jobs.token_id, holds.token_id and billing_holds.key_id.
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id   bigint      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    name         text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
    -- uck_ and the first 8 characters of the secret: all that is kept in
    -- clear, so a database read never yields a usable key.
    display      text        NOT NULL CHECK (display ~ '^uck_[0-9A-Za-z]{8}$'),
    -- SHA-256 of the whole key. A fast hash is enough for a 238-bit random
    -- secret and keeps the per-request lookup cheap (ADR-0011 §2).
    secret_hash  bytea       NOT NULL UNIQUE CHECK (octet_length(secret_hash) = 32),
    scopes       text[]      NOT NULL CHECK (cardinality(scopes) > 0 AND scopes <@ ARRAY['jobs', 'read']::text[]),
    created_by   bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL,
    -- Null never expires.
    expires_at   timestamptz,
    -- Written at most once a minute per key, not on every request.
    last_used_at timestamptz,
    -- Terminal; a revoked key is refused on the next request (no cache).
    revoked_at   timestamptz
);

CREATE INDEX IF NOT EXISTS api_keys_account_idx ON api_keys (account_id, id);
