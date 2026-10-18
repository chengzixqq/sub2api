-- Recreate ops_alert_silences.
--
-- 037_ops_alert_silences.sql is a goose-style file, but the migration runner executes a file as one script, so its
-- rollback half (which removes the table) ran right after the create: the table never existed on any database.
-- Creating a silence therefore failed and the alert evaluator could never see one. Same definition as 037; idempotent.

CREATE TABLE IF NOT EXISTS ops_alert_silences (
    id BIGSERIAL PRIMARY KEY,

    rule_id BIGINT NOT NULL,
    platform VARCHAR(64) NOT NULL,
    group_id BIGINT,
    region VARCHAR(64),

    until TIMESTAMPTZ NOT NULL,
    reason TEXT,

    created_by BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_ops_alert_silences_lookup
    ON ops_alert_silences (rule_id, platform, group_id, region, until);
