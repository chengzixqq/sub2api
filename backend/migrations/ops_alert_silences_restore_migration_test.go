package migrations

import (
	"io/fs"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The runner executes a migration file as one script (repository/migrations_runner.go), so a goose-style file runs its
// “-- +goose Down” half right after the Up half. 037_ops_alert_silences.sql therefore created and immediately dropped
// ops_alert_silences on every database; 247 recreates it.
func TestRestoreOpsAlertSilencesMigration(t *testing.T) {
	content, err := FS.ReadFile("247_restore_ops_alert_silences.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS ops_alert_silences")
	for _, column := range []string{"rule_id BIGINT NOT NULL", "platform VARCHAR(64) NOT NULL", "group_id BIGINT", "region VARCHAR(64)", "until TIMESTAMPTZ NOT NULL", "reason TEXT", "created_by BIGINT", "created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()"} {
		require.Contains(t, sql, column)
	}
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_ops_alert_silences_lookup ON ops_alert_silences (rule_id, platform, group_id, region, until)")
	require.NotContains(t, strings.ToUpper(sql), "DROP ")
	require.NotContains(t, sql, "+goose")
}

// TestNoNewGooseDownMigrations keeps the runner's one-script contract: a “-- +goose Down” section would be executed as
// part of the migration. The three historical files are frozen (their checksums are recorded on every deployment).
func TestNoNewGooseDownMigrations(t *testing.T) {
	files, err := fs.Glob(FS, "*.sql")
	require.NoError(t, err)
	var withDown []string
	for _, name := range files {
		content, err := FS.ReadFile(name)
		require.NoError(t, err)
		if strings.Contains(string(content), "+goose Down") {
			withDown = append(withDown, name)
		}
	}
	sort.Strings(withDown)
	require.Equal(t, []string{
		"019_migrate_wechat_to_attributes.sql",
		"024_add_gemini_tier_id.sql",
		"037_ops_alert_silences.sql",
	}, withDown, "new migrations must not contain a goose Down section: the runner would execute it")
}
