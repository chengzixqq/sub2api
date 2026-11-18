-- Bulk historical DELETE/UPDATE must not rewrite the state row once per source row.
--
-- 258/262 bump source_revision for every protected-history row so that an
-- optimistic scan is rejected. Within one transaction every bump after the
-- first is redundant: publishers compare the revision for equality only, and
-- neither registration nor publication (both FOR UPDATE on the state row) can
-- run while this writer still holds its lock on that row. Repeating the UPDATE
-- made a 10000-row retention batch rewrite one row 10000 times inside the 1s
-- statement budget, so automatic retention rolled back on every cycle.
--
-- The marker keeps the top-level transaction id in a transaction-local setting.
-- A rolled-back savepoint reverts the marker together with its UPDATE, and a
-- value left by any other transaction can never equal the current id.
-- Moving closed_before back is never skipped.
-- Replacing the function takes no lock on usage_logs or on the state table.
CREATE OR REPLACE FUNCTION invalidate_group_usage_rollup_state()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    affected_at TIMESTAMPTZ;
    affected_date DATE;
    published_before DATE;
    protected_before TIMESTAMPTZ;
    rollup_timezone TEXT;
    current_xact TEXT;
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
        current_xact := pg_current_xact_id()::text;
        IF affected_date < published_before
            OR current_setting('sub2api.group_rollup_bumped_xact', true) IS DISTINCT FROM current_xact THEN
            UPDATE usage_group_rollup_state
            SET closed_before = LEAST(closed_before, affected_date), updated_at = NOW()
            WHERE id = 1;
            PERFORM set_config('sub2api.group_rollup_bumped_xact', current_xact, true);
        END IF;
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;
