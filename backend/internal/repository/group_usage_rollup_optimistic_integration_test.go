//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// Pause an actual source SELECT, rather than relying on a large or slow fixture.
// A current write must finish while the scanner is demonstrably still blocked.
func TestGroupUsageRollupOptimistic_CurrentInsertDuringBlockedScan(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f := newOptimisticGroupRollupFixture(t, ctx, false)
	scanner := f.conn(t, ctx)
	writer := f.conn(t, ctx)
	blocker := f.conn(t, ctx)
	f.seed(t, ctx, scanner, 1, f.today.Add(-12*time.Hour), "1.2345678912")
	plan, ready, err := prepareGroupUsageRollup(ctx, scanner, f.today)
	require.NoError(t, err)
	require.True(t, ready)

	var scannerPID int
	require.NoError(t, scanner.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&scannerPID))
	_, err = blocker.ExecContext(ctx, `SELECT pg_advisory_lock(1948063711, $1)`, scannerPID)
	require.NoError(t, err)
	defer func() {
		_, _ = blocker.ExecContext(context.Background(), `SELECT pg_advisory_unlock(1948063711, $1)`, scannerPID)
	}()
	_, err = scanner.ExecContext(ctx, fmt.Sprintf(`
		ALTER TABLE usage_logs RENAME TO usage_logs_raw;
		CREATE FUNCTION pause_group_rollup_test_scan() RETURNS boolean LANGUAGE plpgsql VOLATILE AS $$
		BEGIN
			PERFORM pg_advisory_xact_lock(1948063711, %d);
			RETURN true;
		END;
		$$;
		CREATE VIEW usage_logs AS SELECT * FROM usage_logs_raw WHERE pause_group_rollup_test_scan();
	`, scannerPID))
	require.NoError(t, err)
	type scanResult struct {
		costs []groupUsageRollupCost
		err   error
	}
	scanned := make(chan scanResult, 1)
	go func() {
		costs, scanErr := scanGroupUsageRollup(ctx, scanner, &plan)
		scanned <- scanResult{costs, scanErr}
	}()
	require.Eventually(t, func() bool {
		var blocked bool
		return writer.QueryRowContext(ctx, `SELECT wait_event_type='Lock' AND wait_event='advisory' FROM pg_stat_activity WHERE pid=$1`, scannerPID).Scan(&blocked) == nil && blocked
	}, 3*time.Second, 10*time.Millisecond, "source SELECT must reach the controlled scan barrier")
	writeCtx, writeCancel := context.WithTimeout(ctx, time.Second)
	_, err = writer.ExecContext(writeCtx, `INSERT INTO usage_logs_raw(id,user_id,group_id,actual_cost,created_at) VALUES(2,1,10,2,$1)`, f.today.Add(time.Hour))
	writeCancel()
	require.NoError(t, err, "current usage must commit while a historical source scan is still running")
	var revision int64
	require.NoError(t, writer.QueryRowContext(ctx, `SELECT source_revision FROM usage_group_rollup_state WHERE id=1`).Scan(&revision))
	require.Equal(t, plan.sourceRevision, revision, "current writes must not invalidate a historical scan")
	_, err = blocker.ExecContext(ctx, `SELECT pg_advisory_unlock(1948063711, $1)`, scannerPID)
	require.NoError(t, err)
	result := <-scanned
	require.NoError(t, result.err)
	published, err := publishGroupUsageRollup(ctx, scanner, plan, result.costs)
	require.NoError(t, err)
	require.True(t, published)
	f.assertBucket(t, ctx, scanner, f.today.AddDate(0, 0, -1), "1.2345678912")
}

