package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestChannelMonitorProbeRepository_ConfiguredReservationLimits(t *testing.T) {
	for _, name := range []string{"global budget", "target budget", "interval", "lease and commit"} {
		t.Run(name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			repo := NewChannelMonitorProbeRepository(db)
			now := time.Date(2031, 1, 1, 12, 0, 0, 0, time.UTC)
			policy := service.ChannelMonitorProbePolicy{IntervalSeconds: 120, TimeoutSeconds: 45, TargetDailyLimit: 3, GlobalDailyLimit: 7, FailureThreshold: 2, QuotaStaleSeconds: 600}
			global, used := 0, 0
			if name == "global budget" {
				global = 7
			}
			if name == "target budget" {
				used = 3
			}
			var last any
			if name == "interval" {
				last = now.Add(-90 * time.Second)
			}
			mock.ExpectBegin()
			mock.ExpectExec("INSERT INTO channel_monitor_probe_budgets").WillReturnResult(sqlmock.NewResult(0, 2))
			mock.ExpectQuery("SELECT used.*target_id=0 FOR UPDATE").WillReturnRows(sqlmock.NewRows([]string{"used"}).AddRow(global))
			mock.ExpectQuery("SELECT result FROM channel_monitor_probe_runs").WillReturnError(sql.ErrNoRows)
			mock.ExpectQuery("SELECT .* FROM channel_monitor_probe_targets.*FOR UPDATE").WillReturnRows(sqlmock.NewRows([]string{"id", "group_id", "model", "protocol", "enabled", "version", "created_at", "updated_at", "last_probe_at"}).AddRow(1, 7, "gpt-test", "openai_chat", true, 1, now, now, last))
			mock.ExpectQuery("SELECT lease_until").WillReturnRows(sqlmock.NewRows([]string{"lease_until"}).AddRow(nil))
			if name != "interval" {
				mock.ExpectQuery("SELECT used.*target_id=\\$2 FOR UPDATE").WillReturnRows(sqlmock.NewRows([]string{"used"}).AddRow(used))
			}
			if name == "lease and commit" {
				mock.ExpectExec("UPDATE channel_monitor_probe_budgets SET used=used\\+1").WillReturnResult(sqlmock.NewResult(0, 2))
				mock.ExpectExec("INSERT INTO channel_monitor_probe_runs").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("UPDATE channel_monitor_probe_targets SET lease_until").WithArgs(int64(1), now.Add(55*time.Second), now).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			run, reserved, err := repo.Reserve(context.Background(), 1, "policy-test-operation", false, now, policy)
			switch name {
			case "lease and commit":
				require.NoError(t, err)
				require.True(t, reserved)
				require.NotNil(t, run)
			case "interval":
				require.ErrorIs(t, err, service.ErrChannelMonitorProbeBusy)
				require.False(t, reserved)
			default:
				require.ErrorIs(t, err, service.ErrChannelMonitorProbeBudget)
				require.False(t, reserved)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestChannelMonitorProbeRepository_BudgetReportsConfiguredLimits(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	policy := service.DefaultChannelMonitorObservationConfig().ProbePolicy()
	policy.TargetDailyLimit = 12
	policy.GlobalDailyLimit = 17
	mock.ExpectQuery("SELECT target_id,used").WillReturnRows(sqlmock.NewRows([]string{"target_id", "used"}).AddRow(0, 5).AddRow(3, 2))
	budget, err := NewChannelMonitorProbeRepository(db).Budget(context.Background(), time.Now(), policy)
	require.NoError(t, err)
	require.Equal(t, 17, budget.GlobalLimit)
	require.Equal(t, 12, budget.Targets[0].Limit)
	require.Equal(t, 5, budget.GlobalUsed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestChannelMonitorProbeRepository_ConfiguredQuotaStaleCutoff(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	now := time.Now().UTC()
	mock.ExpectQuery("SELECT ag.group_id").WithArgs(sqlmock.AnyArg(), now.Add(-30*time.Minute)).WillReturnRows(sqlmock.NewRows([]string{"group_id", "missing", "failed", "stale", "updated"}).AddRow(7, false, false, true, now.Add(-31*time.Minute)))
	states, err := NewChannelMonitorProbeRepository(db).QuotaSummaries(context.Background(), []int64{7}, now, 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, "stale", states[7].State)
	require.NoError(t, mock.ExpectationsWereMet())
}
