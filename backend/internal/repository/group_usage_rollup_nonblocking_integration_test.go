//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Upstream's nonblocking-build concern remains covered by the optimistic scan
// suite. These regressions exercise the merged protocol and retention contract,
// including databases that received 258 before the newly inserted upstream 251.
func TestGroupUsageRollupProtocolCompatibility_MigrationOrder(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	for _, order := range []struct {
		name       string
		migrations []string
	}{
		{"fresh_filename_order", []string{groupUsageRollupUpstreamMigration, "258_group_usage_rollup_optimistic.sql", groupUsageRollupCompatibilityMigration}},
		{"existing_custom258", []string{"258_group_usage_rollup_optimistic.sql", groupUsageRollupUpstreamMigration, groupUsageRollupCompatibilityMigration}},
		{"official251_without_custom258", []string{groupUsageRollupUpstreamMigration, groupUsageRollupCompatibilityMigration}},
	} {
		for _, partitioned := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/partitioned_%t", order.name, partitioned), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				f := newOptimisticGroupRollupFixtureWithMigrations(t, ctx, partitioned, order.migrations)
				scanner, writer := f.conn(t, ctx), f.conn(t, ctx)
				f.seed(t, ctx, writer, 1, f.today.Add(-12*time.Hour), "1.25")
				plan, ready, err := prepareGroupUsageRollup(ctx, scanner, f.today)
				require.NoError(t, err)
				require.True(t, ready)
				costs, err := scanGroupUsageRollup(ctx, scanner, &plan)
				require.NoError(t, err)
				table := "usage_logs"
				if partitioned {
					table = "usage_logs_default"
				}
				_, err = writer.ExecContext(ctx, "UPDATE "+table+" SET actual_cost=9 WHERE id=1")
				require.NoError(t, err)
				published, err := publishGroupUsageRollup(ctx, scanner, plan, costs)
				require.NoError(t, err)
				require.False(t, published, "a change in an unpublished scanned day must invalidate even when closed_before is already earlier")
				require.NoError(t, newDashboardAggregationRepositoryWithSQL(scanner).SyncGroupUsageRollups(ctx, f.today))
				f.assertBucket(t, ctx, writer, f.today.Add(-24*time.Hour), "9.0000000000")
			})
		}
	}
}

func TestGroupUsageRollupProtocolCompatibility_ExpiredMarkerDoesNotHideRetainedRaw(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f := newOptimisticGroupRollupFixture(t, ctx, false)
	conn := f.conn(t, ctx)
	f.seed(t, ctx, conn, 1, f.today.Add(-12*time.Hour), "3")
	_, err := conn.ExecContext(ctx, "UPDATE usage_group_rollup_state SET expired_before=$1, building_until=$2, build_dirty_from=$2 WHERE id=1", f.today, f.today.Format("2006-01-02"))
	require.NoError(t, err)
	for _, stateTimezone := range []string{"UTC", "Asia/Shanghai"} {
		_, err = conn.ExecContext(ctx, "UPDATE usage_group_rollup_state SET timezone_name=$1 WHERE id=1", stateTimezone)
		require.NoError(t, err)
		rows, readErr := newUsageLogRepositoryWithSQL(nil, conn).GetAllGroupUsageSummary(ctx, f.today)
		require.NoError(t, readErr)
		var total float64
		for _, row := range rows {
			total += row.TotalCost
		}
		require.InDelta(t, 3, total, 1e-9, "valid or invalid watermark must include retained raw before an upstream expired marker")
	}
	require.NoError(t, newDashboardAggregationRepositoryWithSQL(conn).SyncGroupUsageRollups(ctx, f.today))
	f.assertBucket(t, ctx, conn, f.today.Add(-24*time.Hour), "3.0000000000")
}

