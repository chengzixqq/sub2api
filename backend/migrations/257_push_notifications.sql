-- Outbound push (A11): admins register push targets (APNs device tokens, Bark, ntfy, generic webhooks) and subscribe
-- them to alert / account events. A background dispatcher scans recent events and writes one push_deliveries row per
-- device and event; the unique (device_id, dedup_key) key makes each event go out once even with several instances.
-- credentials holds the token / secret / endpoint as JSON, encrypted when a fixed secret encryption key is configured
-- (credentials_encrypted). Only the device owner can read or change a device; the plaintext is never returned.

CREATE TABLE IF NOT EXISTS push_devices (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    platform VARCHAR(16) NOT NULL,
    token_hash CHAR(64) NOT NULL,
    credentials TEXT NOT NULL,
    credentials_encrypted BOOLEAN NOT NULL DEFAULT FALSE,
    token_hint VARCHAR(8) NOT NULL DEFAULT '',
    endpoint_host VARCHAR(255) NOT NULL DEFAULT '',
    bundle_id VARCHAR(155) NOT NULL DEFAULT '',
    environment VARCHAR(16) NOT NULL DEFAULT '',
    device_name VARCHAR(64) NOT NULL DEFAULT '',
    locale VARCHAR(35) NOT NULL DEFAULT '',
    timezone VARCHAR(64) NOT NULL DEFAULT '',
    site_tag VARCHAR(64) NOT NULL DEFAULT '',
    prefs JSONB NOT NULL,
    disabled_reason VARCHAR(32) NOT NULL DEFAULT '',
    disabled_at TIMESTAMPTZ NULL,
    last_success_at TIMESTAMPTZ NULL,
    last_error TEXT NOT NULL DEFAULT '',
    last_error_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_push_devices_platform_token ON push_devices (platform, token_hash);
CREATE INDEX IF NOT EXISTS idx_push_devices_user ON push_devices (user_id, id);

-- status: pending | sending | sent | failed | skipped (quiet hours, device gone). A sending row whose lease
-- (next_attempt_at) expired was left by a crashed instance and is claimed again.
CREATE TABLE IF NOT EXISTS push_deliveries (
    id BIGSERIAL PRIMARY KEY,
    device_id BIGINT NOT NULL REFERENCES push_devices(id) ON DELETE CASCADE,
    dedup_key VARCHAR(160) NOT NULL,
    event_type VARCHAR(32) NOT NULL,
    payload JSONB NOT NULL,
    status VARCHAR(16) NOT NULL,
    attempts INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    sent_at TIMESTAMPTZ NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_push_deliveries_device_dedup ON push_deliveries (device_id, dedup_key);
CREATE INDEX IF NOT EXISTS idx_push_deliveries_due ON push_deliveries (next_attempt_at) WHERE status IN ('pending', 'sending');
CREATE INDEX IF NOT EXISTS idx_push_deliveries_created ON push_deliveries (created_at);

-- Event scans of the dispatcher (fired_at already has idx_ops_alert_events_fired_at).
CREATE INDEX IF NOT EXISTS idx_ops_alert_events_resolved_at ON ops_alert_events (resolved_at) WHERE resolved_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_accounts_error_at ON accounts (error_at) WHERE error_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_account_test_runs_finished_at ON account_test_runs (finished_at) WHERE finished_at IS NOT NULL;
