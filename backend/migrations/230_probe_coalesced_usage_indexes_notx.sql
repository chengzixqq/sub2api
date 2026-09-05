-- These indexes are intentionally non-transactional so a large usage_logs
-- table remains writable while the index build runs during deployment.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_probe_leader_request_id
    ON usage_logs (probe_leader_request_id)
    WHERE probe_leader_request_id IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_provider_cost_created_at
    ON usage_logs (provider_cost_recorded, created_at);