// A long reader can prevent DROP, but bounded row deletion can still reclaim
// certified raw data. The archived totals survive and the retained frontier
// advances only in the same transaction as the removed source rows.
func TestGroupUsageRollupRetentionCleanupSkipsPartitionDropOnLockTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db := hourlyInvalidationDB(t, true)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	month := truncateToMonthUTC(time.Now().UTC().AddDate(0, -4, 0))
	hour := month.Add(time.Hour)
	require.NoError(t, repo.createUsageLogsPartition(ctx, month))
	_, err := db.ExecContext(ctx, "INSERT INTO usage_logs(id,created_at) VALUES(1,$1)", hour.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, repo.AggregateRange(ctx, hour, hour.Add(time.Hour)))
	reader, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer reader.Rollback()
	var count int
	require.NoError(t, reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_logs").Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, repo.CleanupUsageLogs(ctx, month.AddDate(0, 1, 0)))
	require.NoError(t, reader.Rollback())
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, "SELECT to_regclass($1) IS NOT NULL", "usage_logs_"+month.Format("200601")).Scan(&exists))
	require.True(t, exists, "the reader blocks partition DROP")
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_logs").Scan(&count))
	require.Zero(t, count, "fallback must remove rows instead of stopping at the failed DROP")
	require.NoError(t, db.QueryRowContext(ctx, "SELECT total_requests FROM usage_dashboard_hourly WHERE bucket_start=$1", hour).Scan(&count))
	require.Equal(t, 1, count, "retained dashboard history must survive raw cleanup")
}

type groupUsageRollupTestState struct {
	closedBefore   string
	retainedFrom   time.Time
	expiredBefore  time.Time
	buildingUntil  sql.NullString
	buildDirtyFrom sql.NullString
}

const groupUsageRollupTestStateQuery = `
	SELECT closed_before::text, retained_from, expired_before, building_until::text, build_dirty_from::text
	FROM usage_group_rollup_state
	WHERE id = 1
`

func readGroupUsageRollupTestState(t *testing.T, ctx context.Context, q sqlQueryer) groupUsageRollupTestState {
	t.Helper()
	var state groupUsageRollupTestState
	require.NoError(t, scanSingleRow(ctx, q, groupUsageRollupTestStateQuery, nil,
		&state.closedBefore, &state.retainedFrom, &state.expiredBefore, &state.buildingUntil, &state.buildDirtyFrom))
	return state
}

func readGroupUsageRollupTestStateTx(t *testing.T, ctx context.Context, tx *sql.Tx) groupUsageRollupTestState {
	t.Helper()
	return readGroupUsageRollupTestState(t, ctx, tx)
}

// openGroupUsageRollupSchemaDB 打开一个 search_path 指向 schema 的连接池，供仓储以 *sql.DB 方式运行。
func openGroupUsageRollupSchemaDB(t *testing.T, schema, timezoneName string) *sql.DB {
	t.Helper()
	dsn, err := url.Parse(integrationDSN)
	require.NoError(t, err)
	query := dsn.Query()
	query.Set("search_path", schema)
	query.Set("TimeZone", timezoneName)
	dsn.RawQuery = query.Encode()
	db, err := sql.Open("postgres", dsn.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.PingContext(context.Background()))
	return db
}

func execGroupUsageRollupSQL(t *testing.T, ctx context.Context, db *sql.DB, query string) {
	t.Helper()
	_, err := db.ExecContext(ctx, query)
	require.NoError(t, err)
}

// requireGroupUsageRollupsMatchUsageLogs 断言 [retainedFrom 所在日, todayStart) 的日桶与 usage_logs 明细逐日一致。
func requireGroupUsageRollupsMatchUsageLogs(t *testing.T, ctx context.Context, db *sql.DB, timezoneName string, retainedFrom, todayStart time.Time) {
	t.Helper()
	expected := collectGroupUsageRollupBuckets(t, ctx, db, `
		SELECT (created_at AT TIME ZONE $3::text)::date::text, group_id, SUM(actual_cost)::float8
		FROM usage_logs
		WHERE group_id IS NOT NULL
			AND created_at >= $1
			AND created_at < $2
		GROUP BY 1, 2
	`, retainedFrom, todayStart, timezoneName)
	actual := collectGroupUsageRollupBuckets(t, ctx, db, `
		SELECT bucket_date::text, group_id, actual_cost::float8
		FROM usage_group_daily_rollups
		WHERE bucket_date >= ($1::timestamptz AT TIME ZONE $3::text)::date
			AND bucket_date < ($2::timestamptz AT TIME ZONE $3::text)::date
	`, retainedFrom, todayStart, timezoneName)
	require.Equal(t, expected, actual)
}

func collectGroupUsageRollupBuckets(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) map[string]float64 {
	t.Helper()
	rows, err := db.QueryContext(ctx, query, args...)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	buckets := make(map[string]float64)
	for rows.Next() {
		var bucketDate string
		var groupID int64
		var cost float64
		require.NoError(t, rows.Scan(&bucketDate, &groupID, &cost))
		buckets[fmt.Sprintf("%s/%d", bucketDate, groupID)] = cost
	}
	require.NoError(t, rows.Err())
	return buckets
}

