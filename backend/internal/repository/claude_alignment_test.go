package repository

import (
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/ent"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAlignmentCandidateQueries_ProjectOnlyOverridesAndIDs(t *testing.T) {
	var query string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(captureEntQueryMatcher{actual: &query}))
	require.NoError(t, err)
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	types := []string{service.AccountTypeAPIKey, service.AccountTypeOAuth, service.AccountTypeSetupToken}
	mock.ExpectQuery("snapshot").WithArgs("claude_customization_overrides", `{"anthropic","antigravity"}`, `{"apikey","oauth","setup-token"}`, "active").
		WillReturnRows(sqlmock.NewRows([]string{"id", "type", "platform", "overrides", "schedulable", "scope", "mixed", "groups"}).
			AddRow(3, "apikey", "anthropic", `{"thinking_cache_control":"reject"}`, true, "shared", false, "{8}").
			AddRow(4, "oauth", "anthropic", `{"context_management_missing_beta":"reject","thinking_cache_control":"reject","cache_control_limit_mode":"reject"}`, true, "shared", false, "{8}").
			AddRow(5, "setup-token", "antigravity", `{"unknown_beta_header":"reject"}`, true, "shared", false, "{8}"))
	settings := &settingRepository{client: client}
	overrides, err := settings.ListClaudeAlignmentOverrides(t.Context(), types)
	require.NoError(t, err)
	require.Equal(t, []service.ClaudeAlignmentOverrides{
		{AccountID: 3, AccountType: "apikey", Platform: "anthropic", Schedulable: true, DispatchScope: "shared", GroupIDs: []int64{8}, Fields: map[string]any{"thinking_cache_control": "reject"}},
		{AccountID: 4, AccountType: "oauth", Platform: "anthropic", Schedulable: true, DispatchScope: "shared", GroupIDs: []int64{8}, Fields: map[string]any{"context_management_missing_beta": "reject", "thinking_cache_control": "reject", "cache_control_limit_mode": "reject"}},
		{AccountID: 5, AccountType: "setup-token", Platform: "antigravity", Schedulable: true, DispatchScope: "shared", GroupIDs: []int64{8}, Fields: map[string]any{"unknown_beta_header": "reject"}},
	}, overrides)
	require.Contains(t, normalizeSQLWhitespace(query), "SELECT id, type, platform, extra -> $1::text, schedulable")

	require.Regexp(t, `extra\s*\?\s*\$1::text\s+AND jsonb_typeof\(extra -> \$1::text\) = 'object'`, query)

	groupID := int64(12)
	mock.ExpectQuery("membership").WithArgs(4, "anthropic", "apikey", "oauth", "setup-token", "active", service.DispatchScopeAuthorizedOnly, groupID).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(4))
	repo := newAccountRepositoryWithSQL(client, db, nil)
	found, err := repo.HasGroupAlignmentCandidate(t.Context(), types, []int64{4}, &groupID, false, service.PlatformAnthropic, false)
	require.NoError(t, err)
	require.True(t, found)
	projection, predicates, ok := strings.Cut(normalizeSQLWhitespace(query), " FROM ")
	require.True(t, ok)
	require.Contains(t, projection, `"id"`)
	require.NotContains(t, projection, ",")
	require.Contains(t, predicates, "account_groups")
	require.Contains(t, predicates, "dispatch_scope")
	mock.ExpectQuery("mixed membership").WithArgs(5, "antigravity", "apikey", "oauth", "setup-token", "active", service.DispatchScopeAuthorizedOnly, `{"mixed_scheduling":true}`, groupID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	found, err = repo.HasGroupAlignmentCandidate(t.Context(), types, []int64{5}, &groupID, false, service.PlatformAntigravity, true)
	require.NoError(t, err)
	require.False(t, found)
	require.Contains(t, query, "@>")
	require.NoError(t, mock.ExpectationsWereMet())
}
