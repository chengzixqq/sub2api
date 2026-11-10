-- Group rollup scans must not hold the global ingest gate while reading history.
-- Only small metadata/aggregate publications use FOR UPDATE. Source writers retain
-- KEY SHARE until commit; writes in the protected history invalidate scan results.
-- Drain source writers before taking state DDL locks: writers acquire the source
-- relation before the state row. A busy upgrade aborts atomically for retry.
SET LOCAL lock_timeout = '250ms';
SET LOCAL statement_timeout = '2s';
LOCK TABLE usage_logs IN SHARE ROW EXCLUSIVE MODE;

ALTER TABLE usage_group_rollup_state
    ADD COLUMN IF NOT EXISTS source_revision BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS scan_before TIMESTAMPTZ NOT NULL DEFAULT '-infinity';

CREATE OR REPLACE FUNCTION bump_group_usage_rollup_revision()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.source_revision := OLD.source_revision + 1;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS group_usage_rollup_revision ON usage_group_rollup_state;
CREATE TRIGGER group_usage_rollup_revision BEFORE UPDATE ON usage_group_rollup_state
FOR EACH ROW EXECUTE FUNCTION bump_group_usage_rollup_revision();

CREATE OR REPLACE FUNCTION invalidate_group_usage_rollup_insert()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    published_before DATE;
    protected_before TIMESTAMPTZ;
    rollup_timezone TEXT;
    affected_date DATE;
BEGIN
    SELECT closed_before, scan_before, timezone_name
    INTO published_before, protected_before, rollup_timezone
    FROM usage_group_rollup_state WHERE id = 1 FOR KEY SHARE;
    affected_date := (NEW.created_at AT TIME ZONE rollup_timezone)::date;
    IF NEW.created_at < protected_before OR
       (NEW.group_id IS NOT NULL AND affected_date < published_before) THEN
        -- Ordinary non-key UPDATE is compatible with concurrent KEY SHARE holders.
        -- Do not upgrade multiple ingest transactions to explicit FOR UPDATE.
        UPDATE usage_group_rollup_state
        SET closed_before = CASE WHEN NEW.group_id IS NOT NULL
                THEN LEAST(closed_before, affected_date) ELSE closed_before END,
            updated_at = NOW()
        WHERE id = 1;
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS usage_logs_group_rollup_invalidate_insert ON usage_logs;
-- A row trigger is cloned onto partitions and covers direct leaf inserts. Its
-- name sorts before usage_logs_z_dashboard_* so group -> hour order is preserved.
CREATE TRIGGER usage_logs_group_rollup_invalidate_insert
BEFORE INSERT ON usage_logs FOR EACH ROW
EXECUTE FUNCTION invalidate_group_usage_rollup_insert();

CREATE OR REPLACE FUNCTION invalidate_group_usage_rollup_state()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    affected_at TIMESTAMPTZ;
    affected_date DATE;
    published_before DATE;
    protected_before TIMESTAMPTZ;
    rollup_timezone TEXT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        affected_at := OLD.created_at;
    ELSIF OLD.group_id IS NULL THEN
        affected_at := NEW.created_at;
    ELSIF NEW.group_id IS NULL THEN
        affected_at := OLD.created_at;
    ELSE
        affected_at := LEAST(OLD.created_at, NEW.created_at);
    END IF;
    SELECT closed_before, scan_before, timezone_name
    INTO published_before, protected_before, rollup_timezone
    FROM usage_group_rollup_state WHERE id = 1 FOR KEY SHARE;
    affected_date := (affected_at AT TIME ZONE rollup_timezone)::date;
    IF affected_at < protected_before OR affected_date < published_before THEN
        UPDATE usage_group_rollup_state
        SET closed_before = LEAST(closed_before, affected_date), updated_at = NOW()
        WHERE id = 1;
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;
