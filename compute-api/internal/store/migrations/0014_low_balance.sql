-- The low-balance warning (task W6; docs/spec/accounts.md, "Low-balance
-- warning"). low_balance_quota is the threshold the account holder sets on
-- the Billing page, in quota units (500000 per USD 1) like every balance
-- column; the default is USD 5 (operator decision 2026-10-14) and 0 turns
-- the warning off. The dashboard shows a banner while available is below it.
--
-- low_balance_armed says the next drop of available below the threshold is
-- a downward crossing that gets one email. The local biller clears it, under
-- the account lock, in the transaction whose entry crossed, and the entry's
-- id names that crossing in notifications (subject 'balance'), so each
-- crossing is mailed once. A credit (top-up, operator credit) that leaves
-- available at or above the threshold sets it again; a hold released at a
-- job's end does not, or every job of an account near its threshold would
-- send one. Changing the threshold sets it to available >= the new value.
--
-- Re-runnable for TestMigrateConcurrent: the columns are added, and existing
-- accounts armed from their current balance, only when the columns do not
-- exist yet, so a second run does not reset an account's state.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns
                   WHERE table_schema = current_schema() AND table_name = 'accounts' AND column_name = 'low_balance_quota') THEN
        ALTER TABLE accounts
            ADD COLUMN low_balance_quota bigint NOT NULL DEFAULT 2500000 CHECK (low_balance_quota >= 0),
            ADD COLUMN low_balance_armed boolean NOT NULL DEFAULT false;
        UPDATE accounts SET low_balance_armed = available >= low_balance_quota;
    END IF;
END
$$;
