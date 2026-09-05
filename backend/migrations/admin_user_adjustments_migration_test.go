package migrations

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminUserAdjustmentsMigrationContract(t *testing.T) {
	content, err := FS.ReadFile("223_admin_user_adjustments.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS admin_user_adjustments")
	require.Contains(t, sql, "requested_value DECIMAL(20, 8)")
	require.Contains(t, sql, "delta DECIMAL(20, 8) NOT NULL")
	require.Contains(t, sql, "before_value DECIMAL(20, 8)")
	require.Contains(t, sql, "after_value DECIMAL(20, 8)")
	require.Contains(t, sql, "operator_name VARCHAR(100)")
	require.Contains(t, sql, "user_id BIGINT, user_email")
	require.Contains(t, sql, "CHECK (delta <> 0)")
	require.Contains(t, sql, "before_value + delta = after_value")
	require.Contains(t, sql, "operation IN ('add', 'subtract', 'set', 'legacy')")
	require.Contains(t, sql, "operation = 'legacy' OR kind <> 'concurrency'")
}

func TestAdminUserAdjustmentsMigrationBackfillsOnlyProvableLegacyFields(t *testing.T) {
	content, err := FS.ReadFile("223_admin_user_adjustments.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")

	require.Contains(t, sql, "WHERE rc.type IN ('admin_balance', 'admin_concurrency')")
	require.Contains(t, sql, "AND rc.value <> 0")
	require.NotContains(t, sql, "rc.used_by IS NOT NULL")
	require.Contains(t, sql, "COALESCE(rc.used_at, rc.created_at)")
	require.Contains(t, sql, "'legacy'")
	require.Contains(t, sql, "'legacy_redeem_code'")
	require.Contains(t, sql, "ON CONFLICT (legacy_redeem_code_id) WHERE legacy_redeem_code_id IS NOT NULL DO NOTHING")

	insertStart := strings.Index(sql, "INSERT INTO admin_user_adjustments")
	require.Greater(t, insertStart, -1)
	selectStart := strings.Index(sql[insertStart:], ") SELECT")
	require.Greater(t, selectStart, -1)
	columns := sql[insertStart : insertStart+selectStart]
	require.NotContains(t, columns, "requested_value")
	require.NotContains(t, columns, "before_value")
	require.NotContains(t, columns, "after_value")
	require.NotContains(t, columns, "operator_user_id")
	require.NotContains(t, columns, "operator_email")
	require.NotContains(t, columns, "operator_name")
}

func TestAdminUserAdjustmentsMigrationIsAppendOnly(t *testing.T) {
	content, err := FS.ReadFile("223_admin_user_adjustments.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")

	require.Contains(t, sql, "BEFORE UPDATE OR DELETE ON admin_user_adjustments")
	require.Contains(t, sql, "RAISE EXCEPTION 'admin_user_adjustments is append-only'")
	require.Contains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS idx_admin_user_adjustments_legacy_redeem")
	require.Contains(t, sql, "WHERE legacy_redeem_code_id IS NOT NULL")
	require.Contains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS idx_admin_user_adjustments_action_user_kind")
	require.Contains(t, sql, "ON admin_user_adjustments (action_id, user_id, kind) WHERE user_id IS NOT NULL")
}

func TestAdminUserAdjustmentsMigrationFollowsPreviousLocalMigration(t *testing.T) {
	entries, err := FS.ReadDir(".")
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	slices.Sort(names)

	previous := slices.Index(names, "222_enable_channel_monitor_v2_by_default.sql")
	adjustments := slices.Index(names, "223_admin_user_adjustments.sql")
	require.GreaterOrEqual(t, previous, 0)
	require.Greater(t, adjustments, previous)
	// The official v177 rollup migration shares the 222 prefix and sorts
	// between the local 222 and 223 files by complete filename.
	require.Contains(t, names[previous+1:adjustments], "222_group_usage_daily_rollups.sql")
}
