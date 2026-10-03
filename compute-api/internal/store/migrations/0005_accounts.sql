-- Compute's own accounts (ADR-0011, docs/spec/accounts.md, task A1). The
-- code that reads and writes these lives in internal/accounts. Nothing here
-- is reached by a route yet: the dashboard routes are A4, keys A5, and the
-- balance entries and local holds A6 (0006).
--
-- Re-runnable (IF NOT EXISTS / OR REPLACE) for the reason 0002 gives:
-- TestMigrateConcurrent drops 0001's tables and schema_migrations and
-- migrates again, so every object here may already exist.

-- The billing entity. Owner string elsewhere (jobs.owner, quotas) is
-- 'account:<id>'; holds.user_id takes this id after the cutover.
CREATE TABLE IF NOT EXISTS accounts (
    id                 bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    status             text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'closed')),
    compute_enabled    boolean     NOT NULL DEFAULT false,
    -- Quota units, 500000 per USD 1. Only a chargeback drives available
    -- negative, and a chargeback that does also suspends the account.
    available          bigint      NOT NULL DEFAULT 0,
    held               bigint      NOT NULL DEFAULT 0 CHECK (held >= 0),
    topup_min_cents    integer     NOT NULL DEFAULT 1000 CHECK (topup_min_cents > 0),
    topup_max_cents    integer     NOT NULL DEFAULT 10000 CHECK (topup_max_cents >= topup_min_cents),
    balance_max_cents  integer     NOT NULL DEFAULT 20000 CHECK (balance_max_cents > 0),
    topups_per_day     integer     NOT NULL DEFAULT 3 CHECK (topups_per_day >= 0),
    stripe_customer_id text        UNIQUE,
    suspended_reason   text        CHECK (suspended_reason IN ('dispute', 'abuse', 'operator')),
    created_at         timestamptz NOT NULL DEFAULT now(),
    closed_at          timestamptz,
    CHECK (available >= 0 OR status = 'suspended'),
    CHECK ((status = 'suspended') = (suspended_reason IS NOT NULL)),
    CHECK ((status = 'closed') = (closed_at IS NOT NULL))
);

