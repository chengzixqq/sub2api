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

func expectDispatchPolicyWriteLock(mock sqlmock.Sqlmock, version int64) {
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM users .* FOR NO KEY UPDATE`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
	mock.ExpectQuery(`SELECT id FROM groups .* FOR NO KEY UPDATE`).WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(9))
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

func TestAccountDispatchPolicyRepository_RevokedWorkspaceRollsBack(t *testing.T) {
	repo, mock := newDispatchPolicyRepositoryMock(t)
	expectDispatchPolicyWriteLock(mock, 41)
	mock.ExpectExec(`(?s)UPDATE user_group_account_policies.*nextval`).WithArgs(int64(7), int64(9), "replace").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM user_group_account_grants`).WithArgs(int64(7), int64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT a.platform, a.workspace_id, grp.platform.*FOR NO KEY UPDATE`).WithArgs(int64(15), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"platform", "workspace_id", "group_platform"}).AddRow("openai", 2, "openai"))
	mock.ExpectQuery(`SELECT 1 FROM account_groups.*FOR UPDATE`).WithArgs(int64(15), int64(9)).WillReturnRows(sqlmock.NewRows([]string{"exists"}))
	mock.ExpectQuery(`(?s)SELECT 1 FROM workspace_group_grants wg.*w.status = 'active'.*wg.enabled = TRUE.*FOR SHARE OF wg, w`).WithArgs(int64(2), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}))
	mock.ExpectRollback()
	_, err := repo.PutAccountDispatchPolicy(context.Background(), &service.AccountDispatchPolicy{UserID: 7, GroupID: 9, Mode: "replace", Accounts: []service.AccountDispatchGrant{{AccountID: 15, Origin: "extra"}}}, 41)
	require.ErrorContains(t, err, "workspace no longer has this group")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountDispatchPolicyRepository_MemberRevocationInvalidatesStaleVersion(t *testing.T) {
	repo, mock := newDispatchPolicyRepositoryMock(t)
	mock.ExpectExec(`(?s)WITH revoked AS.*DELETE FROM user_group_account_grants WHERE account_id = \$1 AND origin = 'member'.*RETURNING user_id, group_id.*UPDATE user_group_account_policies p.*nextval`).
		WithArgs(int64(15), "{9}").WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, deleteMemberAccountDispatchGrants(context.Background(), repo.client, 15, []int64{9}))
	// The structural edit advanced 41 -> 42; an editor loaded before removal
	// cannot save the old member grant even if the account later rejoins.
	expectDispatchPolicyWriteLock(mock, 42)
	mock.ExpectRollback()
	_, err := repo.PutAccountDispatchPolicy(context.Background(), &service.AccountDispatchPolicy{UserID: 7, GroupID: 9, Mode: "replace", Accounts: []service.AccountDispatchGrant{{AccountID: 15, Origin: "member"}}}, 41)
	require.ErrorContains(t, err, "current=42")
	require.NoError(t, mock.ExpectationsWereMet())
}
