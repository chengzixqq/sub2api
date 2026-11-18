//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// A registered scan protects all history before today. Production keeps the
// state in this shape permanently, so every retained day is "protected".
func protectGroupUsageHistory(t *testing.T, ctx context.Context, conn *sql.Conn, closedBefore, scanBefore time.Time) int64 {
	t.Helper()
	_, err := conn.ExecContext(ctx, `UPDATE usage_group_rollup_state SET closed_before=$1::date,scan_before=$2 WHERE id=1`,
		closedBefore.Format("2006-01-02"), scanBefore)
	require.NoError(t, err)
	return groupUsageSourceRevision(t, ctx, conn)
}

func groupUsageSourceRevision(t *testing.T, ctx context.Context, conn *sql.Conn) int64 {
	t.Helper()
	var revision int64
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT source_revision FROM usage_group_rollup_state WHERE id=1`).Scan(&revision))
	return revision
}

func groupUsageClosedBefore(t *testing.T, ctx context.Context, conn *sql.Conn) string {
	t.Helper()
	var closed string
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT closed_before::text FROM usage_group_rollup_state WHERE id=1`).Scan(&closed))
	return closed
}

func seedGroupUsageHistory(t *testing.T, ctx context.Context, conn *sql.Conn, firstID, count int, from time.Time) {
	t.Helper()
	_, err := conn.ExecContext(ctx, `INSERT INTO usage_logs(id,user_id,group_id,actual_cost,created_at)
		SELECT $1::int + n,1,10,1,$3::timestamptz + n * INTERVAL '1 second' FROM generate_series(0,$2::int - 1) n`, firstID, count, from)
	require.NoError(t, err)
}

// The publisher only compares source_revision for equality, and it cannot run
// while a writer holds the state row. One bump per transaction is therefore
// enough; one per row made a bulk delete rewrite the same row thousands of times.
func TestGroupUsageRollupSingleBump_BulkHistoricalDeleteBumpsOnce(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	f := newOptimisticGroupRollupFixture(t, ctx, false)
	conn := f.conn(t, ctx)
	day := f.today.AddDate(0, 0, -3)
	seedGroupUsageHistory(t, ctx, conn, 1, 500, day)
	before := protectGroupUsageHistory(t, ctx, conn, f.today, f.today)

	_, err := conn.ExecContext(ctx, `DELETE FROM usage_logs`)
	require.NoError(t, err)

	require.EqualValues(t, 1, groupUsageSourceRevision(t, ctx, conn)-before, "one transaction needs one revision bump, not one per deleted row")
	require.Equal(t, day.Format("2006-01-02"), groupUsageClosedBefore(t, ctx, conn), "the published day that lost rows must still be invalidated")
}

func TestGroupUsageRollupSingleBump_BulkHistoricalUpdateBumpsOnce(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	f := newOptimisticGroupRollupFixture(t, ctx, false)
	conn := f.conn(t, ctx)
	day := f.today.AddDate(0, 0, -3)
	seedGroupUsageHistory(t, ctx, conn, 1, 200, day)
	// Nothing is published yet: the watermark cannot move, only the revision can.
	before := protectGroupUsageHistory(t, ctx, conn, day, f.today)

	_, err := conn.ExecContext(ctx, `UPDATE usage_logs SET actual_cost = actual_cost + 1`)
	require.NoError(t, err)

	require.EqualValues(t, 1, groupUsageSourceRevision(t, ctx, conn)-before)
	require.Equal(t, day.Format("2006-01-02"), groupUsageClosedBefore(t, ctx, conn))
}

// After the first bump the watermark must still follow an older row.
func TestGroupUsageRollupSingleBump_LaterOlderRowStillLowersWatermark(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	f := newOptimisticGroupRollupFixture(t, ctx, false)
	conn := f.conn(t, ctx)
	older, newer := f.today.AddDate(0, 0, -5), f.today.AddDate(0, 0, -2)
	seedGroupUsageHistory(t, ctx, conn, 1, 20, older)
	seedGroupUsageHistory(t, ctx, conn, 101, 20, newer)
	before := protectGroupUsageHistory(t, ctx, conn, f.today, f.today)

	tx, err := conn.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `DELETE FROM usage_logs WHERE id > 100`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `DELETE FROM usage_logs WHERE id <= 100`)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	require.Equal(t, older.Format("2006-01-02"), groupUsageClosedBefore(t, ctx, conn), "an older row deleted after the first bump must still lower the watermark")
	require.EqualValues(t, 2, groupUsageSourceRevision(t, ctx, conn)-before, "only watermark moves may add further updates")
}

// The "already bumped" marker must share the fate of the bump itself. If a
// rolled-back savepoint left the marker behind, the surviving delete would
// commit without any revision change and a stale scan could be published.
func TestGroupUsageRollupSingleBump_SavepointRollbackRestoresBumpObligation(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	f := newOptimisticGroupRollupFixture(t, ctx, false)
	conn := f.conn(t, ctx)
	day := f.today.AddDate(0, 0, -3)
	seedGroupUsageHistory(t, ctx, conn, 1, 2, day)
	// The watermark is already at this day, so only the revision branch applies.
	before := protectGroupUsageHistory(t, ctx, conn, day, f.today)

	tx, err := conn.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `SAVEPOINT first_delete`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `DELETE FROM usage_logs WHERE id = 1`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT first_delete`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `DELETE FROM usage_logs WHERE id = 2`)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	var remaining int
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs`).Scan(&remaining))
	require.Equal(t, 1, remaining)
	require.EqualValues(t, 1, groupUsageSourceRevision(t, ctx, conn)-before, "the committed delete must carry its own revision bump")
}

