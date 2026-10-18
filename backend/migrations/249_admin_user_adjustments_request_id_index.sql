-- Reconciling an unanswered balance adjustment: the App sends its operation id as X-Request-ID and later looks the
-- ledger row up by it (GET /admin/user-adjustments?request_id=...). action_id and user_id are already indexed (223).

CREATE INDEX IF NOT EXISTS idx_admin_user_adjustments_request_id
    ON admin_user_adjustments (request_id) WHERE request_id IS NOT NULL;