func TestGroupUsageRollupOptimistic_RejectsHistoricalChanges(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	cases := []struct {
		name        string
		partitioned bool
		mutation    string
	}{
		{"insert", false, `INSERT INTO usage_logs(id,user_id,group_id,actual_cost,created_at) VALUES(2,1,10,2,$1)`},
		{"update_cost", false, `UPDATE usage_logs SET actual_cost=9 WHERE id=1 AND created_at=$1`},
		{"move_timestamp", false, `UPDATE usage_logs SET created_at=$1::timestamptz + INTERVAL '2 days' WHERE id=1`},
		{"move_group", false, `UPDATE usage_logs SET group_id=20 WHERE id=1 AND created_at=$1`},
		{"delete", false, `DELETE FROM usage_logs WHERE id=1 AND created_at=$1`},
		{"cascade_delete", false, `DELETE FROM users WHERE id=1 AND EXISTS(SELECT 1 FROM usage_logs WHERE created_at=$1)`},
		{"cascade_clear_group", false, `DELETE FROM groups WHERE id=10 AND EXISTS(SELECT 1 FROM usage_logs WHERE created_at=$1)`},
		{"direct_leaf_insert", true, `INSERT INTO usage_logs_default(id,user_id,group_id,actual_cost,created_at) VALUES(2,1,10,2,$1)`},
		{"direct_leaf_update", true, `UPDATE usage_logs_default SET actual_cost=9 WHERE id=1 AND created_at=$1`},
		{"direct_leaf_delete", true, `DELETE FROM usage_logs_default WHERE id=1 AND created_at=$1`},
		{"earlier_than_discovered_min", false, `INSERT INTO usage_logs(id,user_id,group_id,actual_cost,created_at) VALUES(2,1,10,2,$1::timestamptz-INTERVAL '30 days')`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			f := newOptimisticGroupRollupFixture(t, ctx, tc.partitioned)
			scanner, writer := f.conn(t, ctx), f.conn(t, ctx)
			at := f.today.Add(-12 * time.Hour)
			f.seed(t, ctx, scanner, 1, at, "1.25")
			plan, ready, err := prepareGroupUsageRollup(ctx, scanner, f.today)
			require.NoError(t, err)
			require.True(t, ready)
			costs, err := scanGroupUsageRollup(ctx, scanner, &plan)
			require.NoError(t, err)
			_, err = writer.ExecContext(ctx, tc.mutation, at)
			require.NoError(t, err)
			published, err := publishGroupUsageRollup(ctx, scanner, plan, costs)
			require.NoError(t, err)
			require.False(t, published, "a committed historical change must reject the stale scan")
			var closed string
			var revision, buckets int64
			require.NoError(t, writer.QueryRowContext(ctx, `SELECT closed_before::text,source_revision FROM usage_group_rollup_state WHERE id=1`).Scan(&closed, &revision))
			require.Equal(t, "1970-01-01", closed, "rejected publication must not advance the watermark")
			require.Greater(t, revision, plan.sourceRevision, "mutation must invalidate even when watermark cannot move further back")
			require.NoError(t, writer.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_group_daily_rollups`).Scan(&buckets))
			require.Zero(t, buckets, "rejected publication must not leave partial buckets")
		})
	}
}

func TestGroupUsageRollupOptimistic_InFlightWriterDefersPrepare(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f := newOptimisticGroupRollupFixture(t, ctx, false)
	scanner, writer := f.conn(t, ctx), f.conn(t, ctx)
	tx, err := writer.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	// The write began before the new day's horizon was registered and still owns
	// its KEY SHARE gate. It must not be omitted by a scanner crossing midnight.
	_, err = tx.ExecContext(ctx, `INSERT INTO usage_logs(id,user_id,group_id,actual_cost,created_at) VALUES(1,1,10,3,$1)`, f.today.Add(-time.Second))
	require.NoError(t, err)
	started := time.Now()
	_, _, err = prepareGroupUsageRollup(ctx, scanner, f.today)
	require.Error(t, err, "maintenance must defer while a pre-horizon writer is uncommitted")
	require.Less(t, time.Since(started), 3*time.Second, "maintenance lock wait must be bounded")
	require.NoError(t, tx.Commit(), "maintenance timeout must not abort the writer")
	plan, ready, err := prepareGroupUsageRollup(ctx, scanner, f.today)
	require.NoError(t, err)
	require.True(t, ready)
	costs, err := scanGroupUsageRollup(ctx, scanner, &plan)
	require.NoError(t, err)
	published, err := publishGroupUsageRollup(ctx, scanner, plan, costs)
	require.NoError(t, err)
	require.True(t, published)
	f.assertBucket(t, ctx, scanner, f.today.AddDate(0, 0, -1), "3.0000000000")
}

func TestGroupUsageRollupOptimistic_ConcurrentPublishersCannotOverwrite(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f := newOptimisticGroupRollupFixture(t, ctx, false)
	first, second := f.conn(t, ctx), f.conn(t, ctx)
	f.seed(t, ctx, first, 1, f.today.Add(-12*time.Hour), "1.2345678912")
	plan, ready, err := prepareGroupUsageRollup(ctx, first, f.today)
	require.NoError(t, err)
	require.True(t, ready)
	costs, err := scanGroupUsageRollup(ctx, first, &plan)
	require.NoError(t, err)
	type publishResult struct {
		published bool
		err       error
	}
	start := make(chan struct{})
	results := make(chan publishResult, 2)
	for _, conn := range []*sql.Conn{first, second} {
		go func(conn *sql.Conn) {
			<-start
			published, publishErr := publishGroupUsageRollup(ctx, conn, plan, costs)
			results <- publishResult{published, publishErr}
		}(conn)
	}
	close(start)
	publishedCount := 0
	for range 2 {
		result := <-results
		require.NoError(t, result.err)
		if result.published {
			publishedCount++
		}
	}
	require.Equal(t, 1, publishedCount, "only one publication may consume a captured revision")
	f.assertBucket(t, ctx, first, f.today.AddDate(0, 0, -1), "1.2345678912")
}

func TestGroupUsageRollupOptimistic_RegistrationFencesOlderScanner(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f := newOptimisticGroupRollupFixture(t, ctx, false)
	first, second := f.conn(t, ctx), f.conn(t, ctx)
	f.seed(t, ctx, first, 1, f.today.Add(-12*time.Hour), "1.25")
	oldPlan, ready, err := prepareGroupUsageRollup(ctx, first, f.today)
	require.NoError(t, err)
	require.True(t, ready)
	oldCosts, err := scanGroupUsageRollup(ctx, first, &oldPlan)
	require.NoError(t, err)
	newPlan, ready, err := prepareGroupUsageRollup(ctx, second, f.today)
	require.NoError(t, err)
	require.True(t, ready)
	require.Greater(t, newPlan.sourceRevision, oldPlan.sourceRevision)
	published, err := publishGroupUsageRollup(ctx, first, oldPlan, oldCosts)
	require.NoError(t, err)
	require.False(t, published)
	newCosts, err := scanGroupUsageRollup(ctx, second, &newPlan)
	require.NoError(t, err)
	published, err = publishGroupUsageRollup(ctx, second, newPlan, newCosts)
	require.NoError(t, err)
	require.True(t, published)
}

func TestGroupUsageRollupOptimistic_PartitionDropInvalidatesScan(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f := newOptimisticGroupRollupFixture(t, ctx, true)
	scanner, writer := f.conn(t, ctx), f.conn(t, ctx)
	at := f.today.Add(-12 * time.Hour)
	f.seed(t, ctx, scanner, 1, at, "4.25")
	plan, ready, err := prepareGroupUsageRollup(ctx, scanner, f.today)
	require.NoError(t, err)
	require.True(t, ready)
	costs, err := scanGroupUsageRollup(ctx, scanner, &plan)
	require.NoError(t, err)
	tx, err := writer.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	require.NoError(t, lockGroupUsageRollupState(ctx, tx))
	require.NoError(t, invalidateGroupUsageRollupsAt(ctx, tx, at))
	_, err = tx.ExecContext(ctx, `DROP TABLE usage_logs_default`)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	published, err := publishGroupUsageRollup(ctx, scanner, plan, costs)
	require.NoError(t, err)
	require.False(t, published, "partition removal must revoke scans even without row DELETE triggers")
}

func TestGroupUsageRollupOptimistic_LateWriteUsesPublishedTimezone(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f := newOptimisticGroupRollupFixture(t, ctx, true)
	scanner, writer := f.conn(t, ctx), f.conn(t, ctx)
	f.seed(t, ctx, scanner, 1, f.today.Add(-12*time.Hour), "1.25")
	plan, ready, err := prepareGroupUsageRollup(ctx, scanner, f.today)
	require.NoError(t, err)
	require.True(t, ready)
	costs, err := scanGroupUsageRollup(ctx, scanner, &plan)
	require.NoError(t, err)
	published, err := publishGroupUsageRollup(ctx, scanner, plan, costs)
	require.NoError(t, err)
	require.True(t, published)
	_, err = writer.ExecContext(ctx, `SET TIME ZONE 'Asia/Tokyo'`)
	require.NoError(t, err)
	_, err = writer.ExecContext(ctx, `INSERT INTO usage_logs_default(id,user_id,group_id,actual_cost,created_at) VALUES(2,1,10,2,$1)`, f.today.Add(-30*time.Minute))
	require.NoError(t, err)
	var closed string
	require.NoError(t, writer.QueryRowContext(ctx, `SELECT closed_before::text FROM usage_group_rollup_state WHERE id=1`).Scan(&closed))
	require.Equal(t, f.today.AddDate(0, 0, -1).Format("2006-01-02"), closed, "late write must invalidate its UTC bucket even when session date is tomorrow")
}

func TestGroupUsageRollupOptimistic_KeepsCommittedDayWhenNextScanRejected(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f := newOptimisticGroupRollupFixture(t, ctx, false)
	scanner, writer := f.conn(t, ctx), f.conn(t, ctx)
	firstDay := f.today.AddDate(0, 0, -2)
	f.seed(t, ctx, scanner, 1, firstDay.Add(time.Hour), "1.25")
	f.seed(t, ctx, scanner, 2, firstDay.AddDate(0, 0, 1).Add(time.Hour), "2.50")
	plan, ready, err := prepareGroupUsageRollup(ctx, scanner, f.today)
	require.NoError(t, err)
	require.True(t, ready)
	costs, err := scanGroupUsageRollup(ctx, scanner, &plan)
	require.NoError(t, err)
	published, err := publishGroupUsageRollup(ctx, scanner, plan, costs)
	require.NoError(t, err)
	require.True(t, published)
	plan, ready, err = prepareGroupUsageRollup(ctx, scanner, f.today)
	require.NoError(t, err)
	require.True(t, ready)
	costs, err = scanGroupUsageRollup(ctx, scanner, &plan)
	require.NoError(t, err)
	_, err = writer.ExecContext(ctx, `UPDATE usage_logs SET actual_cost=7.5 WHERE id=2`)
	require.NoError(t, err)
	published, err = publishGroupUsageRollup(ctx, scanner, plan, costs)
	require.NoError(t, err)
	require.False(t, published)
	f.assertBucket(t, ctx, writer, firstDay, "1.2500000000")
	var closed string
	require.NoError(t, writer.QueryRowContext(ctx, `SELECT closed_before::text FROM usage_group_rollup_state WHERE id=1`).Scan(&closed))
	require.Equal(t, firstDay.AddDate(0, 0, 1).Format("2006-01-02"), closed)
	plan, ready, err = prepareGroupUsageRollup(ctx, scanner, f.today)
	require.NoError(t, err)
	require.True(t, ready)
	costs, err = scanGroupUsageRollup(ctx, scanner, &plan)
	require.NoError(t, err)
	published, err = publishGroupUsageRollup(ctx, scanner, plan, costs)
	require.NoError(t, err)
	require.True(t, published)
	f.assertBucket(t, ctx, writer, firstDay.AddDate(0, 0, 1), "7.5000000000")
}

func TestGroupUsageRollupOptimistic_ConcurrentHistoricalWritersDoNotUpgradeDeadlock(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f := newOptimisticGroupRollupFixture(t, ctx, false)
	scanner := f.conn(t, ctx)
	f.seed(t, ctx, scanner, 1, f.today.Add(-time.Hour), "1")
	_, ready, err := prepareGroupUsageRollup(ctx, scanner, f.today)
	require.NoError(t, err)
	require.True(t, ready)
	var writers []*sql.Tx
	for i := 0; i < 2; i++ {
		conn := f.conn(t, ctx)
		tx, beginErr := conn.BeginTx(ctx, nil)
		require.NoError(t, beginErr)
		defer func() { _ = tx.Rollback() }()
		_, err = tx.ExecContext(ctx, `INSERT INTO usage_logs(id,user_id,group_id,actual_cost,created_at) VALUES($1,1,10,1,$2)`, 10+i, f.today.Add(time.Hour))
		require.NoError(t, err)
		writers = append(writers, tx)
	}
	// Both transactions already own KEY SHARE. A historical write must only
	// request the compatible non-key UPDATE, not upgrade to FOR UPDATE.
	start, results := make(chan struct{}), make(chan error, 2)
	for i, tx := range writers {
		go func(i int, tx *sql.Tx) {
			<-start
			_, writeErr := tx.ExecContext(ctx, `INSERT INTO usage_logs(id,user_id,group_id,actual_cost,created_at) VALUES($1,1,10,1,$2)`, 20+i, f.today.Add(-time.Hour))
			if writeErr == nil {
				writeErr = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
			results <- writeErr
		}(i, tx)
	}
	close(start)
	for range 2 {
		require.NoError(t, <-results, "compatible ingest locks must not form an upgrade deadlock")
	}
	var count int
	require.NoError(t, scanner.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs`).Scan(&count))
	require.Equal(t, 5, count)
}

func TestGroupUsageRollupOptimistic_PublisherElectionDoesNotBlockIngest(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f := newOptimisticGroupRollupFixture(t, ctx, false)
	owner, contender := f.conn(t, ctx), f.conn(t, ctx)
	f.seed(t, ctx, owner, 1, f.today.Add(-time.Hour), "1")
	_, err := owner.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, groupUsageRollupPublisherLock)
	require.NoError(t, err)
	defer func() {
		_, _ = owner.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, groupUsageRollupPublisherLock)
	}()
	shortCtx, shortCancel := context.WithTimeout(ctx, time.Second)
	repo := newDashboardAggregationRepositoryWithSQL(contender)
	err = repo.SyncGroupUsageRollups(shortCtx, f.today)
	shortCancel()
	require.NoError(t, err, "another elected publisher should be skipped immediately")
	f.seed(t, ctx, contender, 2, f.today.Add(time.Hour), "2")
	var closed string
	require.NoError(t, contender.QueryRowContext(ctx, `SELECT closed_before::text FROM usage_group_rollup_state WHERE id=1`).Scan(&closed))
	require.Equal(t, "1970-01-01", closed, "contender must not publish while another session owns the job lock")
	_, err = owner.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, groupUsageRollupPublisherLock)
	require.NoError(t, err)
	require.NoError(t, repo.SyncGroupUsageRollups(ctx, f.today))
	f.assertBucket(t, ctx, contender, f.today.AddDate(0, 0, -1), "1.0000000000")
}

