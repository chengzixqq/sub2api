//go:build unit

package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func expectGroupPublisherLock(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SELECT pg_try_advisory_lock`).WithArgs(groupUsageRollupPublisherLock).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
}
func expectGroupPublisherUnlock(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SELECT pg_advisory_unlock`).WithArgs(groupUsageRollupPublisherLock).WillReturnRows(sqlmock.NewRows([]string{"unlocked"}).AddRow(true))
}
func expectGroupPrepare(mock sqlmock.Sqlmock, closed, zone string, retained time.Time) {
	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT closed_before::text, retained_from.*FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"closed_before", "retained_from", "timezone_name"}).AddRow(closed, retained, zone))
}
func expectGroupNoop(mock sqlmock.Sqlmock, today time.Time) {
	expectGroupPublisherLock(mock)
	expectGroupPrepare(mock, service.GroupUsageDate(today), service.GroupUsageTimezoneName(), time.Unix(0, 0).UTC())
	mock.ExpectCommit()
	expectGroupPublisherUnlock(mock)
}
func expectGroupOneDay(mock sqlmock.Sqlmock, closed, zone string, today, earliest time.Time) {
	retained := time.Unix(0, 0).UTC()
	expectGroupPrepare(mock, closed, zone, retained)
	mock.ExpectQuery(`UPDATE usage_group_rollup_state.*RETURNING source_revision, scan_before`).WithArgs(today.UTC()).
		WillReturnRows(sqlmock.NewRows([]string{"source_revision", "scan_before"}).AddRow(11, today))
	mock.ExpectCommit() // The scan must start AFTER releasing the metadata lock.
	mock.ExpectQuery(`SELECT MIN\(created_at\) FROM usage_logs`).WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(earliest))
	mock.ExpectQuery(`SELECT group_id, COALESCE\(SUM\(actual_cost\), 0\)::text`).WithArgs(earliest.UTC(), today.UTC()).
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "actual_cost"}).AddRow(10, "1.2345678901"))
	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT source_revision, closed_before::text.*FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"revision", "closed_before", "retained_from", "timezone_name", "scan_before"}).AddRow(11, closed, retained, zone, today))
	mock.ExpectExec(`DELETE FROM usage_group_daily_rollups`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO usage_group_daily_rollups`).WithArgs(service.GroupUsageDate(earliest), sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE usage_group_rollup_state SET closed_before`).WithArgs(service.GroupUsageDate(today), earliest.UTC(), service.GroupUsageTimezoneName()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}
