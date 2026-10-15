-- Finalized dashboard hours remain reusable until their source changes.
-- No usage_logs columns, rows or indexes are rebuilt by this migration.
-- Namespace 1937072738 is shared with dashboard_aggregation_repo.go.
-- Only new publishers using the transactional source-lock protocol certify a
-- bucket. Legacy app/blue workers remain compatible, but their writes revoke
-- certification so they can never overwrite an invalidation with trusted data.
-- This adds metadata to the small summary table, not the usage_logs source.
ALTER TABLE usage_dashboard_hourly ADD COLUMN IF NOT EXISTS source_verified BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE usage_dashboard_hourly ADD COLUMN IF NOT EXISTS source_finalized BOOLEAN NOT NULL DEFAULT false;
-- This frontier records source removal, not MIN(created_at): a late old insert
-- cannot prove that intervening raw hours survived retention. Publishers use a
-- compatible SHARE row lock; only automatic retention advances it exclusively.
CREATE TABLE IF NOT EXISTS usage_dashboard_source_state (
    id SMALLINT PRIMARY KEY CHECK (id = 1),
    raw_retained_from TIMESTAMPTZ NOT NULL DEFAULT TIMESTAMPTZ 'epoch',
    repair_before TIMESTAMPTZ NOT NULL DEFAULT TIMESTAMPTZ 'infinity'
);
INSERT INTO usage_dashboard_source_state (id) VALUES (1) ON CONFLICT DO NOTHING;
CREATE OR REPLACE FUNCTION certify_dashboard_usage_hour()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.computed_at = TIMESTAMPTZ 'epoch'
        AND ROW(NEW.total_requests, NEW.input_tokens, NEW.output_tokens,
            NEW.cache_creation_tokens, NEW.cache_read_tokens, NEW.total_cost,
            NEW.actual_cost, NEW.account_cost, NEW.total_duration_ms, NEW.active_users)
        IS NOT DISTINCT FROM ROW(OLD.total_requests, OLD.input_tokens, OLD.output_tokens,
            OLD.cache_creation_tokens, OLD.cache_read_tokens, OLD.total_cost,
            OLD.actual_cost, OLD.account_cost, OLD.total_duration_ms, OLD.active_users) THEN
        -- Invalidation preserves publication provenance. computed_at revokes
        -- freshness; it must not turn a legacy unverified snapshot into a
        -- rebuildable one or discard an already certified snapshot's provenance.
        NEW.source_verified := OLD.source_verified;
        NEW.source_finalized := OLD.source_finalized;
    ELSE
        NEW.source_verified := COALESCE(current_setting('sub2api.dashboard_verified_publish', true) = 'true', false);
        NEW.source_finalized := NEW.source_verified AND NEW.computed_at >= NEW.bucket_start + INTERVAL '1 hour';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS dashboard_hourly_certify_source ON usage_dashboard_hourly;
CREATE TRIGGER dashboard_hourly_certify_source BEFORE INSERT OR UPDATE ON usage_dashboard_hourly
FOR EACH ROW EXECUTE FUNCTION certify_dashboard_usage_hour();

CREATE OR REPLACE FUNCTION invalidate_dashboard_usage_hour()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    affected TIMESTAMPTZ;
    affected_hours TIMESTAMPTZ[];
BEGIN
    -- Raw retention and aggregate retention have independent windows. Automatic
    -- retention deletion preserves older summaries; explicit deletion invalidates.
    IF TG_OP = 'INSERT' THEN
        -- INSERT's group invalidator runs AFTER STATEMENT, later than row
        -- triggers. Take its compatible lock BEFORE acquiring any hour lock.
        -- This also applies to direct leaf writes (row triggers are cloned).
        PERFORM id FROM usage_group_rollup_state WHERE id = 1 FOR KEY SHARE;
        affected_hours := ARRAY[NEW.created_at];
    ELSIF TG_OP = 'DELETE' THEN
        affected_hours := ARRAY[OLD.created_at];
    ELSE
        affected_hours := ARRAY[OLD.created_at, NEW.created_at];
    END IF;
    FOR affected IN
        SELECT DISTINCT date_trunc('hour', t)
        FROM unnest(affected_hours) AS t ORDER BY 1
    LOOP
        -- Keep this shared lock even for current-hour inserts: the transaction
        -- may commit after rollover while a publisher is closing that hour.
        -- Shared ingest locks do not conflict; publishers lock only closed hours.
        PERFORM pg_advisory_xact_lock_shared(1937072738, floor(EXTRACT(EPOCH FROM affected)/3600)::integer);
        IF affected + INTERVAL '1 hour' <= clock_timestamp()
            AND NOT (TG_OP = 'DELETE' AND COALESCE(current_setting('sub2api.dashboard_preserve_aggregates', true) = 'true', false)) THEN
            UPDATE usage_dashboard_hourly SET computed_at = TIMESTAMPTZ 'epoch'
            WHERE bucket_start = affected AND computed_at <> TIMESTAMPTZ 'epoch';
        END IF;
    END LOOP;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;

-- Row triggers automatically clone onto existing and newly attached partitions,
-- including writes directly to a leaf. UPDATE/DELETE group row invalidators
-- sort before this z trigger; INSERT explicitly takes the group lock first.
DROP TRIGGER IF EXISTS usage_logs_z_dashboard_invalidate_insert ON usage_logs;
CREATE TRIGGER usage_logs_z_dashboard_invalidate_insert
BEFORE INSERT ON usage_logs FOR EACH ROW EXECUTE FUNCTION invalidate_dashboard_usage_hour();
DROP TRIGGER IF EXISTS usage_logs_z_dashboard_invalidate_delete ON usage_logs;
CREATE TRIGGER usage_logs_z_dashboard_invalidate_delete
AFTER DELETE ON usage_logs FOR EACH ROW EXECUTE FUNCTION invalidate_dashboard_usage_hour();
DROP TRIGGER IF EXISTS usage_logs_z_dashboard_invalidate_update ON usage_logs;
CREATE TRIGGER usage_logs_z_dashboard_invalidate_update
AFTER UPDATE OF created_at, user_id, input_tokens, output_tokens, cache_creation_tokens,
    cache_read_tokens, total_cost, actual_cost, provider_cost_recorded,
    account_stats_cost, account_rate_multiplier, duration_ms ON usage_logs
FOR EACH ROW EXECUTE FUNCTION invalidate_dashboard_usage_hour();

-- Preexisting buckets keep their values and computed_at unchanged, but start
-- unverified. The worker repairs at most four raw-covered hours per cycle,
-- newest first. Summary values older than raw retention are never cleared by
-- initial rebuilding. A legacy publisher always revokes source_verified.
