-- Compact telemetry is a new epoch. Existing observation data remains untouched.
CREATE TABLE IF NOT EXISTS channel_monitor_compact_config (
 id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK(id), version INTEGER NOT NULL DEFAULT 1,
 config JSONB NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- Copy thresholds once; a legacy live policy must never enable a new collector epoch.
INSERT INTO channel_monitor_compact_config(id,config)
SELECT TRUE, config || '{"version":1,"mode":"shadow","live_group_ids":[],"detail_retention_hours":24,"probe_enabled":false,"quota_enabled":true}'::jsonb
FROM channel_monitor_observation_config WHERE id=TRUE
ON CONFLICT DO NOTHING;
INSERT INTO channel_monitor_compact_config(id,config) VALUES(TRUE,
'{"version":1,"enabled":true,"mode":"shadow","detail_retention_hours":24,"refresh_interval_seconds":60,"minimum_sample":50,"healthy_reliability":0.99,"warning_reliability":0.95,"warning_ttft_ms":3000,"critical_ttft_ms":10000,"overrides":[],"probe_enabled":false,"quota_enabled":true}'::jsonb)
ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS channel_monitor_compact_dedup (
 request_id UUID PRIMARY KEY,
 fingerprint BYTEA NOT NULL CHECK(octet_length(fingerprint)=32),
 received_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_monitor_compact_dedup_received ON channel_monitor_compact_dedup(received_at);
ALTER TABLE channel_monitor_compact_dedup SET (autovacuum_vacuum_scale_factor=0.02,autovacuum_analyze_scale_factor=0.02);

CREATE TABLE IF NOT EXISTS channel_monitor_compact_minute (
 bucket_start TIMESTAMPTZ NOT NULL,
 source TEXT NOT NULL CHECK(source IN ('traffic','probe')),
 platform VARCHAR(64) NOT NULL, group_id BIGINT NOT NULL, model VARCHAR(192) NOT NULL, protocol VARCHAR(64) NOT NULL,
 facts JSONB NOT NULL, computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(bucket_start,source,platform,group_id,model,protocol)
) PARTITION BY RANGE(bucket_start);
CREATE INDEX IF NOT EXISTS idx_monitor_compact_minute_group ON channel_monitor_compact_minute(group_id,bucket_start);
CREATE TABLE IF NOT EXISTS channel_monitor_compact_hour (
 LIKE channel_monitor_compact_minute INCLUDING DEFAULTS INCLUDING CONSTRAINTS,
 PRIMARY KEY(bucket_start,source,platform,group_id,model,protocol)
) PARTITION BY RANGE(bucket_start);
CREATE INDEX IF NOT EXISTS idx_monitor_compact_hour_group ON channel_monitor_compact_hour(group_id,bucket_start);

CREATE TABLE IF NOT EXISTS channel_monitor_compact_attempt_minute (
 bucket_start TIMESTAMPTZ NOT NULL,
 source TEXT NOT NULL CHECK(source IN ('traffic','probe')),
 platform VARCHAR(64) NOT NULL, group_id BIGINT NOT NULL, model VARCHAR(192) NOT NULL, protocol VARCHAR(64) NOT NULL,
 account_id BIGINT NOT NULL CHECK(account_id>0), facts JSONB NOT NULL,
 computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(bucket_start,source,platform,group_id,model,protocol,account_id)
) PARTITION BY RANGE(bucket_start);
CREATE INDEX IF NOT EXISTS idx_monitor_compact_attempt_minute_group ON channel_monitor_compact_attempt_minute(group_id,bucket_start);
CREATE TABLE IF NOT EXISTS channel_monitor_compact_attempt_hour (
 LIKE channel_monitor_compact_attempt_minute INCLUDING DEFAULTS INCLUDING CONSTRAINTS,
 PRIMARY KEY(bucket_start,source,platform,group_id,model,protocol,account_id)
) PARTITION BY RANGE(bucket_start);
CREATE INDEX IF NOT EXISTS idx_monitor_compact_attempt_hour_group ON channel_monitor_compact_attempt_hour(group_id,bucket_start);

CREATE TABLE IF NOT EXISTS channel_monitor_compact_writer_sessions (
 id UUID PRIMARY KEY, started_at TIMESTAMPTZ NOT NULL, heartbeat_at TIMESTAMPTZ NOT NULL,
 data_through TIMESTAMPTZ, last_ingested_at TIMESTAMPTZ, last_write_error BOOLEAN NOT NULL DEFAULT FALSE,
 ended_at TIMESTAMPTZ, in_flight BIGINT NOT NULL DEFAULT 0, dropped_events BIGINT NOT NULL DEFAULT 0,
 pending_events BIGINT NOT NULL DEFAULT 0 CHECK(pending_events>=0)
);
CREATE INDEX IF NOT EXISTS idx_monitor_compact_sessions_time ON channel_monitor_compact_writer_sessions(started_at);
CREATE TABLE IF NOT EXISTS channel_monitor_compact_gaps (
 session_id UUID NOT NULL, started_at TIMESTAMPTZ NOT NULL, ended_at TIMESTAMPTZ NOT NULL,
 reason VARCHAR(64) NOT NULL, lost_events BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY(session_id,started_at,reason)
);
CREATE INDEX IF NOT EXISTS idx_monitor_compact_gaps_time ON channel_monitor_compact_gaps(ended_at);

CREATE TABLE IF NOT EXISTS channel_monitor_compact_samples (
 request_id UUID NOT NULL, completed_at TIMESTAMPTZ NOT NULL,
 source TEXT NOT NULL CHECK(source IN ('traffic','probe')),
 group_id BIGINT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('success','failure')),
 facts JSONB NOT NULL CHECK(octet_length(facts::text)<=16384),
 PRIMARY KEY(request_id,completed_at)
) PARTITION BY RANGE(completed_at);
CREATE INDEX IF NOT EXISTS idx_monitor_compact_samples_time ON channel_monitor_compact_samples(completed_at);
CREATE TABLE IF NOT EXISTS channel_monitor_compact_sample_budgets (
 day DATE NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('success','failure')),
 used INTEGER NOT NULL DEFAULT 0 CHECK(used>=0), PRIMARY KEY(day,kind)
);

CREATE OR REPLACE FUNCTION channel_monitor_compact_ensure_partitions(at_time TIMESTAMPTZ)
RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE table_name TEXT; child_name TEXT; day_start TIMESTAMPTZ; offset_days INTEGER;
BEGIN
 FOR offset_days IN -1..2 LOOP
  day_start := ((at_time AT TIME ZONE 'UTC')::date + offset_days)::timestamp AT TIME ZONE 'UTC';
  FOREACH table_name IN ARRAY ARRAY['channel_monitor_compact_minute','channel_monitor_compact_hour','channel_monitor_compact_attempt_minute','channel_monitor_compact_attempt_hour','channel_monitor_compact_samples'] LOOP
   child_name := table_name || '_' || to_char(day_start AT TIME ZONE 'UTC','YYYYMMDD');
   IF to_regclass(child_name) IS NULL THEN
    PERFORM pg_advisory_xact_lock(hashtext('channel-monitor-compact-partitions'));
    IF to_regclass(child_name) IS NULL THEN
     EXECUTE format('CREATE TABLE %I PARTITION OF %I FOR VALUES FROM (%L) TO (%L)',child_name,table_name,day_start,day_start+INTERVAL '24 hours');
    END IF;
   END IF;
  END LOOP;
 END LOOP;
END;
$$;
SELECT channel_monitor_compact_ensure_partitions(NOW());