func TestGroupUsageRollupOptimistic_BoundedBatchResumesFromCommittedWatermark(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f := newOptimisticGroupRollupFixture(t, ctx, false)
	conn := f.conn(t, ctx)
	firstDay := f.today.AddDate(0, 0, -10)
	for day := 0; day < 10; day++ {
		f.seed(t, ctx, conn, day+1, firstDay.AddDate(0, 0, day).Add(time.Hour), "1")
	}
	repo := newDashboardAggregationRepositoryWithSQL(conn)
	require.NoError(t, repo.SyncGroupUsageRollups(ctx, f.today))
	var closed string
	var buckets int
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT closed_before::text FROM usage_group_rollup_state WHERE id=1`).Scan(&closed))
	require.Equal(t, firstDay.AddDate(0, 0, 7).Format("2006-01-02"), closed)
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_group_daily_rollups`).Scan(&buckets))
	require.Equal(t, 7, buckets, "one invocation must not replay the full historical backlog")
	require.NoError(t, repo.SyncGroupUsageRollups(ctx, f.today))
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT closed_before::text FROM usage_group_rollup_state WHERE id=1`).Scan(&closed))
	require.Equal(t, f.today.Format("2006-01-02"), closed)
	var total string
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*), SUM(actual_cost)::text FROM usage_group_daily_rollups`).Scan(&buckets, &total))
	require.Equal(t, 10, buckets)
	require.Equal(t, "10.0000000000", total)
	var before, after int64
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT source_revision FROM usage_group_rollup_state WHERE id=1`).Scan(&before))
	require.NoError(t, repo.SyncGroupUsageRollups(ctx, f.today))
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT source_revision FROM usage_group_rollup_state WHERE id=1`).Scan(&after))
	require.Equal(t, before, after, "an already closed backlog must remain a no-op")
}

