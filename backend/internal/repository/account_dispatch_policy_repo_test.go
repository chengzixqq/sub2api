package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func newDispatchPolicyRepositoryMock(t *testing.T) (*accountRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	return &accountRepository{client: client, sql: db}, mock
}

func dispatchPolicyRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"user_id", "group_id", "version", "mode", "username", "email", "id", "origin", "name", "dispatch_scope", "member_account_id"})
}

func TestAccountDispatchPolicyRepository_ReadPreservesEmptyReplace(t *testing.T) {
	repo, mock := newDispatchPolicyRepositoryMock(t)
	// A removed account, invalid extra workspace grant, or empty list produces a
	// NULL account through the LEFT JOIN. The parent must still deny fallback.
	mock.ExpectQuery(`(?s)SELECT.*user_group_account_policies.*wg.enabled = TRUE`).
		WithArgs(int64(7), int64(9)).
		WillReturnRows(dispatchPolicyRows().AddRow(7, 9, 23, "replace", "user", "u@example.test", nil, "extra", "", "", nil))
	policy, err := repo.GetAccountDispatchPolicy(context.Background(), 7, 9)
	require.NoError(t, err)
	require.NotNil(t, policy)
	require.Equal(t, service.AccountDispatchModeReplace, policy.Mode)
	require.Empty(t, policy.Accounts)
	require.EqualValues(t, 23, policy.Version)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountDispatchPolicyRepository_ReadFailureDoesNotBecomeNoPolicy(t *testing.T) {
	repo, mock := newDispatchPolicyRepositoryMock(t)
	want := errors.New("database unavailable")
	mock.ExpectQuery(`(?s)SELECT.*user_group_account_policies`).WithArgs(int64(7), int64(9)).WillReturnError(want)
	policy, err := repo.GetAccountDispatchPolicy(context.Background(), 7, 9)
	require.Nil(t, policy)
	require.ErrorIs(t, err, want)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountDispatchPolicyRepository_AdminListPreservesIneligibleGrants(t *testing.T) {
	repo, mock := newDispatchPolicyRepositoryMock(t)
	mock.ExpectQuery(`(?s)SELECT p.user_id, p.group_id, p.version.*ineligible_reason.*FROM user_group_account_policies`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "group_id", "version", "mode", "username", "email", "account_id", "origin", "name", "dispatch_scope", "ineligible_reason"}).
			AddRow(7, 9, 12, "replace", "user", "u@example.test", 15, "member", "removed", "shared", "member_removed").
			AddRow(7, 9, 12, "replace", "user", "u@example.test", 16, "extra", "test", "authorized_only", nil))
	policies, err := repo.ListAccountDispatchPolicies(context.Background(), 9)
	require.NoError(t, err)
	require.Len(t, policies, 1)
	require.Len(t, policies[0].Accounts, 2)
	require.False(t, *policies[0].Accounts[0].Eligible)
	require.Equal(t, "member_removed", policies[0].Accounts[0].IneligibleReason)
	require.True(t, *policies[0].Accounts[1].Eligible)
	require.Empty(t, policies[0].Accounts[1].IneligibleReason)
	require.NoError(t, mock.ExpectationsWereMet())
}

func expectDispatchPolicyWriteLock(mock sqlmock.Sqlmock, version int64, grants ...service.AccountDispatchGrant) {
	mock.ExpectBegin()
	if len(grants) > 0 {
		mock.ExpectQuery(`(?s)SELECT id, platform, workspace_id FROM accounts.*ORDER BY id FOR NO KEY UPDATE`).
			WithArgs("{15}").
			WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "workspace_id"}).AddRow(15, "openai", 1))
	}
	mock.ExpectQuery(`SELECT id FROM users .* FOR NO KEY UPDATE`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
	mock.ExpectQuery(`SELECT platform FROM groups .* FOR NO KEY UPDATE`).WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"platform"}).AddRow("openai"))
	if len(grants) > 0 {
		mock.ExpectQuery(`(?s)SELECT account_id FROM account_groups.*ORDER BY account_id FOR UPDATE`).
			WithArgs(int64(9), "{15}").
			WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(15))
	}
	mock.ExpectExec(`(?s)INSERT INTO user_group_account_policies`).WithArgs(int64(7), int64(9), "replace").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`(?s)SELECT version FROM user_group_account_policies.*FOR UPDATE`).WithArgs(int64(7), int64(9)).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(version))
}

