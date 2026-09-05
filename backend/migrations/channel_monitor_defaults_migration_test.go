package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChannelMonitorDefaultsMigration(t *testing.T) {
	content, err := FS.ReadFile("227_channel_monitor_defaults.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")

	require.Contains(t, sql, "VALUES ('channel_monitor_mode', 'v1', NOW())")
	require.Contains(t, sql, "ALTER TABLE channel_monitors ALTER COLUMN check_mode SET DEFAULT 'quota'")
	require.Contains(t, sql, "VALUES ('channel_monitor_show_quota', 'false', NOW())")
	require.NotContains(t, strings.ToLower(sql), "update channel_monitors")
}
