-- One row per transactional email compute has decided to send about one
-- thing (internal/notify; docs/spec/notifications.md, "Sent once"). The key
-- is (account, subject kind, subject id, message): the job emails use
-- ('job', job id, 'job-succeeded'), the top-up receipt ('topup', top-up id,
-- 'topup-received'). Later messages (refunds, low balance, invitations)
-- reuse the table with their own subject kinds; none needs a migration.
--
-- The row is the claim: it is inserted before the first address is mailed,
-- so a retried webhook, a second process or a restarted loop finds it and
-- sends nothing. 'failed' means SES refused every recipient and the next
-- trigger may try again; a row left 'claimed' (the process died mid-send) is
-- never retried, because whether the mail went out is unknown.
--
-- No foreign key to accounts, for 0006's reason: the record outlives what it
-- names. Re-runnable (IF NOT EXISTS) for store's TestMigrateConcurrent.

CREATE TABLE IF NOT EXISTS notifications (
    account_id   bigint      NOT NULL,
    subject_kind text        NOT NULL CHECK (subject_kind ~ '^[a-z][a-z_]{0,31}$'),
    subject_id   text        NOT NULL CHECK (char_length(subject_id) BETWEEN 1 AND 128),
    -- The mailer's message kind, e.g. job-failed.
    message      text        NOT NULL CHECK (message ~ '^[a-z][a-z0-9-]{0,63}$'),
    state        text        NOT NULL DEFAULT 'claimed' CHECK (state IN ('claimed', 'sent', 'failed')),
    attempts     integer     NOT NULL DEFAULT 1 CHECK (attempts >= 1),
    -- How many addresses SES accepted on the last attempt.
    delivered    integer     NOT NULL DEFAULT 0 CHECK (delivered >= 0),
    last_error   text,
    claimed_at   timestamptz NOT NULL,
    sent_at      timestamptz,
    PRIMARY KEY (account_id, subject_kind, subject_id, message),
    CHECK ((state = 'sent') = (sent_at IS NOT NULL))
);
