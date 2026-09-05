package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProbeCoalescedUsageMigrationKeepsColumnsIdempotent(t *testing.T) {
	content, err := FS.ReadFile("229_probe_coalesced_usage.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS probe_coalesced BOOLEAN NOT NULL DEFAULT FALSE")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS probe_leader_request_id VARCHAR(128)")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS provider_cost_recorded BOOLEAN NOT NULL DEFAULT TRUE")
	require.NotContains(t, sql, "CREATE INDEX")
}

func TestProbeCoalescedUsageIndexesUseConcurrentBuilds(t *testing.T) {
	content, err := FS.ReadFile("230_probe_coalesced_usage_indexes_notx.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_probe_leader_request_id")
	require.Contains(t, sql, "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_provider_cost_created_at")
	require.NotContains(t, strings.ToUpper(sql), "BEGIN")
	require.NotContains(t, strings.ToUpper(sql), "COMMIT")
}