// groupUsageSummaryByGroup 返回 group_id → [total, today, yesterday]。
func groupUsageSummaryByGroup(t *testing.T, ctx context.Context, db *sql.DB, todayStart time.Time) map[int64][3]float64 {
	t.Helper()
	result, err := newUsageLogRepositoryWithSQL(nil, db).GetAllGroupUsageSummary(ctx, todayStart)
	require.NoError(t, err)
	summary := make(map[int64][3]float64, len(result))
	for _, row := range result {
		summary[row.GroupID] = [3]float64{row.TotalCost, row.TodayCost, row.YesterdayCost}
	}
	return summary
}

// 夏令时在零点跳变的日期（America/Santiago 2026-09-06 没有 00:00）：从该日续建时不覆盖前一天的日桶。
func TestGroupUsageRollupSyncKeepsPreviousDayBucketAcrossMidnightDSTGap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	useGroupUsageRepositoryTestTimezone(t, "America/Santiago")
	schema := createGroupUsageRollupTriggerTestSchema(t, ctx, false)
	db := openGroupUsageRollupSchemaDB(t, schema, "America/Santiago")
	santiago, err := time.LoadLocation("America/Santiago")
	require.NoError(t, err)
	todayStart := time.Date(2026, 9, 8, 0, 0, 0, 0, santiago)

	execGroupUsageRollupSQL(t, ctx, db, `
		INSERT INTO groups (id) VALUES (10);
		INSERT INTO users (id) VALUES (1);
		INSERT INTO usage_logs (id, user_id, group_id, actual_cost, created_at) VALUES
			(1, 1, 10, 1, TIMESTAMPTZ '2026-09-06 02:30:00+00'),
			(2, 1, 10, 2, TIMESTAMPTZ '2026-09-06 03:30:00+00'),
			(3, 1, 10, 4, TIMESTAMPTZ '2026-09-06 05:00:00+00'),
			(4, 1, 10, 8, TIMESTAMPTZ '2026-09-07 13:00:00+00'),
			(5, 1, 10, 16, TIMESTAMPTZ '2026-09-07 03:30:00+00');
		INSERT INTO usage_group_daily_rollups (bucket_date, group_id, actual_cost)
		VALUES (DATE '2026-09-05', 10, 3);
		UPDATE usage_group_rollup_state
		SET closed_before = DATE '2026-09-06',
			retained_from = TIMESTAMPTZ '2026-09-06 02:30:00+00',
			timezone_name = 'America/Santiago'
		WHERE id = 1;
	`)

	repo := newDashboardAggregationRepositoryWithSQL(db)
	require.NoError(t, repo.SyncGroupUsageRollups(ctx, todayStart))

	state := readGroupUsageRollupTestState(t, ctx, db)
	require.Equal(t, "2026-09-08", state.closedBefore)
	requireGroupUsageRollupsMatchUsageLogs(t, ctx, db, "America/Santiago", state.retainedFrom, todayStart)
	var previousDay float64
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT actual_cost::float8 FROM usage_group_daily_rollups
		WHERE bucket_date = DATE '2026-09-05' AND group_id = 10
	`).Scan(&previousDay))
	require.InDelta(t, 3, previousDay, 1e-9)
	for _, name := range []string{"America/Santiago", "UTC", "America/Santiago"} {
		useGroupUsageRepositoryTestTimezone(t, name)
		require.NoError(t, repo.SyncGroupUsageRollups(ctx, todayStart))
		state = readGroupUsageRollupTestState(t, ctx, db)
		requireGroupUsageRollupsMatchUsageLogs(t, ctx, db, name, state.retainedFrom, todayStart)
		summary := groupUsageSummaryByGroup(t, ctx, db, todayStart)
		require.InDelta(t, 31, summary[10][0], 1e-9, "the first hour after the DST gap must be counted exactly once, including after timezone rebuilds")
		if name == "America/Santiago" {
			var gapDay, nextDay float64
			require.NoError(t, db.QueryRowContext(ctx, "SELECT SUM(actual_cost) FILTER(WHERE bucket_date='2026-09-06'), SUM(actual_cost) FILTER(WHERE bucket_date='2026-09-07') FROM usage_group_daily_rollups WHERE group_id=10").Scan(&gapDay, &nextDay))
			require.InDelta(t, 4, gapDay, 1e-9)
			require.InDelta(t, 24, nextDay, 1e-9)
		}
	}
}
