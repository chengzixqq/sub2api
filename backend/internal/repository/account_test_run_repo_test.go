package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

var accountTestRunMockColumns = []string{
	"id", "run_id", "account_id", "source", "status", "model_id", "latency_ms", "first_token_ms",
	"upstream_status_code", "error_message", "response_text", "recovery", "started_at", "finished_at", "created_by",
}

func accountTestRunMockRow(id int64, runID string, accountID int64) *sqlmock.Rows {
	return sqlmock.NewRows(accountTestRunMockColumns).AddRow(
		id, runID, accountID, service.AccountTestRunSourceAsync, service.AccountTestRunStatusRunning, "m",
		nil, nil, nil, "", "", nil, time.Now(), nil, nil)
}

func TestAccountTestRunRepoCreateRunning_RetriesWhenBlockingRunFinishesBeforeRead(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewAccountTestRunRepository(db)
	run := &service.AccountTestRun{RunID: "tr_new", AccountID: 12, Source: service.AccountTestRunSourceAsync, ModelID: "m"}

	// The insert conflicts with a running row, which finishes before it is read.
	mock.ExpectQuery("INSERT INTO account_test_runs").WillReturnRows(sqlmock.NewRows(accountTestRunMockColumns))
	mock.ExpectQuery("FROM account_test_runs WHERE account_id = \\$1 AND status = 'running'").
		WithArgs(int64(12)).WillReturnRows(sqlmock.NewRows(accountTestRunMockColumns))
	mock.ExpectQuery("INSERT INTO account_test_runs").WillReturnRows(accountTestRunMockRow(7, "tr_new", 12))

	stored, created, err := repo.CreateRunning(context.Background(), run)

	require.NoError(t, err)
	require.True(t, created, "the slot freed by the finished run is taken by a second insert")
	require.Equal(t, "tr_new", stored.RunID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountTestRunRepoCreateRunning_ReturnsExistingRunningRow(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewAccountTestRunRepository(db)

	mock.ExpectQuery("INSERT INTO account_test_runs").WillReturnRows(sqlmock.NewRows(accountTestRunMockColumns))
	mock.ExpectQuery("FROM account_test_runs WHERE account_id = \\$1 AND status = 'running'").
		WithArgs(int64(12)).WillReturnRows(accountTestRunMockRow(3, "tr_old", 12))

	existing, created, err := repo.CreateRunning(context.Background(), &service.AccountTestRun{RunID: "tr_new", AccountID: 12})

	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, "tr_old", existing.RunID)
	require.NoError(t, mock.ExpectationsWereMet())
}
