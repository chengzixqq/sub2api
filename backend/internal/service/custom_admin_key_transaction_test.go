//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

func TestScopedAdminAfterCommitEffects(t *testing.T) {
	for _, mode := range []string{"commit", "rollback", "commit_failure"} {
		t.Run(mode, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			mock.ExpectBegin()
			tx, err := client.Tx(context.Background())
			require.NoError(t, err)
			calls := 0
			AfterAccountCommit(dbent.NewTxContext(context.Background(), tx), func(ctx context.Context) {
				require.Nil(t, dbent.TxFromContext(ctx), "提交后回调不能复用已关闭的事务")
				calls++
			})
			require.Zero(t, calls)
			switch mode {
			case "commit":
				mock.ExpectCommit()
				require.NoError(t, tx.Commit())
				require.Equal(t, 1, calls)
			case "rollback":
				mock.ExpectRollback()
				require.NoError(t, tx.Rollback())
				require.Zero(t, calls)
			case "commit_failure":
				mock.ExpectCommit().WillReturnError(errors.New("提交失败"))
				require.Error(t, tx.Commit())
				require.Zero(t, calls)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
	calls := 0
	AfterAccountCommit(context.Background(), func(context.Context) { calls++ })
	require.Equal(t, 1, calls, "无事务时保留原管理员同步语义")
}

func TestAccountGroupSnapshotPublishesAfterCommit(t *testing.T) {
	account := &Account{ID: 19, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, GroupIDs: []int64{4},
		Extra: map[string]any{AccountClaudeCustomizationExtraKey: map[string]any{"request_id_mode": "fill"}}}
	root := &projectedQueueSettings{queueAlignmentSettings: &queueAlignmentSettings{accounts: []*Account{account}}}
	settings := NewSettingService(root, nil)
	settings.alignmentHits = NewAlignmentHitRecorder(&memoryHitStore{})
	before := settings.alignmentSnapshot(t.Context())
	require.Equal(t, []int64{4}, before.routing.accounts[19].GroupIDs)
	admin := &adminServiceImpl{accountRepo: &updateAccountCredsRepoStub{account: account}, settingService: settings}

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	mock.ExpectBegin()
	tx, err := client.Tx(t.Context())
	require.NoError(t, err)
	empty := []int64{}
	_, err = admin.UpdateAccount(dbent.NewTxContext(t.Context(), tx), 19, &UpdateAccountInput{GroupIDs: &empty, SkipMixedChannelCheck: true})
	require.NoError(t, err)
	require.Same(t, before, settings.alignmentRejectCache.Load())
	// The root reader sees the new bindings only once the transaction commits.
	root.accounts[0].GroupIDs = empty
	mock.ExpectCommit()
	require.NoError(t, tx.Commit())
	require.Empty(t, settings.alignmentRejectCache.Load().routing.accounts[19].GroupIDs)
	require.Equal(t, 2, root.reads)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountStatusAndMixedSchedulingRefreshSnapshot(t *testing.T) {
	account := &Account{ID: 21, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Status: StatusActive}
	root := &queueAlignmentSettings{}
	settings := NewSettingService(root, nil)
	settings.alignmentHits = NewAlignmentHitRecorder(&memoryHitStore{})
	settings.alignmentSnapshot(t.Context())
	admin := &adminServiceImpl{accountRepo: &updateAccountCredsRepoStub{account: account}, settingService: settings}
	_, err := admin.UpdateAccount(t.Context(), 21, &UpdateAccountInput{Status: StatusDisabled})
	require.NoError(t, err)
	require.Equal(t, 2, root.reads)
	_, err = admin.PatchAccount(t.Context(), 21, &PatchAccountInput{ExtraPatch: map[string]any{"mixed_scheduling": true}})
	require.NoError(t, err)
	require.Equal(t, 3, root.reads)
	base := &accountRepoStubForClearAccountError{account: account}
	admin.accountRepo = &conditionalClearRepoStub{accountRepoStubForClearAccountError: base}
	account.Status = StatusDisabled
	skipped, err := admin.ClearAccountErrorWithOptions(t.Context(), 21, ClearAccountErrorOptions{PreserveInactive: true})
	require.NoError(t, err)
	require.True(t, skipped.SkippedInactive)
	require.Equal(t, 3, root.reads)
	_, err = admin.ClearAccountError(t.Context(), 21)
	require.NoError(t, err)
	require.Equal(t, 4, root.reads)
	require.NoError(t, admin.SetAccountError(t.Context(), 21, "failed"))
	require.Equal(t, 5, root.reads)
	account.Status = StatusError
	cleared, err := admin.ClearAccountErrorWithOptions(t.Context(), 21, ClearAccountErrorOptions{PreserveInactive: true})
	require.NoError(t, err)
	require.False(t, cleared.SkippedInactive)
	require.Equal(t, 6, root.reads)

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	for _, commit := range []bool{false, true} {
		mock.ExpectBegin()
		tx, err := client.Tx(t.Context())
		require.NoError(t, err)
		txCtx := dbent.NewTxContext(t.Context(), tx)
		require.NoError(t, admin.SetAccountError(txCtx, 21, "fault"))
		_, err = admin.ClearAccountErrorWithOptions(txCtx, 21, ClearAccountErrorOptions{PreserveInactive: true})
		require.NoError(t, err)
		require.Equal(t, 6, root.reads)
		if commit {
			mock.ExpectCommit()
			require.NoError(t, tx.Commit())
			require.Equal(t, 8, root.reads)
		} else {
			mock.ExpectRollback()
			require.NoError(t, tx.Rollback())
			require.Equal(t, 6, root.reads)
		}
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBulkGroupBindingsRefreshSnapshotOnce(t *testing.T) {
	root := &queueAlignmentSettings{}
	settings := NewSettingService(root, nil)
	settings.alignmentHits = NewAlignmentHitRecorder(&memoryHitStore{})
	admin := &adminServiceImpl{accountRepo: &accountRepoStubForBulkUpdate{}, settingService: settings}
	groups := []int64{}
	results, err := admin.BulkUpdateAccounts(WithScope(t.Context(), AdminScope()), &BulkUpdateAccountsInput{
		AccountIDs: []int64{1, 2, 3}, GroupIDs: &groups, SkipMixedChannelCheck: true,
	})
	require.NoError(t, err)
	require.Equal(t, 3, results.Success)
	require.Equal(t, 1, root.reads)
}
