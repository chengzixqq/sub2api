CREATE TABLE IF NOT EXISTS admin_user_adjustments (
    id BIGSERIAL PRIMARY KEY,
    action_id UUID NOT NULL,
    kind VARCHAR(20) NOT NULL,
    operation VARCHAR(20) NOT NULL,
    requested_value DECIMAL(20, 8),
    delta DECIMAL(20, 8) NOT NULL,
    before_value DECIMAL(20, 8),
    after_value DECIMAL(20, 8),
    user_id BIGINT,
    user_email VARCHAR(255),
    user_name VARCHAR(100),
    operator_user_id BIGINT,
    operator_email VARCHAR(255),
    operator_name VARCHAR(100),
    notes TEXT,
    client_ip VARCHAR(64),
    auth_method VARCHAR(32),
    request_id VARCHAR(128),
    source VARCHAR(64) NOT NULL,
    legacy_redeem_code_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT admin_user_adjustments_kind_check
        CHECK (kind IN ('balance', 'concurrency')),
    CONSTRAINT admin_user_adjustments_operation_check
        CHECK (operation IN ('add', 'subtract', 'set', 'legacy')),
    CONSTRAINT admin_user_adjustments_delta_check
        CHECK (delta <> 0),
    CONSTRAINT admin_user_adjustments_values_check
        CHECK (
            before_value IS NULL OR after_value IS NULL
            OR before_value + delta = after_value
        ),
    CONSTRAINT admin_user_adjustments_concurrency_integral_check
        CHECK (
            operation = 'legacy'
            OR kind <> 'concurrency'
            OR (
                delta = TRUNC(delta)
                AND (requested_value IS NULL OR requested_value = TRUNC(requested_value))
                AND (before_value IS NULL OR before_value = TRUNC(before_value))
                AND (after_value IS NULL OR after_value = TRUNC(after_value))
            )
        )
);

CREATE INDEX IF NOT EXISTS idx_admin_user_adjustments_created
    ON admin_user_adjustments (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_admin_user_adjustments_user_created
    ON admin_user_adjustments (user_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_admin_user_adjustments_operator_created
    ON admin_user_adjustments (operator_user_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_admin_user_adjustments_kind_created
    ON admin_user_adjustments (kind, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_admin_user_adjustments_action
    ON admin_user_adjustments (action_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_admin_user_adjustments_action_user_kind
    ON admin_user_adjustments (action_id, user_id, kind)
    WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_admin_user_adjustments_legacy_redeem
    ON admin_user_adjustments (legacy_redeem_code_id)
    WHERE legacy_redeem_code_id IS NOT NULL;

INSERT INTO admin_user_adjustments (
    action_id,
    kind,
    operation,
    delta,
    user_id,
    user_email,
    user_name,
    notes,
    source,
    legacy_redeem_code_id,
    created_at
)
SELECT
    md5('legacy-redeem-code:' || rc.id::text)::uuid,
    CASE rc.type
        WHEN 'admin_balance' THEN 'balance'
        WHEN 'admin_concurrency' THEN 'concurrency'
    END,
    'legacy',
    rc.value,
    rc.used_by,
    u.email,
    NULLIF(u.username, ''),
    rc.notes,
    'legacy_redeem_code',
    rc.id,
    COALESCE(rc.used_at, rc.created_at)
FROM redeem_codes rc
LEFT JOIN users u ON u.id = rc.used_by
WHERE rc.type IN ('admin_balance', 'admin_concurrency')
  AND rc.value <> 0
ON CONFLICT (legacy_redeem_code_id) WHERE legacy_redeem_code_id IS NOT NULL
DO NOTHING;

CREATE OR REPLACE FUNCTION reject_admin_user_adjustment_mutation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'admin_user_adjustments is append-only';
END;
$$;

DROP TRIGGER IF EXISTS admin_user_adjustments_immutable ON admin_user_adjustments;
CREATE TRIGGER admin_user_adjustments_immutable
BEFORE UPDATE OR DELETE ON admin_user_adjustments
FOR EACH ROW
EXECUTE FUNCTION reject_admin_user_adjustment_mutation();