func TestDashboardAggregationRepositorySyncGroupUsageRollupsNoopsAtCurrentDate(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	today := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
	expectGroupNoop(mock, today)
	require.NoError(t, newDashboardAggregationRepositoryWithSQL(db).SyncGroupUsageRollups(context.Background(), today))
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestDashboardAggregationRepositorySyncGroupUsageRollupsRebuildsWhenTimezoneChanges(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "America/New_York")
	db, mock := newSQLMock(t)
	today := time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC)
	yesterday := time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC) // 23-hour DST day.
	expectGroupPublisherLock(mock)
	expectGroupOneDay(mock, "2026-03-09", "Asia/Shanghai", today, yesterday)
	expectGroupPublisherUnlock(mock)
	require.NoError(t, newDashboardAggregationRepositoryWithSQL(db).SyncGroupUsageRollups(context.Background(), today))
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestDashboardAggregationRepositorySyncGroupUsageRollupsPublishesWatermarkLast(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	today := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
	yesterday := today.Add(-24 * time.Hour)
	expectGroupPublisherLock(mock)
	expectGroupOneDay(mock, "2026-08-13", "Asia/Shanghai", today, yesterday)
	expectGroupPublisherUnlock(mock)
	require.NoError(t, newDashboardAggregationRepositoryWithSQL(db).SyncGroupUsageRollups(context.Background(), today))
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestDashboardAggregationRepositorySyncGroupUsageRollupsRejectsFutureWatermark(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	today := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
	expectGroupPublisherLock(mock)
	expectGroupPrepare(mock, "2026-08-15", "Asia/Shanghai", time.Unix(0, 0).UTC())
	mock.ExpectRollback()
	expectGroupPublisherUnlock(mock)
	err := newDashboardAggregationRepositoryWithSQL(db).SyncGroupUsageRollups(context.Background(), today)
	require.ErrorContains(t, err, "未来")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationRepositoryRecomputeRangeFailureLeavesGroupRollupsUntouched(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	start := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL sub2api.dashboard_verified_publish`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT raw_retained_from.*FOR SHARE`).WillReturnRows(sqlmock.NewRows([]string{"retained_from"}).AddRow(time.Unix(0, 0).UTC()))
	mock.ExpectQuery(`SELECT .*CURRENT_TIMESTAMP`).WillReturnRows(sqlmock.NewRows([]string{"finalized"}).AddRow(true))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT source_verified FROM usage_dashboard_hourly`).WillReturnRows(sqlmock.NewRows([]string{"source_verified"}).AddRow(true))
	mock.ExpectExec(`DELETE FROM usage_dashboard_hourly`).
		WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()

	err := repo.RecomputeRange(context.Background(), start, end)
	require.ErrorIs(t, err, sql.ErrConnDone)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationRepositoryRecomputeRangeCommitsInvalidationBeforeGroupSync(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	start := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	fixedNow := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	repo.clock = func() time.Time { return fixedNow }
	todayStart := service.GroupUsageTodayStart(fixedNow)

	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL sub2api.dashboard_verified_publish`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT raw_retained_from.*FOR SHARE`).WillReturnRows(sqlmock.NewRows([]string{"retained_from"}).AddRow(time.Unix(0, 0).UTC()))
	mock.ExpectQuery(`SELECT .*CURRENT_TIMESTAMP`).WillReturnRows(sqlmock.NewRows([]string{"finalized"}).AddRow(true))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT source_verified FROM usage_dashboard_hourly`).WillReturnRows(sqlmock.NewRows([]string{"source_verified"}).AddRow(true))
	for _, query := range []string{
		`DELETE FROM usage_dashboard_hourly WHERE`,
		`DELETE FROM usage_dashboard_hourly_users WHERE`,
		`INSERT INTO usage_dashboard_hourly_users`,
		`INSERT INTO usage_dashboard_hourly`,
		`INSERT INTO usage_dashboard_hourly`,
	} {
		mock.ExpectExec(query).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 1))
	for _, query := range []string{
		`DELETE FROM usage_dashboard_daily_users WHERE`,
		`INSERT INTO usage_dashboard_daily_users`,
		`INSERT INTO usage_dashboard_daily`,
	} {
		mock.ExpectExec(query).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT id FROM usage_group_rollup_state.*FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectExec(`UPDATE usage_group_rollup_state`).WithArgs(start).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	expectGroupNoop(mock, todayStart)

	require.NoError(t, repo.RecomputeRange(context.Background(), start, end))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationRepositoryCleanupUsageLogsNonPartitionedInvalidatesEachBatchAndSyncs(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	earliestDeletedAt := time.Date(2026, 5, 3, 2, 0, 0, 0, time.UTC)
	fixedNow := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	repo.clock = func() time.Time { return fixedNow }
	todayStart := service.GroupUsageTodayStart(fixedNow)

	mock.ExpectQuery(`SELECT EXISTS`).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`SET LOCAL sub2api.dashboard_preserve_aggregates`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT id FROM usage_group_rollup_state.*FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(`SELECT raw_retained_from.*FOR UPDATE NOWAIT`).WillReturnRows(sqlmock.NewRows([]string{"retained_from"}).AddRow(time.Unix(0, 0).UTC()))
	mock.ExpectQuery(`(?s)SELECT tableoid::bigint, ctid::text, created_at.*FOR UPDATE NOWAIT`).
		WithArgs(cutoff, usageLogsCleanupBatchSize).
		WillReturnRows(sqlmock.NewRows([]string{"tableoid", "ctid", "created_at"}).
			AddRow(123, "(0,1)", earliestDeletedAt.Add(time.Hour)).
			AddRow(123, "(0,2)", earliestDeletedAt))
	expectDashboardRetentionSafety(mock)
	mock.ExpectExec(`UPDATE usage_dashboard_source_state`).WithArgs(earliestDeletedAt.Add(time.Hour + time.Microsecond)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)DELETE FROM usage_logs WHERE \(tableoid,ctid\) IN.*unnest`).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(`UPDATE usage_group_rollup_state`).
		WithArgs(earliestDeletedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	expectGroupNoop(mock, todayStart)

	require.NoError(t, repo.CleanupUsageLogs(context.Background(), cutoff))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationRepositoryCleanupUsageLogsPartitionedSortsAndInvalidatesEachDropBeforeSync(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	aprilStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	juneStart := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	fixedNow := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	repo.clock = func() time.Time { return fixedNow }
	todayStart := service.GroupUsageTodayStart(fixedNow)

	mock.ExpectQuery(`SELECT EXISTS`).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`SELECT c.relname`).
		WillReturnRows(sqlmock.NewRows([]string{"relname"}).
			AddRow("usage_logs_202606").
			AddRow("usage_logs_invalid").
			AddRow("usage_logs_202604").
			AddRow("usage_logs_202607"))

	for _, partition := range []struct {
		name  string
		start time.Time
	}{
		{name: "usage_logs_202604", start: aprilStart},
		{name: "usage_logs_202606", start: juneStart},
	} {
		mock.ExpectBegin()
		mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`SELECT id FROM usage_group_rollup_state.*FOR UPDATE`).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
		mock.ExpectQuery(`SELECT raw_retained_from.*FOR UPDATE NOWAIT`).WillReturnRows(sqlmock.NewRows([]string{"retained_from"}).AddRow(time.Unix(0, 0).UTC()))
		mock.ExpectExec(`LOCK TABLE ONLY usage_logs, "` + partition.name + `" IN ACCESS EXCLUSIVE MODE NOWAIT`).WillReturnResult(sqlmock.NewResult(0, 0))
		expectDashboardRetentionSafety(mock)
		mock.ExpectExec(`UPDATE usage_dashboard_source_state`).WithArgs(partition.start.AddDate(0, 1, 0)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`UPDATE usage_group_rollup_state`).
			WithArgs(partition.start).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`DROP TABLE IF EXISTS "` + partition.name + `"`).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectCommit()
	}
	// The boundary partition is pruned by exact timestamp. tableoid is required
	// because ctid alone can also identify a retained row in another partition.
	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`SET LOCAL sub2api.dashboard_preserve_aggregates`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT id FROM usage_group_rollup_state.*FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(`SELECT raw_retained_from.*FOR UPDATE NOWAIT`).WillReturnRows(sqlmock.NewRows([]string{"retained_from"}).AddRow(time.Unix(0, 0).UTC()))
	mock.ExpectQuery(`(?s)SELECT tableoid::bigint, ctid::text, created_at.*FOR UPDATE NOWAIT`).
		WithArgs(cutoff, usageLogsCleanupBatchSize).
		WillReturnRows(sqlmock.NewRows([]string{"tableoid", "ctid", "created_at"}).AddRow(123, "(0,1)", cutoff.Add(-time.Hour)))
	expectDashboardRetentionSafety(mock)
	mock.ExpectExec(`UPDATE usage_dashboard_source_state`).WithArgs(cutoff.Add(-time.Hour + time.Microsecond)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)DELETE FROM usage_logs WHERE \(tableoid,ctid\) IN.*unnest`).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE usage_group_rollup_state`).
		WithArgs(cutoff.Add(-time.Hour)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	expectGroupNoop(mock, todayStart)

	require.NoError(t, repo.CleanupUsageLogs(context.Background(), cutoff))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationRepositoryCleanupUsageLogsNonPartitionedFailureRollsBackWithoutSync(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	deletedAt := time.Date(2026, 5, 3, 2, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`SELECT EXISTS`).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`SET LOCAL sub2api.dashboard_preserve_aggregates`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT id FROM usage_group_rollup_state.*FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(`SELECT raw_retained_from.*FOR UPDATE NOWAIT`).WillReturnRows(sqlmock.NewRows([]string{"retained_from"}).AddRow(time.Unix(0, 0).UTC()))
	mock.ExpectQuery(`(?s)SELECT tableoid::bigint, ctid::text, created_at.*FOR UPDATE NOWAIT`).
		WithArgs(cutoff, usageLogsCleanupBatchSize).
		WillReturnRows(sqlmock.NewRows([]string{"tableoid", "ctid", "created_at"}).AddRow(123, "(0,1)", deletedAt))
	expectDashboardRetentionSafety(mock)
	mock.ExpectExec(`UPDATE usage_dashboard_source_state`).WithArgs(deletedAt.Add(time.Microsecond)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)DELETE FROM usage_logs WHERE \(tableoid,ctid\) IN.*unnest`).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE usage_group_rollup_state`).
		WithArgs(deletedAt).
		WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()

	err := repo.CleanupUsageLogs(context.Background(), cutoff)
	require.ErrorIs(t, err, sql.ErrConnDone)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationRepositoryCleanupUsageLogsPartitionFailureRollsBackAndStops(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	aprilStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	dropErr := errors.New("drop partition failed")

	mock.ExpectQuery(`SELECT EXISTS`).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`SELECT c.relname`).
		WillReturnRows(sqlmock.NewRows([]string{"relname"}).
			AddRow("usage_logs_202606").
			AddRow("usage_logs_202604"))
	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT id FROM usage_group_rollup_state.*FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(`SELECT raw_retained_from.*FOR UPDATE NOWAIT`).WillReturnRows(sqlmock.NewRows([]string{"retained_from"}).AddRow(time.Unix(0, 0).UTC()))
	mock.ExpectExec(`LOCK TABLE ONLY usage_logs, "usage_logs_202604" IN ACCESS EXCLUSIVE MODE NOWAIT`).WillReturnResult(sqlmock.NewResult(0, 0))
	expectDashboardRetentionSafety(mock)
	mock.ExpectExec(`UPDATE usage_dashboard_source_state`).WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE usage_group_rollup_state`).
		WithArgs(aprilStart).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DROP TABLE IF EXISTS "usage_logs_202604"`).
		WillReturnError(dropErr)
	mock.ExpectRollback()

	err := repo.CleanupUsageLogs(context.Background(), cutoff)
	require.ErrorIs(t, err, dropErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func expectDashboardRetentionSafety(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`(?s)SELECT bucket_start FROM usage_dashboard_hourly.*ORDER BY bucket_start LIMIT`).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), dashboardRetentionHourLockLimit+1).WillReturnRows(sqlmock.NewRows([]string{"bucket_start"}))
	mock.ExpectQuery(`(?s)SELECT bucket_start FROM usage_dashboard_hourly.*WHERE source_verified`).WillReturnRows(sqlmock.NewRows([]string{"bucket_start"}))
	mock.ExpectQuery(`SELECT bool_and\(pg_try_advisory_xact_lock`).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectQuery(`(?s)SELECT EXISTS.*NOT source_finalized`).WillReturnRows(sqlmock.NewRows([]string{"unsafe"}).AddRow(false))
}

func setGroupUsageRollupTestTimezone(t *testing.T) {
	t.Helper()
	useGroupUsageRepositoryTestTimezone(t, "Asia/Shanghai")
}

func TestDashboardAggregationRepositorySyncGroupUsageRollupsDiscardsUncertainLockSession(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery(`SELECT pg_try_advisory_lock`).WithArgs(groupUsageRollupPublisherLock).WillReturnError(context.Canceled)
	mock.ExpectClose()
	err = newDashboardAggregationRepositoryWithSQL(db).SyncGroupUsageRollups(context.Background(), time.Now())
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, db.Stats().OpenConnections, "uncertain session lock ownership must not return to the pool")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationRepositorySyncGroupUsageRollupsDiscardsFailedUnlockSession(t *testing.T) {
	for _, responseError := range []bool{false, true} {
		t.Run(map[bool]string{false: "false_result", true: "response_error"}[responseError], func(t *testing.T) {
			setGroupUsageRollupTestTimezone(t)
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			today := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
			expectGroupPublisherLock(mock)
			expectGroupPrepare(mock, service.GroupUsageDate(today), service.GroupUsageTimezoneName(), time.Unix(0, 0).UTC())
			mock.ExpectCommit()
			q := mock.ExpectQuery(`SELECT pg_advisory_unlock`).WithArgs(groupUsageRollupPublisherLock)
			if responseError {
				q.WillReturnError(context.DeadlineExceeded)
			} else {
				q.WillReturnRows(sqlmock.NewRows([]string{"unlocked"}).AddRow(false))
			}
			mock.ExpectClose()
			require.NoError(t, newDashboardAggregationRepositoryWithSQL(db).SyncGroupUsageRollups(context.Background(), today))
			require.Zero(t, db.Stats().OpenConnections)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func expectUsageLogsBacklogProbe(mock sqlmock.Sqlmock, cutoff time.Time, remaining bool) {
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM usage_logs WHERE created_at <`).WithArgs(cutoff).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(remaining))
}