// The marker is transaction-local: the next transaction on the same pooled
// connection owes its own bump.
func TestGroupUsageRollupSingleBump_EveryTransactionBumps(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	f := newOptimisticGroupRollupFixture(t, ctx, false)
	conn := f.conn(t, ctx)
	day := f.today.AddDate(0, 0, -3)
	seedGroupUsageHistory(t, ctx, conn, 1, 3, day)
	before := protectGroupUsageHistory(t, ctx, conn, day, f.today)

	for id := 1; id <= 3; id++ {
		_, err := conn.ExecContext(ctx, `DELETE FROM usage_logs WHERE id = $1`, id)
		require.NoError(t, err)
	}

	require.EqualValues(t, 3, groupUsageSourceRevision(t, ctx, conn)-before)
}

// The optimistic protocol end to end: a bulk change that bumps only once must
// still reject a scan that started before it.
func TestGroupUsageRollupSingleBump_BulkDeleteRejectsStaleScan(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	f := newOptimisticGroupRollupFixture(t, ctx, false)
	scanner, writer := f.conn(t, ctx), f.conn(t, ctx)
	day := f.today.AddDate(0, 0, -1)
	seedGroupUsageHistory(t, ctx, scanner, 1, 300, day)

	plan, ready, err := prepareGroupUsageRollup(ctx, scanner, f.today)
	require.NoError(t, err)
	require.True(t, ready)
	costs, err := scanGroupUsageRollup(ctx, scanner, &plan)
	require.NoError(t, err)
	_, err = writer.ExecContext(ctx, `DELETE FROM usage_logs WHERE id > 1`)
	require.NoError(t, err)

	published, err := publishGroupUsageRollup(ctx, scanner, plan, costs)
	require.NoError(t, err)
	require.False(t, published, "a committed bulk delete must reject the stale scan")
	var buckets int64
	require.NoError(t, writer.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_group_daily_rollups`).Scan(&buckets))
	require.Zero(t, buckets)
}

// Automatic retention as production runs it: grouped rows, a registered scan,
// certified hourly summaries, one full batch. Before the fix the DELETE spent
// its whole statement budget rewriting the state row and rolled back.
func TestGroupUsageRollupSingleBump_RetentionBatchCommitsWithBoundedStateWrites(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	db := hourlyInvalidationDB(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	hour := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	_, err := db.ExecContext(ctx, `INSERT INTO groups(id) VALUES(10)`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO usage_logs(id,group_id,created_at)
		SELECT n,10,$1::timestamptz + INTERVAL '1 minute' FROM generate_series(1,$2::int) n`, hour, usageLogsCleanupBatchSize+1)
	require.NoError(t, err)
	require.NoError(t, repo.AggregateRange(ctx, hour, hour.Add(time.Hour)))
	require.NoError(t, repo.UpdateAggregationWatermark(ctx, time.Now().UTC()))
	_, err = db.ExecContext(ctx, `UPDATE usage_group_rollup_state SET closed_before=$1::date,scan_before=$2,timezone_name='UTC' WHERE id=1`,
		today.Format("2006-01-02"), today)
	require.NoError(t, err)
	var before int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT source_revision FROM usage_group_rollup_state WHERE id=1`).Scan(&before))

	affected, err := cleanupUsageLogsBatchWithRollupInvalidation(ctx, db, hour.Add(4*time.Hour))
	require.NoError(t, err, "a full batch of protected history must fit the group-gate budget")
	require.EqualValues(t, usageLogsCleanupBatchSize, affected)

	var after int64
	var closed string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT source_revision,closed_before::text FROM usage_group_rollup_state WHERE id=1`).Scan(&after, &closed))
	require.GreaterOrEqual(t, after-before, int64(1), "retention must still invalidate the published day")
	require.LessOrEqual(t, after-before, int64(3), "state-row writes must not grow with the batch size")
	require.Equal(t, hour.Format("2006-01-02"), closed)
}

// A row locked by another transaction makes the batch back off without
// deleting anything. The run must report the backlog instead of looking
// finished, and complete once the lock is gone.
func TestGroupUsageRollupSingleBump_RetentionReportsBacklogBehindLockedRow(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	db := hourlyInvalidationDB(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	hour := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	cutoff := hour.Add(4 * time.Hour)
	_, err := db.ExecContext(ctx, `INSERT INTO groups(id) VALUES(10)`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO usage_logs(id,group_id,created_at)
		SELECT n,10,$1::timestamptz + INTERVAL '1 minute' FROM generate_series(1,5) n`, hour)
	require.NoError(t, err)
	require.NoError(t, repo.AggregateRange(ctx, hour, hour.Add(time.Hour)))
	require.NoError(t, repo.UpdateAggregationWatermark(ctx, time.Now().UTC()))

	holder, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback() }()
	_, err = holder.ExecContext(ctx, `SELECT id FROM usage_logs WHERE id=1 FOR UPDATE`)
	require.NoError(t, err)

	require.ErrorIs(t, repo.CleanupUsageLogs(ctx, cutoff), service.ErrUsageLogsRetentionPending)
	var remaining int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs`).Scan(&remaining))
	require.Equal(t, 5, remaining)

	require.NoError(t, holder.Rollback())
	require.NoError(t, repo.CleanupUsageLogs(ctx, cutoff))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs`).Scan(&remaining))
	require.Zero(t, remaining)
}
