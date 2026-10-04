-- A per-key monthly spending cap, in quota units (500000 per USD 1):
-- accounts.md open point 3, the console's per-token quota, at the key grain.
-- Null is no cap. Checked by the local biller when a hold is placed, against
-- the key's holds opened this UTC month (open holds at their full quota,
-- closed ones at what they settled), so two submits on one key cannot both
-- squeeze under the cap.
--
-- Re-runnable, like every migration here.
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS monthly_cap bigint CHECK (monthly_cap IS NULL OR monthly_cap > 0);
