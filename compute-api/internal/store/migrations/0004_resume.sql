-- What a compute-api process needs to pick up jobs another process was
-- running (docs/spec/backend.md, "Restart"). The attempt's UP time and last
-- observed UP are already durable in the ledger's metering_attempts (0002);
-- this adds the two things that were only in memory or only in the request.
--
-- Re-runnable (IF NOT EXISTS) for the reason 0002 and 0003 give:
-- TestMigrateConcurrent drops jobs and schema_migrations and migrates again.

-- The placement candidates upstream.Registry.Place resolved at submit, in
-- catalogue order (a JSON array of sku.Provider). A job that is relaunched
-- after a restart must go where the customer's `providers` allowed and the
-- deployment enabled at submit, not wherever the SKU could go today. Null on
-- rows written before this migration; the SKU's own providers stand in.
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS candidates jsonb
    CHECK (candidates IS NULL OR jsonb_typeof(candidates) = 'array');

-- A customer cancel accepted by a process that does not hold the worker lock
-- (store.WorkerLock). Only the lock holder drives jobs, so it carries the
-- cancel out; accepted_at is where GPU billing stops (metering.md B11),
-- however long the hand-over takes. One row per job: a second cancel of the
-- same job changes nothing.
CREATE TABLE IF NOT EXISTS job_cancel_requests (
    job_id      text        PRIMARY KEY,
    accepted_at timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
