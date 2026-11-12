package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountDispatchPolicyMigrationKeepsEmptyReplaceDistinct(t *testing.T) {
	content, err := FS.ReadFile("260_account_dispatch_policy.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "dispatch_scope VARCHAR(24) NOT NULL DEFAULT 'shared'")
	require.Contains(t, sql, "user_group_account_policies")
	require.Contains(t, sql, "mode IN ('append', 'replace')")
	require.Contains(t, sql, "CREATE SEQUENCE IF NOT EXISTS user_group_account_policy_version_seq AS BIGINT")
	require.Contains(t, sql, "version BIGINT NOT NULL DEFAULT nextval('user_group_account_policy_version_seq')")
	require.Contains(t, sql, "user_group_account_grants")
	require.Contains(t, sql, "origin IN ('member', 'extra')")
	require.Contains(t, sql, "PRIMARY KEY (user_id, group_id)")
	require.NotContains(t, sql, "DROP TABLE")
}
