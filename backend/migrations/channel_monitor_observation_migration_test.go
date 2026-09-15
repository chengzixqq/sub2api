package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChannelMonitorObservationMigrationDefinesIndependentRetentionTables(t *testing.T) {
	raw, err := FS.ReadFile("238_channel_monitor_observation.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(raw))
	for _, table := range []string{
		"channel_monitor_observation_config",
		"channel_monitor_observation_sessions",
		"channel_monitor_observation_gaps",
		"channel_monitor_observation_events",
		"channel_monitor_observation_attempts",
		"channel_monitor_observation_aggregates",
	} {
		require.Contains(t, sql, "create table if not exists "+table)
	}
	require.Contains(t, sql, "detail_retention_hours\":72")
	require.Contains(t, sql, "on conflict do nothing")
	require.Contains(t, sql, "on delete cascade")
}

func TestChannelMonitorObservationMigrationUsesBoundedAggregateBuckets(t *testing.T) {
	raw, err := FS.ReadFile("238_channel_monitor_observation.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(raw))
	require.Contains(t, sql, "check(bucket_seconds in (60,300,3600,43200,86400))")
	require.Contains(t, sql, "create or replace function channel_monitor_observation_sum")
	require.Contains(t, sql, "jsonb_each(right_value)")
}