// expectUsageLogsRetentionBatch scripts one retention batch that selects a
// single victim row and reports `affected` deleted rows.
func expectUsageLogsRetentionBatch(mock sqlmock.Sqlmock, cutoff, at time.Time, affected int64) {
	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`SET LOCAL sub2api.dashboard_preserve_aggregates`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT id FROM usage_group_rollup_state.*FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(`SELECT raw_retained_from.*FOR UPDATE NOWAIT`).WillReturnRows(sqlmock.NewRows([]string{"retained_from"}).AddRow(time.Unix(0, 0).UTC()))
	mock.ExpectQuery(`(?s)SELECT tableoid::bigint, ctid::text, created_at.*FOR UPDATE NOWAIT`).
		WithArgs(cutoff, usageLogsCleanupBatchSize).
		WillReturnRows(sqlmock.NewRows([]string{"tableoid", "ctid", "created_at"}).AddRow(123, "(0,1)", at))
	expectDashboardRetentionSafety(mock)
	mock.ExpectExec(`UPDATE usage_dashboard_source_state`).WithArgs(at.Add(time.Microsecond)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)DELETE FROM usage_logs WHERE \(tableoid,ctid\) IN.*unnest`).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, affected))
	mock.ExpectExec(`UPDATE usage_group_rollup_state`).WithArgs(at).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}

// expectUsageLogsRetentionBatchDeferred scripts a batch that gives up on a
// NOWAIT conflict over its victim rows without deleting anything.
func expectUsageLogsRetentionBatchDeferred(mock sqlmock.Sqlmock, cutoff time.Time) {
	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`SET LOCAL sub2api.dashboard_preserve_aggregates`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT id FROM usage_group_rollup_state.*FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(`SELECT raw_retained_from.*FOR UPDATE NOWAIT`).WillReturnRows(sqlmock.NewRows([]string{"retained_from"}).AddRow(time.Unix(0, 0).UTC()))
	mock.ExpectQuery(`(?s)SELECT tableoid::bigint, ctid::text, created_at.*FOR UPDATE NOWAIT`).
		WithArgs(cutoff, usageLogsCleanupBatchSize).
		WillReturnError(&pq.Error{Code: "55P03"})
	mock.ExpectRollback()
}

// advancingRetentionClock moves past half the run budget on every read, so
// the second full batch of a run ends after the budget.
func advancingRetentionClock() func() time.Time {
	current := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	return func() time.Time {
		now := current
		current = current.Add(usageLogsCleanupRunBudget/2 + time.Second)
		return now
	}
}

// One retention run must not keep taking the ingest gate until a whole backlog
// is gone. It stops at its time budget and reports the remainder as pending,
// without the group rollup rebuild that the next run would invalidate again.
func TestDashboardAggregationRepositoryCleanupUsageLogsStopsAtRunBudgetAndReportsPending(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	deletedAt := time.Date(2026, 5, 3, 2, 0, 0, 0, time.UTC)
	repo.clock = advancingRetentionClock()

	mock.ExpectQuery(`SELECT EXISTS`).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	for batch := 0; batch < 2; batch++ {
		expectUsageLogsRetentionBatch(mock, cutoff, deletedAt.Add(time.Duration(batch)*time.Hour), usageLogsCleanupBatchSize)
	}
	expectUsageLogsBacklogProbe(mock, cutoff, true)

	err := repo.CleanupUsageLogs(context.Background(), cutoff)

	require.ErrorIs(t, err, service.ErrUsageLogsRetentionPending)
	require.NoError(t, mock.ExpectationsWereMet(), "no third batch and no group rollup rebuild after the budget")
}

// The last full batch can end exactly at the budget with nothing left behind.
// That run is complete: it rebuilds the group rollups and reports success.
func TestDashboardAggregationRepositoryCleanupUsageLogsBudgetWithoutBacklogCompletes(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	deletedAt := time.Date(2026, 5, 3, 2, 0, 0, 0, time.UTC)
	repo.clock = advancingRetentionClock()

	mock.ExpectQuery(`SELECT EXISTS`).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	for batch := 0; batch < 2; batch++ {
		expectUsageLogsRetentionBatch(mock, cutoff, deletedAt.Add(time.Duration(batch)*time.Hour), usageLogsCleanupBatchSize)
	}
	expectUsageLogsBacklogProbe(mock, cutoff, false)
	// Every clock read of this run stays within the same configured day.
	expectGroupNoop(mock, service.GroupUsageTodayStart(time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)))

	require.NoError(t, repo.CleanupUsageLogs(context.Background(), cutoff))
	require.NoError(t, mock.ExpectationsWereMet())
}

// A batch abandoned by a lock conflict deleted nothing. Reporting that as a
// finished run would make the service wait the whole retention interval while
// the backlog is still there, so it must surface as pending. Nothing was
// invalidated in this run, so the group rollup rebuild still proceeds.
func TestDashboardAggregationRepositoryCleanupUsageLogsDeferredBatchWithBacklogRebuildsAndReportsPending(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	fixedNow := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	repo.clock = func() time.Time { return fixedNow }

	mock.ExpectQuery(`SELECT EXISTS`).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	expectUsageLogsRetentionBatchDeferred(mock, cutoff)
	expectUsageLogsBacklogProbe(mock, cutoff, true)
	expectGroupNoop(mock, service.GroupUsageTodayStart(fixedNow))

	err := repo.CleanupUsageLogs(context.Background(), cutoff)

	require.ErrorIs(t, err, service.ErrUsageLogsRetentionPending)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Rows were deleted before a later batch was deferred: the next run deletes on
// and moves the group watermark back again, so the rebuild is skipped.
func TestDashboardAggregationRepositoryCleanupUsageLogsDeferredAfterProgressSkipsRebuild(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	fixedNow := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	repo.clock = func() time.Time { return fixedNow }

	mock.ExpectQuery(`SELECT EXISTS`).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	expectUsageLogsRetentionBatch(mock, cutoff, time.Date(2026, 5, 3, 2, 0, 0, 0, time.UTC), usageLogsCleanupBatchSize)
	expectUsageLogsRetentionBatchDeferred(mock, cutoff)
	expectUsageLogsBacklogProbe(mock, cutoff, true)

	err := repo.CleanupUsageLogs(context.Background(), cutoff)

	require.ErrorIs(t, err, service.ErrUsageLogsRetentionPending)
	require.NoError(t, mock.ExpectationsWereMet(), "no group rollup rebuild between two deleting runs")
}

// A conflict with nothing left below the cutoff is an ordinary finished run.
func TestDashboardAggregationRepositoryCleanupUsageLogsDeferredBatchWithoutBacklogCompletes(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	fixedNow := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	repo.clock = func() time.Time { return fixedNow }

	mock.ExpectQuery(`SELECT EXISTS`).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	expectUsageLogsRetentionBatchDeferred(mock, cutoff)
	expectUsageLogsBacklogProbe(mock, cutoff, false)
	expectGroupNoop(mock, service.GroupUsageTodayStart(fixedNow))

	require.NoError(t, repo.CleanupUsageLogs(context.Background(), cutoff))
	require.NoError(t, mock.ExpectationsWereMet())
}
