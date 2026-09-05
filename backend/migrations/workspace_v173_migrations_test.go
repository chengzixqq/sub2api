package migrations

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkspaceV173MigrationsRestorePricesAndEnableV2(t *testing.T) {
	restoreContent, err := FS.ReadFile("221_restore_non_grok_video_generation_config.sql")
	require.NoError(t, err)
	restoreSQL := strings.Join(strings.Fields(string(restoreContent)), " ")
	require.Contains(t, restoreSQL, "UPDATE groups AS g")
	require.Contains(t, restoreSQL, "FROM groups_video_price_backup_220 AS b")
	require.Contains(t, restoreSQL, "video_price_480p = b.video_price_480p")
	require.Contains(t, restoreSQL, "video_price_720p = b.video_price_720p")
	require.Contains(t, restoreSQL, "video_price_1080p = b.video_price_1080p")
	require.Contains(t, restoreSQL, "video_model_prices = b.video_model_prices")
	require.NotContains(t, strings.ToUpper(restoreSQL), "DROP TABLE")

	modeContent, err := FS.ReadFile("222_enable_channel_monitor_v2_by_default.sql")
	require.NoError(t, err)
	modeSQL := strings.Join(strings.Fields(string(modeContent)), " ")
	require.Contains(t, modeSQL, "VALUES ('channel_monitor_mode', 'v2')")
	require.Contains(t, modeSQL, "ON CONFLICT (key) DO UPDATE")
}

func TestWorkspaceV173MigrationFilenamesSortAfterOfficialCleanup(t *testing.T) {
	entries, err := FS.ReadDir(".")
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	slices.Sort(names)

	cleanup := slices.Index(names, "220_clear_non_grok_video_generation_config.sql")
	groupPricing := slices.Index(names, "221_group_model_pricing.sql")
	restore := slices.Index(names, "221_restore_non_grok_video_generation_config.sql")
	enableV2 := slices.Index(names, "222_enable_channel_monitor_v2_by_default.sql")
	require.GreaterOrEqual(t, cleanup, 0)
	require.Equal(t, cleanup+1, groupPricing)
	require.Equal(t, groupPricing+1, restore)
	require.Equal(t, restore+1, enableV2)
}
