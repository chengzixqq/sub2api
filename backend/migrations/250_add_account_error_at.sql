-- When an account entered the error state. The App shows "错误于 …" without parsing error_message;
-- the column is set whenever status becomes 'error' and cleared when the account leaves it.
-- Existing error accounts stay NULL: updated_at changes on every write, so it is not a reliable backfill.

ALTER TABLE accounts ADD COLUMN IF NOT EXISTS error_at TIMESTAMPTZ NULL;
