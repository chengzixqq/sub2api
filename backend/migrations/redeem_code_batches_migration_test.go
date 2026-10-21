package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedeemCodeBatchesMigration(t *testing.T) {
	content, err := FS.ReadFile("248_redeem_code_batches.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ALTER TABLE redeem_codes ADD COLUMN IF NOT EXISTS batch_id UUID")
	require.Contains(t, sql, "ALTER TABLE redeem_codes ADD COLUMN IF NOT EXISTS created_by BIGINT")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_redeem_codes_batch_id ON redeem_codes (batch_id) WHERE batch_id IS NOT NULL")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_redeem_codes_created_at ON redeem_codes (created_at DESC, id DESC)")
	require.NotContains(t, sql, "+goose")
}

func TestAdminUserAdjustmentsRequestIDIndexMigration(t *testing.T) {
	content, err := FS.ReadFile("249_admin_user_adjustments_request_id_index.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_admin_user_adjustments_request_id ON admin_user_adjustments (request_id) WHERE request_id IS NOT NULL")
}

func TestAccountErrorAtMigration(t *testing.T) {
	content, err := FS.ReadFile("250_add_account_error_at.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ALTER TABLE accounts ADD COLUMN IF NOT EXISTS error_at TIMESTAMPTZ NULL")
	require.NotContains(t, sql, "UPDATE accounts")
	require.NotContains(t, sql, "+goose")
}

func TestAccountConfigVersionMigration(t *testing.T) {
	content, err := FS.ReadFile("251_add_account_config_version.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ALTER TABLE accounts ADD COLUMN IF NOT EXISTS config_version BIGINT NOT NULL DEFAULT 0")
	require.NotContains(t, sql, "+goose")
}

func TestOpsAlertEventRuleSnapshotMigration(t *testing.T) {
	content, err := FS.ReadFile("253_ops_alert_event_rule_snapshot.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ALTER TABLE ops_alert_events ADD COLUMN IF NOT EXISTS rule_snapshot JSONB")
	require.Contains(t, sql, "ALTER TABLE ops_alert_events ADD COLUMN IF NOT EXISTS resolved_by_user_id BIGINT")
	require.NotContains(t, sql, "UPDATE ")
	require.NotContains(t, sql, "+goose")
}

func TestScheduledTestResultUpstreamStatusMigration(t *testing.T) {
	content, err := FS.ReadFile("254_add_scheduled_test_result_upstream_status.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ALTER TABLE scheduled_test_results ADD COLUMN IF NOT EXISTS upstream_status_code INT NULL")
	require.NotContains(t, sql, "UPDATE ")
	require.NotContains(t, sql, "+goose")
}

func TestAccountTestRunsMigration(t *testing.T) {
	content, err := FS.ReadFile("255_account_test_runs.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS account_test_runs")
	require.Contains(t, sql, "account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE")
	require.Contains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS idx_account_test_runs_one_running ON account_test_runs (account_id) WHERE status = 'running'")
	require.NotContains(t, sql, "DROP ")
	require.NotContains(t, sql, "+goose")
}

// E10: each ledger row points at its compatibility redeem_codes row, so balance-history can show before/after. The
// column is new and nullable: older rows are matched at query time, and the append-only ledger is never updated.
func TestAdminUserAdjustmentsRedeemCodeIDMigration(t *testing.T) {
	content, err := FS.ReadFile("256_admin_user_adjustments_redeem_code_id.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ALTER TABLE admin_user_adjustments ADD COLUMN IF NOT EXISTS redeem_code_id BIGINT")
	require.Contains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS idx_admin_user_adjustments_redeem_code ON admin_user_adjustments (redeem_code_id) WHERE redeem_code_id IS NOT NULL")
	require.NotContains(t, sql, "UPDATE ")
	require.NotContains(t, sql, "TRIGGER")
	require.NotContains(t, sql, "+goose")
}
