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
