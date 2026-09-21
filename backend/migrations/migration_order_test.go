package migrations

import (
	"io/fs"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDuplicate238MigrationsHaveStableDictionaryOrder locks the execution
// order of the three same-number migrations.  The runner sorts filenames, so
// adding another 238 file must not silently change the dependency sequence.
func TestDuplicate238MigrationsHaveStableDictionaryOrder(t *testing.T) {
	files, err := fs.Glob(FS, "238_*.sql")
	require.NoError(t, err)
	require.Equal(t, []string{
		"238_channel_monitor_observation.sql",
		"238_opencode_go_platform.sql",
		"238_purge_unlimited_user_platform_quotas.sql",
	}, func() []string {
		sort.Strings(files)
		return files
	}())
}