func TestGroupUsageRollupOptimistic_ReaderKeepsSnapshotAcrossTimezoneRebuild(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f := newOptimisticGroupRollupFixture(t, ctx, false)
	reader, writer, blocker := f.conn(t, ctx), f.conn(t, ctx), f.conn(t, ctx)
	for day := 1; day <= 3; day++ {
		f.seed(t, ctx, writer, day, f.today.AddDate(0, 0, day-4).Add(12*time.Hour), fmt.Sprint(day))
	}
	require.NoError(t, newDashboardAggregationRepositoryWithSQL(writer).SyncGroupUsageRollups(ctx, f.today))
	var readerPID int
	require.NoError(t, reader.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&readerPID))
	_, err := blocker.ExecContext(ctx, `SELECT pg_advisory_lock(1948063712,$1)`, readerPID)
	require.NoError(t, err)
	defer func() {
		_, _ = blocker.ExecContext(context.Background(), `SELECT pg_advisory_unlock(1948063712,$1)`, readerPID)
	}()
	// Pause the first metadata SELECT after its statement snapshot exists. A
	// separate instance can publish a different timezone before the reader's
	// second query; both queries must still observe the original snapshot.
	_, err = writer.ExecContext(ctx, fmt.Sprintf(`
		ALTER TABLE usage_group_rollup_state RENAME TO usage_group_rollup_state_raw;
		CREATE FUNCTION pause_group_rollup_test_reader() RETURNS boolean LANGUAGE plpgsql VOLATILE AS $$
		BEGIN
			IF pg_backend_pid()=%d THEN
				PERFORM pg_advisory_xact_lock(1948063712,%d);
			END IF;
			RETURN true;
		END;
		$$;
		CREATE VIEW usage_group_rollup_state AS
		SELECT * FROM usage_group_rollup_state_raw WHERE pause_group_rollup_test_reader();
	`, readerPID, readerPID))
	require.NoError(t, err)
	type summaryResult struct {
		total float64
		err   error
	}
	results := make(chan summaryResult, 1)
	go func() {
		rows, readErr := newUsageLogRepositoryWithSQL(nil, reader).GetAllGroupUsageSummary(ctx, f.today)
		var total float64
		for _, row := range rows {
			if row.GroupID == 10 {
				total = row.TotalCost
			}
		}
		results <- summaryResult{total, readErr}
	}()
	require.Eventually(t, func() bool {
		var waiting bool
		return writer.QueryRowContext(ctx, `SELECT wait_event_type='Lock' AND wait_event='advisory' FROM pg_stat_activity WHERE pid=$1`, readerPID).Scan(&waiting) == nil && waiting
	}, 3*time.Second, 10*time.Millisecond)
	// Simulate another instance's atomic first-day publication in Asia/Tokyo:
	// old buckets disappear, one new bucket exists, and the remaining source is
	// an open tail. Combining the old watermark with these new buckets gives 1
	// instead of the correct 6 under either complete database state.
	tx, err := writer.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `DELETE FROM usage_group_daily_rollups;
		INSERT INTO usage_group_daily_rollups(bucket_date,group_id,actual_cost) VALUES('2026-10-29',10,1);
		UPDATE usage_group_rollup_state SET timezone_name='Asia/Tokyo',closed_before='2026-10-30' WHERE id=1`)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	_, err = blocker.ExecContext(ctx, `SELECT pg_advisory_unlock(1948063712,$1)`, readerPID)
	require.NoError(t, err)
	result := <-results
	require.NoError(t, result.err)
	require.Equal(t, float64(6), result.total, "metadata and buckets must use one repeatable-read snapshot")
	rows, err := newUsageLogRepositoryWithSQL(nil, writer).GetAllGroupUsageSummary(ctx, f.today)
	require.NoError(t, err)
	for _, row := range rows {
		if row.GroupID == 10 {
			require.Equal(t, float64(6), row.TotalCost, "a fresh UTC reader must fall back to raw after a timezone switch")
		}
	}
}

