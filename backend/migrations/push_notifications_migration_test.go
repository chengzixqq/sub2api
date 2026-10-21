package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A11: push targets, their deliveries (one row per device and event, the unique key makes each event go out once
// across instances) and the indexes the dispatcher's event scans need. The whole file runs as one script, so it
// must be idempotent and must not carry a goose Down section.
func TestPushNotificationsMigration(t *testing.T) {
	content, err := FS.ReadFile("257_push_notifications.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")

	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS push_devices")
	require.Contains(t, sql, "user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE")
	require.Contains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS idx_push_devices_platform_token ON push_devices (platform, token_hash)")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_push_devices_user ON push_devices (user_id, id)")

	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS push_deliveries")
	require.Contains(t, sql, "device_id BIGINT NOT NULL REFERENCES push_devices(id) ON DELETE CASCADE")
	require.Contains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS idx_push_deliveries_device_dedup ON push_deliveries (device_id, dedup_key)")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_push_deliveries_due ON push_deliveries (next_attempt_at) WHERE status IN ('pending', 'sending')")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_push_deliveries_created ON push_deliveries (created_at)")

	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_ops_alert_events_resolved_at ON ops_alert_events (resolved_at) WHERE resolved_at IS NOT NULL")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_accounts_error_at ON accounts (error_at) WHERE error_at IS NOT NULL")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_account_test_runs_finished_at ON account_test_runs (finished_at) WHERE finished_at IS NOT NULL")

	require.NotContains(t, sql, "CONCURRENTLY")
	require.NotContains(t, sql, "DROP ")
	require.NotContains(t, sql, "UPDATE ")
	require.NotContains(t, sql, "+goose")
}
