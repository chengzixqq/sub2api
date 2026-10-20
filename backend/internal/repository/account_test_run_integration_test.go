//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func newAccountTestRunForRepo(accountID int64) *service.AccountTestRun {
	return &service.AccountTestRun{
		RunID:     "tr_" + uuid.NewString()[:8] + fmt.Sprintf("%d", time.Now().UnixNano()),
		AccountID: accountID,
		Source:    service.AccountTestRunSourceAsync,
		Status:    service.AccountTestRunStatusRunning,
		ModelID:   "claude-sonnet-4-5",
	}
}

func mustCreateTestRunAccount(t *testing.T) int64 {
	t.Helper()
	account := mustCreateAccount(t, testEntClient(t), &service.Account{Name: "test-run-" + uuid.NewString()})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM account_test_runs WHERE account_id = $1", account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id = $1", account.ID)
	})
	return account.ID
}

func TestAccountTestRunRepo_OneRunningPerAccount(t *testing.T) {
	ctx := context.Background()
	repo := NewAccountTestRunRepository(integrationDB)
	accountID := mustCreateTestRunAccount(t)
	otherID := mustCreateTestRunAccount(t)
	createdBy := int64(42)

	first := newAccountTestRunForRepo(accountID)
	first.CreatedBy = &createdBy
	stored, created, err := repo.CreateRunning(ctx, first)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, first.RunID, stored.RunID)
	require.Equal(t, service.AccountTestRunStatusRunning, stored.Status)
	require.Nil(t, stored.LatencyMs)
	require.Nil(t, stored.FinishedAt)
	require.Nil(t, stored.Recovery)
	require.Equal(t, &createdBy, stored.CreatedBy)
	require.False(t, stored.StartedAt.IsZero())

	existing, created, err := repo.CreateRunning(ctx, newAccountTestRunForRepo(accountID))
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, first.RunID, existing.RunID)
	running, err := repo.GetRunningByAccount(ctx, accountID)
	require.NoError(t, err)
	require.Equal(t, first.RunID, running.RunID)

	_, created, err = repo.CreateRunning(ctx, newAccountTestRunForRepo(otherID))
	require.NoError(t, err)
	require.True(t, created, "another account is independent")

	latency, firstToken, code := int64(3420), int64(1210), 200
	finishedAt := time.Now().UTC().Truncate(time.Microsecond)
	stored.Status = service.AccountTestRunStatusSuccess
	stored.LatencyMs = &latency
	stored.FirstTokenMs = &firstToken
	stored.UpstreamStatusCode = &code
	stored.ResponseText = "ok"
	stored.Recovery = &service.AccountTestRunRecovery{ClearedError: true}
	stored.FinishedAt = &finishedAt
	require.NoError(t, repo.Finish(ctx, stored))

	got, err := repo.GetByRunID(ctx, first.RunID)
	require.NoError(t, err)
	require.Equal(t, service.AccountTestRunStatusSuccess, got.Status)
	require.Equal(t, &latency, got.LatencyMs)
	require.Equal(t, &firstToken, got.FirstTokenMs)
	require.Equal(t, &code, got.UpstreamStatusCode)
	require.Equal(t, "ok", got.ResponseText)
	require.Equal(t, &service.AccountTestRunRecovery{ClearedError: true}, got.Recovery)
	require.NotNil(t, got.FinishedAt)
	require.True(t, got.FinishedAt.Equal(finishedAt))

	// Finish only applies to running rows.
	got.Status = service.AccountTestRunStatusFailed
	require.NoError(t, repo.Finish(ctx, got))
	again, err := repo.GetByRunID(ctx, first.RunID)
	require.NoError(t, err)
	require.Equal(t, service.AccountTestRunStatusSuccess, again.Status)
	_, err = repo.GetRunningByAccount(ctx, accountID)
	require.ErrorIs(t, err, service.ErrAccountTestRunNotFound, "a finished run is not running")

	_, created, err = repo.CreateRunning(ctx, newAccountTestRunForRepo(accountID))
	require.NoError(t, err)
	require.True(t, created, "a new run can start after the previous one finished")

	_, err = repo.GetByRunID(ctx, "tr_does_not_exist")
	require.ErrorIs(t, err, service.ErrAccountTestRunNotFound)
	_, err = repo.GetLatestByAccount(ctx, -1)
	require.ErrorIs(t, err, service.ErrAccountTestRunNotFound)
}

