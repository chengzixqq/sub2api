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

func TestStaleMemberDispatchGrantMigrationKeepsExtrasAndAdvancesVersions(t *testing.T) {
	content, err := FS.ReadFile("261_revoke_stale_member_dispatch_grants.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "DELETE FROM user_group_account_grants g")
	require.Contains(t, sql, "g.origin = 'member'")
	require.Contains(t, sql, "NOT EXISTS ( SELECT 1 FROM account_groups ag")
	require.Contains(t, sql, "RETURNING g.user_id, g.group_id")
	require.Contains(t, sql, "nextval('user_group_account_policy_version_seq')")
	require.NotContains(t, sql, "origin = 'extra'")
	require.NotContains(t, sql, "DELETE FROM user_group_account_policies")
}
