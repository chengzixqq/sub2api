-- E10: each new ledger row points at the compatibility redeem_codes row written in the same transaction, so
-- GET /admin/users/:id/balance-history can show the before / after values of an admin adjustment. Rows written before
-- this column are matched at query time (legacy_redeem_code_id, or same user / timestamp / amount); the append-only
-- ledger is never updated, so there is no backfill. No foreign key: a compatibility row may be gone.

ALTER TABLE admin_user_adjustments ADD COLUMN IF NOT EXISTS redeem_code_id BIGINT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_admin_user_adjustments_redeem_code
    ON admin_user_adjustments (redeem_code_id)
    WHERE redeem_code_id IS NOT NULL;
