-- Customer support conversations (ADR-0014; docs/spec/accounts.md "Support
-- conversations"): a signed-in user writes, the operator answers with
-- `compute-api admin support`, the user is emailed that there is an answer.
--
-- A conversation belongs to one user, not the account: what a member writes
-- to support is not shown to the account's other members. No foreign key to
-- users or accounts, as elsewhere: the record outlives what it names.
-- Re-runnable (IF NOT EXISTS) for store's TestMigrateConcurrent.

CREATE TABLE IF NOT EXISTS support_conversations (
    id         text        PRIMARY KEY CHECK (id ~ '^sc_[0-9A-HJKMNP-TV-Z]{26}$'),
    account_id bigint      NOT NULL,
    user_id    bigint      NOT NULL,
    -- open: the newest message is the customer's and waits for a person.
    -- answered: the newest message is staff's. closed: by either side.
    state      text        NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'answered', 'closed')),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    closed_at  timestamptz,
    -- 'user', or the operator's name for a close from the admin CLI.
    closed_by  text,
    CHECK ((state = 'closed') = (closed_at IS NOT NULL))
);

-- One live conversation per user: a second message continues it.
CREATE UNIQUE INDEX IF NOT EXISTS support_conversations_one_live_idx ON support_conversations (user_id) WHERE state <> 'closed';
CREATE INDEX IF NOT EXISTS support_conversations_user_idx ON support_conversations (user_id, created_at);
CREATE INDEX IF NOT EXISTS support_conversations_waiting_idx ON support_conversations (updated_at) WHERE state = 'open';

CREATE TABLE IF NOT EXISTS support_messages (
    -- A number, so "everything after message N" is one comparison for
    -- whoever follows a conversation from outside (the operator's tooling).
    id              bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    conversation_id text        NOT NULL REFERENCES support_conversations (id),
    from_staff      boolean     NOT NULL,
    -- The user's id for a customer message, the operator's name for staff's.
    author          text        NOT NULL,
    body            text        NOT NULL CHECK (length(body) BETWEEN 1 AND 4000),
    created_at      timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS support_messages_conversation_idx ON support_messages (conversation_id, id);