-- A login. One per account today (role owner); kept apart from accounts so
-- organisations can be added later without moving rows (ADR-0011 §1).
CREATE TABLE IF NOT EXISTS users (
    id                  bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id          bigint      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    role                text        NOT NULL DEFAULT 'owner' CHECK (role IN ('owner')),
    -- NFC, trimmed, lower-cased by internal/accounts before it gets here.
    -- 'closed+<id>@invalid' after closing, so the address can register again.
    email               text        NOT NULL UNIQUE CHECK (email <> '' AND char_length(email) <= 254),
    password_hash       text        NOT NULL CHECK (password_hash LIKE '$argon2id$%'),
    status              text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'active', 'disabled', 'closed')),
    email_verified_at   timestamptz,
    last_login_at       timestamptz,
    password_changed_at timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    CHECK (status <> 'active' OR email_verified_at IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS users_account_idx ON users (account_id);
CREATE INDEX IF NOT EXISTS users_pending_idx ON users (created_at) WHERE status = 'pending';

-- Dashboard sessions. The cookie value is never stored, only its SHA-256,
-- so a database read does not yield a usable cookie.
CREATE TABLE IF NOT EXISTS sessions (
    id_hash      bytea       PRIMARY KEY CHECK (octet_length(id_hash) = 32),
    user_id      bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    csrf_token   text        NOT NULL,
    created_at   timestamptz NOT NULL,
    -- Idle timeout runs from here; written at most once a minute.
    last_seen_at timestamptz NOT NULL,
    -- Last time the password was entered in this session (login or reauth).
    reauth_at    timestamptz NOT NULL,
    -- Absolute lifetime; never extended.
    expires_at   timestamptz NOT NULL,
    ip           text        NOT NULL DEFAULT '',
    user_agent   text        NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS sessions_user_idx ON sessions (user_id);
CREATE INDEX IF NOT EXISTS sessions_expires_idx ON sessions (expires_at);

-- Emailed 6-digit verification codes. The id is a text ULID generated
-- before the insert, because the code is hashed with it as the salt.
CREATE TABLE IF NOT EXISTS email_codes (
    id            text        PRIMARY KEY CHECK (id ~ '^ec_[0-9A-HJKMNP-TV-Z]{26}$'),
    user_id       bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    purpose       text        NOT NULL CHECK (purpose IN ('verify')),
    code_hash     bytea       NOT NULL CHECK (octet_length(code_hash) = 32),
    attempts      integer     NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 5),
    expires_at    timestamptz NOT NULL,
    used_at       timestamptz,
    superseded_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- At most one code per user and purpose that is neither used nor
-- superseded. Expiry and exhaustion cannot be in an index predicate; issuing
-- a new code supersedes those too.
CREATE UNIQUE INDEX IF NOT EXISTS email_codes_live_idx ON email_codes (user_id, purpose)
    WHERE used_at IS NULL AND superseded_at IS NULL;

-- Password-reset links. digest is SHA-256 of the 32-byte random token; the
-- token itself travels only in the link's fragment.
CREATE TABLE IF NOT EXISTS auth_tokens (
    digest        bytea       PRIMARY KEY CHECK (octet_length(digest) = 32),
    user_id       bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    purpose       text        NOT NULL CHECK (purpose IN ('reset')),
    expires_at    timestamptz NOT NULL,
    used_at       timestamptz,
    superseded_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS auth_tokens_live_idx ON auth_tokens (user_id, purpose)
    WHERE used_at IS NULL AND superseded_at IS NULL;

-- Throttle counters, in Postgres so they survive restarts and are shared by
-- every replica. key is 'bucket:subject'. The window opens at the first
-- attempt and later attempts do not extend it.
--
-- expires_at is stored per row (it is not in the spec's column list) so the
-- purge removes each row against its own window. Unify Data purged every row
-- older than one fixed window, so a route with a short window wiped the
-- counters of a route with a long one, and its hourly limit reset every few
-- minutes.
CREATE TABLE IF NOT EXISTS auth_attempts (
    key          text        PRIMARY KEY CHECK (key ~ '^[a-z0-9_]+:.+$'),
    count        integer     NOT NULL CHECK (count >= 1),
    window_start timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL CHECK (expires_at > window_start)
);

CREATE INDEX IF NOT EXISTS auth_attempts_expires_idx ON auth_attempts (expires_at);

-- Security and money audit trail. Append-only, like usage_events: no foreign
-- keys, because rows outlive the accounts and users they name (an account
-- purged after 7 days unverified keeps its signup row).
CREATE TABLE IF NOT EXISTS audit_events (
    id         text        PRIMARY KEY CHECK (id ~ '^ae_[0-9A-HJKMNP-TV-Z]{26}$'),
    seq        bigint      GENERATED ALWAYS AS IDENTITY UNIQUE,
    at         timestamptz NOT NULL DEFAULT now(),
    actor_type text        NOT NULL CHECK (actor_type IN ('user', 'admin', 'system', 'stripe')),
    actor_id   text        NOT NULL DEFAULT '',
    account_id bigint,
    action     text        NOT NULL CHECK (action ~ '^[a-z_]+$'),
    ip         text        NOT NULL DEFAULT '',
    user_agent text        NOT NULL DEFAULT '',
    reason     text,
    detail     jsonb       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(detail) = 'object'),
    CHECK (actor_type <> 'admin' OR btrim(coalesce(reason, '')) <> '')
);

CREATE INDEX IF NOT EXISTS audit_events_account_idx ON audit_events (account_id, seq);

CREATE OR REPLACE FUNCTION audit_events_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_events is append-only';
END;
$$;

CREATE OR REPLACE TRIGGER audit_events_no_update BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_append_only();
CREATE OR REPLACE TRIGGER audit_events_no_truncate BEFORE TRUNCATE ON audit_events
    FOR EACH STATEMENT EXECUTE FUNCTION audit_events_append_only();