func TestAccountDispatchPolicyRepository_ConflictingVersionRollsBack(t *testing.T) {
	repo, mock := newDispatchPolicyRepositoryMock(t)
	expectDispatchPolicyWriteLock(mock, 41)
	mock.ExpectRollback()
	_, err := repo.PutAccountDispatchPolicy(context.Background(), &service.AccountDispatchPolicy{UserID: 7, GroupID: 9, Mode: "replace"}, 1)
	require.ErrorContains(t, err, "current=41")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountDispatchPolicyRepository_EmptyReplaceUsesNonReusableVersion(t *testing.T) {
	repo, mock := newDispatchPolicyRepositoryMock(t)
	expectDispatchPolicyWriteLock(mock, 0)
	mock.ExpectExec(`(?s)UPDATE user_group_account_policies.*`+regexp.QuoteMeta("nextval('user_group_account_policy_version_seq')")).
		WithArgs(int64(7), int64(9), "replace").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM user_group_account_grants`).WithArgs(int64(7), int64(9)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	mock.ExpectQuery(`(?s)SELECT.*user_group_account_policies`).WithArgs(int64(7), int64(9)).
		WillReturnRows(dispatchPolicyRows().AddRow(7, 9, 42, "replace", "user", "u@example.test", nil, nil, "", "", nil))
	policy, err := repo.PutAccountDispatchPolicy(context.Background(), &service.AccountDispatchPolicy{UserID: 7, GroupID: 9, Mode: "replace"}, 0)
	require.NoError(t, err)
	require.EqualValues(t, 42, policy.Version)
	require.Empty(t, policy.Accounts)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountDispatchPolicyRepository_BulkWritesMaximumGrantCount(t *testing.T) {
	repo, mock := newDispatchPolicyRepositoryMock(t)
	grants := make([]service.AccountDispatchGrant, 500)
	accountIDs := make([]int64, len(grants))
	origins := make([]string, len(grants))
	accountRows := sqlmock.NewRows([]string{"id", "platform", "workspace_id"})
	readRows := dispatchPolicyRows()
	for i := range grants {
		id := int64(i + 1000)
		grants[i] = service.AccountDispatchGrant{AccountID: id, Origin: service.AccountDispatchOriginExtra}
		accountIDs[i] = id
		origins[i] = service.AccountDispatchOriginExtra
		accountRows.AddRow(id, "openai", int64(1))
		readRows.AddRow(int64(7), int64(9), int64(42), "replace", "user", "u@example.test", id, "extra", "account", "shared", nil)
	}
	idArray, err := pq.Array(accountIDs).Value()
	require.NoError(t, err)
	originArray, err := pq.Array(origins).Value()
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, platform, workspace_id FROM accounts.*ORDER BY id FOR NO KEY UPDATE`).
		WithArgs(idArray).WillReturnRows(accountRows)
	mock.ExpectQuery(`SELECT id FROM users .* FOR NO KEY UPDATE`).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
	mock.ExpectQuery(`SELECT platform FROM groups .* FOR NO KEY UPDATE`).WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"platform"}).AddRow("openai"))
	mock.ExpectQuery(`(?s)SELECT account_id FROM account_groups.*ORDER BY account_id FOR UPDATE`).
		WithArgs(int64(9), idArray).WillReturnRows(sqlmock.NewRows([]string{"account_id"}))
	mock.ExpectExec(`(?s)INSERT INTO user_group_account_policies`).WithArgs(int64(7), int64(9), "replace").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`(?s)SELECT version FROM user_group_account_policies.*FOR UPDATE`).WithArgs(int64(7), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(0))
	mock.ExpectExec(`(?s)UPDATE user_group_account_policies.*nextval`).WithArgs(int64(7), int64(9), "replace").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM user_group_account_grants`).WithArgs(int64(7), int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`(?s)INSERT INTO user_group_account_grants.*FROM unnest`).
		WithArgs(int64(7), int64(9), idArray, originArray).WillReturnResult(sqlmock.NewResult(0, 500))
	mock.ExpectCommit()
	mock.ExpectQuery(`(?s)SELECT.*user_group_account_policies`).WithArgs(int64(7), int64(9)).
		WillReturnRows(readRows)

	policy, err := repo.PutAccountDispatchPolicy(context.Background(), &service.AccountDispatchPolicy{
		UserID: 7, GroupID: 9, Mode: service.AccountDispatchModeReplace, Accounts: grants,
	}, 0)
	require.NoError(t, err)
	require.Len(t, policy.Accounts, 500)
	require.Equal(t, int64(1000), policy.Accounts[0].AccountID)
	require.Equal(t, int64(1499), policy.Accounts[499].AccountID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountDispatchPolicyRepository_RevokedWorkspaceRollsBack(t *testing.T) {
	repo, mock := newDispatchPolicyRepositoryMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, platform, workspace_id FROM accounts.*ORDER BY id FOR NO KEY UPDATE`).
		WithArgs("{15}").WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "workspace_id"}).AddRow(15, "openai", 2))
	mock.ExpectQuery(`SELECT id FROM users .* FOR NO KEY UPDATE`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
	mock.ExpectQuery(`SELECT platform FROM groups .* FOR NO KEY UPDATE`).WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"platform"}).AddRow("openai"))
	mock.ExpectQuery(`(?s)SELECT account_id FROM account_groups.*ORDER BY account_id FOR UPDATE`).
		WithArgs(int64(9), "{15}").WillReturnRows(sqlmock.NewRows([]string{"account_id"}))
	mock.ExpectQuery(`(?s)SELECT wg.workspace_id FROM workspace_group_grants wg.*w.status = 'active'.*wg.enabled = TRUE.*FOR SHARE OF wg, w`).WithArgs("{2}", int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}))
	mock.ExpectRollback()
	_, err := repo.PutAccountDispatchPolicy(context.Background(), &service.AccountDispatchPolicy{UserID: 7, GroupID: 9, Mode: "replace", Accounts: []service.AccountDispatchGrant{{AccountID: 15, Origin: "extra"}}}, 41)
	require.ErrorContains(t, err, "workspace no longer has this group")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountDispatchPolicyRepository_MemberRevocationInvalidatesStaleVersion(t *testing.T) {
	repo, mock := newDispatchPolicyRepositoryMock(t)
	mock.ExpectQuery(`(?s)SELECT p.user_id, p.group_id.*FROM user_group_account_policies.*FOR UPDATE OF p`).
		WithArgs(int64(15), "{9}").WillReturnRows(sqlmock.NewRows([]string{"user_id", "group_id"}).AddRow(7, 9))
	mock.ExpectExec(`(?s)WITH revoked AS.*DELETE FROM user_group_account_grants AS g WHERE g.account_id = \$1 AND g.origin = 'member'.*RETURNING g.user_id, g.group_id.*UPDATE user_group_account_policies p.*nextval`).
		WithArgs(int64(15), "{9}").WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, deleteMemberAccountDispatchGrants(context.Background(), repo.client, 15, []int64{9}))
	// The structural edit advanced 41 -> 42; an editor loaded before removal
	// cannot save the old member grant even if the account later rejoins.
	expectDispatchPolicyWriteLock(mock, 42, service.AccountDispatchGrant{AccountID: 15, Origin: service.AccountDispatchOriginMember})
	mock.ExpectRollback()
	_, err := repo.PutAccountDispatchPolicy(context.Background(), &service.AccountDispatchPolicy{UserID: 7, GroupID: 9, Mode: "replace", Accounts: []service.AccountDispatchGrant{{AccountID: 15, Origin: "member"}}}, 41)
	require.ErrorContains(t, err, "current=42")
	require.NoError(t, mock.ExpectationsWereMet())
}
