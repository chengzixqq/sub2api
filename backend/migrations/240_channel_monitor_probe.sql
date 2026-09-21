-- Additive and independent of legacy monitor history and business billing.
CREATE TABLE IF NOT EXISTS channel_monitor_probe_targets (
    id BIGSERIAL PRIMARY KEY,
    group_id BIGINT NOT NULL REFERENCES groups(id),
    model VARCHAR(200) NOT NULL,
    protocol VARCHAR(32) NOT NULL CHECK (protocol IN ('openai_chat','openai_responses','anthropic','gemini')),
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_probe_at TIMESTAMPTZ,
    lease_until TIMESTAMPTZ,
    UNIQUE (group_id,model,protocol)
);
CREATE TABLE IF NOT EXISTS channel_monitor_probe_budgets (
    utc_day DATE NOT NULL,
    target_id BIGINT NOT NULL,
    used INTEGER NOT NULL DEFAULT 0 CHECK (used >= 0),
    PRIMARY KEY (utc_day,target_id)
);
CREATE TABLE IF NOT EXISTS channel_monitor_probe_runs (
    id VARCHAR(64) PRIMARY KEY,
    target_id BIGINT NOT NULL REFERENCES channel_monitor_probe_targets(id),
    idempotency_key VARCHAR(64) NOT NULL,
    manual BOOLEAN NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    result JSONB NOT NULL,
    UNIQUE(target_id,idempotency_key)
);
CREATE INDEX IF NOT EXISTS idx_channel_monitor_probe_runs_recent ON channel_monitor_probe_runs(target_id,started_at DESC);
CREATE TABLE IF NOT EXISTS channel_monitor_probe_quotas (
    account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    next_poll_at TIMESTAMPTZ NOT NULL,
    snapshot JSONB,
    fetched_at TIMESTAMPTZ
);
