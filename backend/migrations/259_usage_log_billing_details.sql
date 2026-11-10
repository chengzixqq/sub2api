-- Optional per-model billing detail; existing rows remain NULL.
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS billing_details JSONB;
