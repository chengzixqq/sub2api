-- Independent terminal facts: no billing history or legacy monitor table is changed.
CREATE TABLE IF NOT EXISTS channel_monitor_observation_config (
 id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK(id), version INTEGER NOT NULL DEFAULT 1,
 config JSONB NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
INSERT INTO channel_monitor_observation_config(id,config) VALUES(TRUE,
'{"version":1,"enabled":true,"mode":"shadow","detail_retention_hours":72,"refresh_interval_seconds":60,"minimum_sample":50,"healthy_reliability":0.99,"warning_reliability":0.95,"warning_ttft_ms":3000,"critical_ttft_ms":10000,"overrides":[]}'::jsonb)
ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS channel_monitor_observation_sessions (
 id UUID PRIMARY KEY, started_at TIMESTAMPTZ NOT NULL, heartbeat_at TIMESTAMPTZ NOT NULL,
 ended_at TIMESTAMPTZ, in_flight BIGINT NOT NULL DEFAULT 0, dropped_events BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_monitor_observation_sessions_time ON channel_monitor_observation_sessions(started_at);

CREATE TABLE IF NOT EXISTS channel_monitor_observation_gaps (
 session_id UUID NOT NULL, started_at TIMESTAMPTZ NOT NULL, ended_at TIMESTAMPTZ NOT NULL,
 reason VARCHAR(64) NOT NULL, lost_events BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY(session_id,started_at,reason)
);
CREATE INDEX IF NOT EXISTS idx_monitor_observation_gaps_time ON channel_monitor_observation_gaps(ended_at);

CREATE TABLE IF NOT EXISTS channel_monitor_observation_events (
 request_id UUID PRIMARY KEY, session_id UUID NOT NULL, completed_at TIMESTAMPTZ NOT NULL,
 platform VARCHAR(64) NOT NULL, group_id BIGINT NOT NULL, model VARCHAR(192) NOT NULL,
 fingerprint VARCHAR(64) NOT NULL, facts JSONB NOT NULL,
 aggregated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_monitor_observation_events_time ON channel_monitor_observation_events(completed_at);
CREATE INDEX IF NOT EXISTS idx_monitor_observation_events_group_time ON channel_monitor_observation_events(group_id,completed_at);

CREATE TABLE IF NOT EXISTS channel_monitor_observation_attempts (
 request_id UUID NOT NULL REFERENCES channel_monitor_observation_events(request_id) ON DELETE CASCADE,
 sequence SMALLINT NOT NULL CHECK(sequence BETWEEN 1 AND 16), facts JSONB NOT NULL,
 PRIMARY KEY(request_id,sequence)
);

CREATE TABLE IF NOT EXISTS channel_monitor_observation_aggregates (
 bucket_seconds INTEGER NOT NULL CHECK(bucket_seconds IN (60,300,3600,43200,86400)),
 bucket_start TIMESTAMPTZ NOT NULL, platform VARCHAR(64) NOT NULL, group_id BIGINT NOT NULL,
 model VARCHAR(192) NOT NULL, facts JSONB NOT NULL, computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(bucket_seconds,bucket_start,platform,group_id,model)
);
CREATE INDEX IF NOT EXISTS idx_monitor_observation_aggregate_group_time ON channel_monitor_observation_aggregates(group_id,bucket_start);

-- Sum bounded numeric counters and histograms without replacing existing buckets.
CREATE OR REPLACE FUNCTION channel_monitor_observation_sum(left_value JSONB,right_value JSONB)
RETURNS JSONB LANGUAGE plpgsql IMMUTABLE STRICT AS $$
DECLARE result JSONB := left_value; item RECORD;
BEGIN
 FOR item IN SELECT key,value FROM jsonb_each(right_value) LOOP
  IF jsonb_typeof(item.value) = 'number' THEN
   result := jsonb_set(result,ARRAY[item.key],to_jsonb(COALESCE((result->>item.key)::BIGINT,0)+(item.value::TEXT)::BIGINT),TRUE);
  ELSIF jsonb_typeof(item.value) = 'object' THEN
   result := jsonb_set(result,ARRAY[item.key],channel_monitor_observation_sum(COALESCE(result->item.key,'{}'::JSONB),item.value),TRUE);
  END IF;
 END LOOP;
 RETURN result;
END;
$$;