func TestAccountTestRunRepo_InterruptStaleAndPrune(t *testing.T) {
	ctx := context.Background()
	repo := NewAccountTestRunRepository(integrationDB)
	accountID := mustCreateTestRunAccount(t)
	otherID := mustCreateTestRunAccount(t)

	stale, _, err := repo.CreateRunning(ctx, newAccountTestRunForRepo(accountID))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE account_test_runs SET started_at = NOW() - INTERVAL '200 seconds' WHERE run_id = $1", stale.RunID)
	require.NoError(t, err)
	fresh, _, err := repo.CreateRunning(ctx, newAccountTestRunForRepo(otherID))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE account_test_runs SET started_at = NOW() - INTERVAL '100 seconds' WHERE run_id = $1", fresh.RunID)
	require.NoError(t, err)

	n, err := repo.InterruptStale(ctx, accountID, service.AccountTestRunStaleAfter)
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	got, err := repo.GetByRunID(ctx, stale.RunID)
	require.NoError(t, err)
	require.Equal(t, service.AccountTestRunStatusInterrupted, got.Status)
	require.NotNil(t, got.FinishedAt)
	require.Equal(t, service.AccountTestRunInterruptedMessage, got.ErrorMessage)

	_, err = repo.InterruptStale(ctx, 0, service.AccountTestRunStaleAfter)
	require.NoError(t, err)
	stillRunning, err := repo.GetByRunID(ctx, fresh.RunID)
	require.NoError(t, err)
	require.Equal(t, service.AccountTestRunStatusRunning, stillRunning.Status, "a run inside the window is live")

	base := time.Now().Add(-100 * time.Second).UTC().Truncate(time.Microsecond)
	var newest string
	for i := 0; i < 25; i++ {
		finishedAt := base.Add(time.Duration(i)*time.Second + 500*time.Millisecond)
		run := newAccountTestRunForRepo(accountID)
		run.Source = service.AccountTestRunSourceManual
		run.Status = service.AccountTestRunStatusFailed
		run.ErrorMessage = "API returned 401: bad"
		run.StartedAt = base.Add(time.Duration(i) * time.Second)
		run.FinishedAt = &finishedAt
		require.NoError(t, repo.InsertFinished(ctx, run))
		newest = run.RunID
	}
	running, _, err := repo.CreateRunning(ctx, newAccountTestRunForRepo(accountID))
	require.NoError(t, err)

	require.NoError(t, repo.Prune(ctx, accountID, service.AccountTestRunKeepPerAccount))

	var finished, runningCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FILTER (WHERE status <> 'running'), COUNT(*) FILTER (WHERE status = 'running') FROM account_test_runs WHERE account_id = $1",
		accountID).Scan(&finished, &runningCount))
	require.Equal(t, service.AccountTestRunKeepPerAccount, finished)
	require.Equal(t, 1, runningCount)

	latest, err := repo.GetLatestByAccount(ctx, accountID)
	require.NoError(t, err)
	require.Equal(t, running.RunID, latest.RunID, "the just started run is the newest")
	_, err = integrationDB.ExecContext(ctx, "DELETE FROM account_test_runs WHERE run_id = $1", running.RunID)
	require.NoError(t, err)
	latest, err = repo.GetLatestByAccount(ctx, accountID)
	require.NoError(t, err)
	require.Equal(t, newest, latest.RunID)
	require.Equal(t, service.AccountTestRunSourceManual, latest.Source)
}
