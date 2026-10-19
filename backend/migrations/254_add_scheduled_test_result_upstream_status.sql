-- Upstream HTTP status of a scheduled test result, so the App can color rows and spot credential errors
-- without parsing error_message. Written by the scheduled runner from the account test trace (falling back to
-- the status in the error text); NULL when no upstream response was seen. Existing rows stay NULL: results
-- roll over per plan (max_results), and readers fall back to parsing error_message.
-- error_kind is derived on read from status, upstream_status_code and error_message, so it has no column.

ALTER TABLE scheduled_test_results ADD COLUMN IF NOT EXISTS upstream_status_code INT NULL;
