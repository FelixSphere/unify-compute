-- Account listings for the template marketplace (ADR-0012 §2, spec/marketplace.md
-- M2), and the template a job was submitted with.
--
-- A listing is one templates.json entry an account asked to publish. It
-- reaches buyers only when listed AND review_state = 'approved'; the super
-- admin approves with a reason (admin CLI). Any change to what a buyer
-- trusts returns it to pending (internal/listings). Unify Data's brokered
-- market has the same shape: products.review_state, reviewed_by, listed.
--
-- Re-runnable (IF NOT EXISTS), like every migration here.

CREATE TABLE IF NOT EXISTS listings (
    id            text        PRIMARY KEY CHECK (id ~ '^lst_[0-9A-HJKMNP-TV-Z]{26}$'),
    account_id    bigint      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    -- The slug buyers submit as `template`. Unique across accounts, and
    -- internal/listings refuses one that collides with templates.json.
    template_id   text        NOT NULL UNIQUE CHECK (template_id ~ '^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$'),
    -- One templates.json entry, validated by internal/template before it
    -- is stored, so a listing that would not boot as a curated template
    -- cannot exist.
    template      jsonb       NOT NULL CHECK (jsonb_typeof(template) = 'object'),
    review_state  text        NOT NULL DEFAULT 'pending' CHECK (review_state IN ('pending', 'approved', 'rejected')),
    review_reason text,
    reviewed_by   text,
    reviewed_at   timestamptz,
    listed        boolean     NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CHECK ((review_state = 'pending') = (reviewed_at IS NULL))
);

CREATE INDEX IF NOT EXISTS listings_account_idx ON listings (account_id, created_at);
-- The buyer-facing read: approved and listed, by slug.
CREATE INDEX IF NOT EXISTS listings_public_idx ON listings (template_id) WHERE listed AND review_state = 'approved';

-- The template a job was submitted with (null for a hand-written spec).
-- Publishers see run counts of their listings, never who ran them.
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS template_id text;
CREATE INDEX IF NOT EXISTS jobs_template_idx ON jobs (template_id) WHERE template_id IS NOT NULL;
