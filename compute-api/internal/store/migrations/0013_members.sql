-- Members and invitations (ADR-0011 D9, docs/spec/accounts.md "Members and
-- roles", task A5b): the account is the tenant, users are logins with a role
-- in exactly one account, and new users arrive by invitation.
--
-- 0011 and 0012 are taken by work in flight; this file does not depend on
-- either.
--
-- Re-runnable, like every migration here: TestMigrateConcurrent drops
-- schema_migrations and migrates again over tables that already exist.

-- How the tenant appears in the dashboard and in member emails. Signup sets
-- it to the first user's email; owners and admins rename it (1-64
-- characters, checked by internal/accounts). The bound here only admits the
-- email default, which can be longer.
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS name text NOT NULL DEFAULT '' CHECK (char_length(name) <= 254);

-- Members plus live invitations. 20 by default; the operator raises it on
-- request, like the top-up limits.
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS member_limit integer NOT NULL DEFAULT 20 CHECK (member_limit >= 1);

-- Accounts made before this migration had one user each, its owner.
UPDATE accounts a SET name = u.email
FROM users u
WHERE a.name = '' AND u.account_id = a.id AND u.role = 'owner';

-- 0005 allowed only 'owner'. Dropping and re-adding by name is idempotent;
-- the name is the one Postgres gave 0005's inline CHECK.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('owner', 'admin', 'member'));

-- The last-owner rule counts these.
CREATE INDEX IF NOT EXISTS users_account_role_idx ON users (account_id, role) WHERE status = 'active';

CREATE TABLE IF NOT EXISTS invitations (
    id            text        PRIMARY KEY CHECK (id ~ '^inv_[0-9A-HJKMNP-TV-Z]{26}$'),
    account_id    bigint      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    -- Normalised like users.email.
    email         text        NOT NULL CHECK (email <> '' AND char_length(email) <= 254),
    -- owner is granted by PATCH /members/{id}, never by invitation.
    role          text        NOT NULL CHECK (role IN ('admin', 'member')),
    -- SHA-256 of a 32-byte random token; the token travels only in the
    -- email link's fragment.
    token_digest  bytea       NOT NULL UNIQUE CHECK (octet_length(token_digest) = 32),
    -- Null once the inviter's row is gone; the invitation stays valid,
    -- because it was the account that invited.
    invited_by    bigint      REFERENCES users (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL,
    expires_at    timestamptz NOT NULL CHECK (expires_at > created_at),
    accepted_at   timestamptz,
    revoked_at    timestamptz,
    superseded_at timestamptz,
    CHECK (num_nonnulls(accepted_at, revoked_at, superseded_at) <= 1)
);

-- At most one invitation per account and address that is neither accepted,
-- revoked nor superseded. Expiry cannot be in an index predicate; a new
-- invitation (or "Send again") supersedes an expired one too.
CREATE UNIQUE INDEX IF NOT EXISTS invitations_open_idx ON invitations (account_id, email)
    WHERE accepted_at IS NULL AND revoked_at IS NULL AND superseded_at IS NULL;
