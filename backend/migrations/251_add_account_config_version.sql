-- Optimistic concurrency for admin account edits (PATCH /admin/accounts/:id with If-Match).
-- updated_at is unusable as a version: last_used flushes, rate limits and other runtime writes bump it every few
-- seconds. config_version only moves on configuration writes (full update, PATCH, bulk edit).

ALTER TABLE accounts ADD COLUMN IF NOT EXISTS config_version BIGINT NOT NULL DEFAULT 0;