type optimisticGroupRollupFixture struct {
	schema string
	today  time.Time
}

func newOptimisticGroupRollupFixture(t *testing.T, ctx context.Context, partitioned bool) optimisticGroupRollupFixture {
	t.Helper()
	f := optimisticGroupRollupFixture{
		schema: fmt.Sprintf("group_rollup_optimistic_%d", time.Now().UnixNano()),
		today:  time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
	}
	_, err := integrationDB.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(f.schema))
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, cleanupErr := integrationDB.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+pq.QuoteIdentifier(f.schema)+" CASCADE")
		require.NoError(t, cleanupErr)
	})
	conn := f.conn(t, ctx)
	usageDDL := `CREATE TABLE usage_logs (
		id BIGINT PRIMARY KEY,
		user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		group_id BIGINT REFERENCES groups(id) ON DELETE SET NULL,
		actual_cost NUMERIC(20,10) NOT NULL,
		created_at TIMESTAMPTZ NOT NULL
	);`
	if partitioned {
		usageDDL = `CREATE TABLE usage_logs (
			id BIGINT NOT NULL,
			user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			group_id BIGINT REFERENCES groups(id) ON DELETE SET NULL,
			actual_cost NUMERIC(20,10) NOT NULL,
			created_at TIMESTAMPTZ NOT NULL
		) PARTITION BY RANGE(created_at);
		CREATE TABLE usage_logs_default PARTITION OF usage_logs DEFAULT;`
	}
	_, err = conn.ExecContext(ctx, `CREATE TABLE users(id BIGINT PRIMARY KEY); CREATE TABLE groups(id BIGINT PRIMARY KEY);`+usageDDL)
	require.NoError(t, err)
	for _, name := range []string{"222_group_usage_daily_rollups.sql", "223_group_usage_rollup_timezone.sql", "258_group_usage_rollup_optimistic.sql"} {
		migration, readErr := migrations.FS.ReadFile(name)
		require.NoError(t, readErr)
		_, err = conn.ExecContext(ctx, string(migration))
		require.NoError(t, err, name)
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO users(id) VALUES(1); INSERT INTO groups(id) VALUES(10),(20); UPDATE usage_group_rollup_state SET timezone_name='UTC' WHERE id=1`)
	require.NoError(t, err)
	return f
}

func (f optimisticGroupRollupFixture) conn(t *testing.T, ctx context.Context) *sql.Conn {
	t.Helper()
	conn, err := integrationDB.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		resetCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(resetCtx, `RESET search_path; RESET TIME ZONE`)
		_ = conn.Close()
	})
	_, err = conn.ExecContext(ctx, "SET search_path TO "+pq.QuoteIdentifier(f.schema)+"; SET TIME ZONE 'UTC'")
	require.NoError(t, err)
	return conn
}

func (f optimisticGroupRollupFixture) seed(t *testing.T, ctx context.Context, conn *sql.Conn, id int, at time.Time, amount string) {
	t.Helper()
	_, err := conn.ExecContext(ctx, `INSERT INTO usage_logs(id,user_id,group_id,actual_cost,created_at) VALUES($1,1,10,$2,$3)`, id, amount, at)
	require.NoError(t, err)
}

func (f optimisticGroupRollupFixture) assertBucket(t *testing.T, ctx context.Context, conn *sql.Conn, day time.Time, expected string) {
	t.Helper()
	var actual string
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT actual_cost::text FROM usage_group_daily_rollups WHERE bucket_date=$1::date AND group_id=10`, day.Format("2006-01-02")).Scan(&actual))
	require.Equal(t, expected, actual)
}
