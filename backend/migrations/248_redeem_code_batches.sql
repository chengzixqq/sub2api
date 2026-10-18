-- Redeem code batches: one generate call is one batch, identified by batch_id (the client's operation id, or derived
-- from its Idempotency-Key). Re-sending the same batch_id returns the existing codes instead of generating a second
-- batch, and lets a client that lost the response find the batch again. created_by records the admin who generated it.
-- Both columns stay NULL for codes created before this migration and for admin adjustment records.

ALTER TABLE redeem_codes ADD COLUMN IF NOT EXISTS batch_id UUID;
ALTER TABLE redeem_codes ADD COLUMN IF NOT EXISTS created_by BIGINT;

CREATE INDEX IF NOT EXISTS idx_redeem_codes_batch_id
    ON redeem_codes (batch_id) WHERE batch_id IS NOT NULL;

-- Lists sort by creation and filter by a creation window when reconciling a batch.
CREATE INDEX IF NOT EXISTS idx_redeem_codes_created_at
    ON redeem_codes (created_at DESC, id DESC);
