-- Probe coalescing attribution. User billing remains per request; provider
-- cost is recorded only for the request that performed the real upstream call.
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS probe_coalesced BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS probe_leader_request_id VARCHAR(128),
    ADD COLUMN IF NOT EXISTS provider_cost_recorded BOOLEAN NOT NULL DEFAULT TRUE;

COMMENT ON COLUMN usage_logs.probe_coalesced IS
    'True when the downstream probe response was synthesized from a coalesced leader';
COMMENT ON COLUMN usage_logs.probe_leader_request_id IS
    'Request ID of the real upstream probe used as the coalescing leader';
COMMENT ON COLUMN usage_logs.provider_cost_recorded IS
    'True when this usage row represents a real upstream provider/account cost';
