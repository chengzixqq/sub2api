//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func hourlyInvalidationDB(t *testing.T, partitioned bool) *sql.DB {
	t.Helper()
	ctx := context.Background()
	schema := fmt.Sprintf("hourly_invalidation_%d", time.Now().UnixNano())
	_, err := integrationDB.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schema))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DROP SCHEMA "+pq.QuoteIdentifier(schema)+" CASCADE")
	})
	dsn := os.Getenv("SUB2API_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("isolated PostgreSQL DSN required")
	}
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	q := parsed.Query()
	q.Set("search_path", schema)
	q.Set("TimeZone", "UTC")
	parsed.RawQuery = q.Encode()
	db, err := sql.Open("postgres", parsed.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(ctx, `CREATE TABLE groups(id BIGINT PRIMARY KEY); CREATE TABLE users(id BIGINT PRIMARY KEY); INSERT INTO users(id) VALUES(1)`)
	require.NoError(t, err)
	ddl := `CREATE TABLE usage_logs (id BIGINT NOT NULL,user_id BIGINT NOT NULL DEFAULT 1,group_id BIGINT,created_at TIMESTAMPTZ NOT NULL,input_tokens BIGINT NOT NULL DEFAULT 10,output_tokens BIGINT NOT NULL DEFAULT 20,cache_creation_tokens BIGINT NOT NULL DEFAULT 3,cache_read_tokens BIGINT NOT NULL DEFAULT 4,total_cost NUMERIC NOT NULL DEFAULT 2,actual_cost NUMERIC NOT NULL DEFAULT 1,provider_cost_recorded BOOLEAN NOT NULL DEFAULT true,account_stats_cost NUMERIC,account_rate_multiplier NUMERIC,duration_ms BIGINT)`
	if partitioned {
		ddl += ` PARTITION BY RANGE(created_at)`
	}
	_, err = db.ExecContext(ctx, ddl)
	require.NoError(t, err)
	if partitioned {
		_, err = db.ExecContext(ctx, `CREATE TABLE usage_logs_default PARTITION OF usage_logs DEFAULT`)
		require.NoError(t, err)
	}
	for _, name := range []string{"034_usage_dashboard_aggregation_tables.sql", "107_add_account_cost_to_dashboard_tables.sql", "222_group_usage_daily_rollups.sql", "223_group_usage_rollup_timezone.sql", "246_dashboard_hourly_invalidation.sql"} {
		raw, err := migrations.FS.ReadFile(name)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, string(raw))
		require.NoError(t, err)
	}
	return db
}

type hourlyQueryCapture struct {
	*sql.DB
	query string
	args  []any
}

func (c *hourlyQueryCapture) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	c.query = q
	c.args = args
	return c.DB.QueryContext(ctx, q, args...)
}
func noHourlyRawRows(p map[string]any) bool {
	if n, _ := p["Relation Name"].(string); n == "usage_logs" || n == "usage_logs_default" {
		return p["Actual Rows"].(float64) == 0
	}
	if children, ok := p["Plans"].([]any); ok {
		for _, c := range children {
			if !noHourlyRawRows(c.(map[string]any)) {
				return false
			}
		}
	}
	return true
}

func TestDashboardHourlyInvalidation_LateChangesAndEmptyHour(t *testing.T) {
	for _, partitioned := range []bool{false, true} {
		t.Run(fmt.Sprint(partitioned), func(t *testing.T) {
			db := hourlyInvalidationDB(t, partitioned)
			ctx := context.Background()
			hour := time.Now().UTC().Truncate(time.Hour).Add(-24 * time.Hour)
			repo := newDashboardAggregationRepositoryWithSQL(db)
			table := "usage_logs"
			if partitioned {
				table = "usage_logs_default"
			}
			_, err := db.ExecContext(ctx, `INSERT INTO `+table+`(id,created_at) VALUES (1,$1)`, hour.Add(time.Minute))
			require.NoError(t, err)
			require.NoError(t, repo.AggregateRange(ctx, hour, hour.Add(time.Hour)))
			require.NoError(t, repo.UpdateAggregationWatermark(ctx, time.Now().UTC()))
			historical, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)
			_, err = historical.ExecContext(ctx, `SET LOCAL sub2api.dashboard_verified_publish='true'`)
			require.NoError(t, err)
			_, err = historical.ExecContext(ctx, `UPDATE usage_dashboard_hourly SET computed_at=$1`, hour.Add(2*time.Hour))
			require.NoError(t, err)
			require.NoError(t, historical.Commit())
			require.NoError(t, err)
			capture := &hourlyQueryCapture{DB: db}
			usage := &usageLogRepository{sql: capture}
			trend, err := usage.getUsageTrendFromAggregates(ctx, hour, hour.Add(time.Hour), "hour")
			require.NoError(t, err)
			require.Len(t, trend, 1)
			require.EqualValues(t, 1, trend[0].Requests)
			var plan []byte
			require.NoError(t, db.QueryRowContext(ctx, "EXPLAIN (ANALYZE,FORMAT JSON) "+capture.query, capture.args...).Scan(&plan))
			var tree []map[string]any
			require.NoError(t, json.Unmarshal(plan, &tree))
			require.True(t, noHourlyRawRows(tree[0]["Plan"].(map[string]any)), string(plan))
			t.Log("finalized historical bucket reused; raw source actual rows=0")
			for _, change := range []struct {
				sql              string
				args             []any
				requests, tokens int
				cost             float64
			}{
				{`INSERT INTO ` + table + `(id,created_at) VALUES (2,$1)`, []any{hour.Add(2 * time.Minute)}, 2, 74, 4},
				{`UPDATE ` + table + ` SET input_tokens=110 WHERE id=1`, nil, 2, 174, 4},
				{`UPDATE ` + table + ` SET provider_cost_recorded=false WHERE id=1`, nil, 2, 174, 2},
				{`UPDATE ` + table + ` SET actual_cost=9,total_cost=7 WHERE id=2`, nil, 2, 174, 7},
				{`DELETE FROM ` + table + ` WHERE id=1`, nil, 1, 37, 7},
				{`DELETE FROM ` + table + ` WHERE id=2`, nil, 0, 0, 0},
			} {
				_, err = db.ExecContext(ctx, change.sql, change.args...)
				require.NoError(t, err)
				var invalid bool
				require.NoError(t, db.QueryRowContext(ctx, `SELECT computed_at=TIMESTAMPTZ 'epoch' FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour).Scan(&invalid))
				require.True(t, invalid)
				trend, err = usage.getUsageTrendFromAggregates(ctx, hour, hour.Add(time.Hour), "hour")
				require.NoError(t, err)
				if change.requests == 0 {
					require.Empty(t, trend)
				} else {
					require.Len(t, trend, 1)
					require.EqualValues(t, change.requests, trend[0].Requests)
					require.EqualValues(t, change.tokens, trend[0].TotalTokens)
					require.InDelta(t, change.cost, trend[0].Cost, 1e-9)
					require.NotNil(t, trend[0].AccountCost)
					require.InDelta(t, change.cost, *trend[0].AccountCost, 1e-9, "no stats cost or multiplier: account cost equals recorded cost")
				}
				require.NoError(t, repo.RepairInvalidatedBuckets(ctx, 1))
				trend, err = usage.getUsageTrendFromAggregates(ctx, hour, hour.Add(time.Hour), "hour")
				require.NoError(t, err)
				require.Len(t, trend, 1)
				require.EqualValues(t, change.requests, trend[0].Requests)
				require.EqualValues(t, change.tokens, trend[0].TotalTokens)
				require.InDelta(t, change.cost, trend[0].Cost, 1e-9)
				require.NotNil(t, trend[0].AccountCost)
				require.InDelta(t, change.cost, *trend[0].AccountCost, 1e-9, "the hourly aggregate carries the same account cost")
			}
		})
	}
}

func TestDashboardHourlyInvalidation_ConcurrentPublisherAndWriter(t *testing.T) {
	db := hourlyInvalidationDB(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	hour := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	require.NoError(t, repo.AggregateRange(ctx, hour, hour.Add(time.Hour)))
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	// Model the new publisher protocol, not a legacy writer that must revoke
	// provenance. The separate LegacyPublisher test covers that fallback path.
	_, err = tx.ExecContext(ctx, `SET LOCAL sub2api.dashboard_verified_publish='true'`)
	require.NoError(t, err)
	require.NoError(t, lockDashboardHour(ctx, tx, hour))
	writer, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = writer.Rollback() }()
	var pid int
	require.NoError(t, writer.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
	done := make(chan error, 1)
	go func() {
		_, err := writer.ExecContext(ctx, `INSERT INTO usage_logs_default(id,created_at) VALUES (1,$1)`, hour.Add(time.Minute))
		done <- err
	}()
	require.Eventually(t, func() bool {
		var w sql.NullString
		return db.QueryRowContext(ctx, `SELECT wait_event_type FROM pg_stat_activity WHERE pid=$1`, pid).Scan(&w) == nil && w.String == "Lock"
	}, 3*time.Second, 10*time.Millisecond)
	_, err = tx.ExecContext(ctx, `UPDATE usage_dashboard_hourly SET computed_at=NOW() WHERE bucket_start=$1`, hour)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	require.NoError(t, <-done)
	require.NoError(t, writer.Commit())
	var invalid bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT computed_at=TIMESTAMPTZ 'epoch' FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour).Scan(&invalid))
	require.True(t, invalid)
	require.NoError(t, repo.RepairInvalidatedBuckets(ctx, 1))
	writer2, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = writer2.Rollback() }()
	_, err = writer2.ExecContext(ctx, `INSERT INTO usage_logs_default(id,created_at) VALUES (2,$1)`, hour.Add(2*time.Minute))
	require.NoError(t, err)
	publish := make(chan error, 1)
	go func() { publish <- repo.AggregateRange(ctx, hour, hour.Add(time.Hour)) }()
	select {
	case err := <-publish:
		t.Fatalf("publisher did not wait for writer: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, writer2.Commit())
	require.NoError(t, <-publish)
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT total_requests FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour).Scan(&count))
	require.Equal(t, 2, count)
}
func TestDashboardHourlyInvalidation_ReverseHourBulkDoesNotDeadlock(t *testing.T) {
	db := hourlyInvalidationDB(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	hour := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	writer, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = writer.Rollback() }()
	_, err = writer.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES (2,$1)`, hour.Add(90*time.Minute))
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- repo.AggregateRange(ctx, hour, hour.Add(2*time.Hour)) }()
	time.Sleep(100 * time.Millisecond)
	_, err = writer.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES (1,$1)`, hour.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, writer.Commit())
	require.NoError(t, <-done)
	require.NoError(t, repo.RepairInvalidatedBuckets(ctx, 4))
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT SUM(total_requests) FROM usage_dashboard_hourly WHERE bucket_start>=$1 AND bucket_start<$2`, hour, hour.Add(2*time.Hour)).Scan(&count))
	require.Equal(t, 2, count)
}

func TestDashboardHourlyInvalidation_InitialRepairPreservesRetainedHistory(t *testing.T) {
	db := hourlyInvalidationDB(t, false)
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	hour := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES (1,$1),(2,$2)`, hour.Add(time.Minute), hour.Add(2*time.Hour+time.Minute))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO usage_dashboard_hourly(bucket_start,total_requests,computed_at) VALUES ($1,777,$1::timestamptz+INTERVAL '2 hours')`, hour.Add(-48*time.Hour))
	require.NoError(t, err)
	require.NoError(t, repo.AggregateRange(ctx, hour.Add(2*time.Hour), hour.Add(3*time.Hour)))
	// Emulate an otherwise identical legacy snapshot. Certification must compare
	// all metrics to raw before trusting it for the first time.
	_, err = db.ExecContext(ctx, `UPDATE usage_dashboard_hourly SET computed_at=$1 WHERE bucket_start=$2`, hour.Add(4*time.Hour), hour.Add(2*time.Hour))
	require.NoError(t, err)
	require.NoError(t, repo.RepairInvalidatedBuckets(ctx, 4))
	var oldCount, newCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT total_requests FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour.Add(-48*time.Hour)).Scan(&oldCount))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT total_requests FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour.Add(2*time.Hour)).Scan(&newCount))
	require.Equal(t, 777, oldCount)
	require.Equal(t, 1, newCount)
	// Automatic retention keeps the independent aggregate retention window.
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `SET LOCAL sub2api.dashboard_preserve_aggregates='true'; DELETE FROM usage_logs WHERE id=2`)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	var kept bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT computed_at>bucket_start FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour.Add(2*time.Hour)).Scan(&kept))
	require.True(t, kept)
}

func TestDashboardHourlyInvalidation_PartialEdgesAndMissingHours(t *testing.T) {
	db := hourlyInvalidationDB(t, false)
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	hour := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES (1,$1),(2,$2),(3,$3),(4,$4)`, hour.Add(time.Minute), hour.Add(40*time.Minute), hour.Add(90*time.Minute), hour.Add(130*time.Minute))
	require.NoError(t, err)
	require.NoError(t, repo.AggregateRange(ctx, hour, hour.Add(time.Hour)))
	require.NoError(t, repo.UpdateAggregationWatermark(ctx, time.Now().UTC()))
	usage := &usageLogRepository{sql: db}
	trend, err := usage.getUsageTrendFromAggregates(ctx, hour.Add(30*time.Minute), hour.Add(150*time.Minute), "hour")
	require.NoError(t, err)
	var total int64
	for _, point := range trend {
		total += point.Requests
	}
	require.EqualValues(t, 3, total)
	// Mutations rolled back cannot permanently invalidate or change counters.
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE usage_logs SET input_tokens=999 WHERE id=1`)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	var valid bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT computed_at>=bucket_start+INTERVAL '1 hour' FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour).Scan(&valid))
	require.True(t, valid)
}

func TestDashboardHourlyInvalidation_LegacyPublisherRevokesCertification(t *testing.T) {
	db := hourlyInvalidationDB(t, false)
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	hour := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES (1,$1)`, hour.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, repo.AggregateRange(ctx, hour, hour.Add(time.Hour)))
	require.NoError(t, repo.UpdateAggregationWatermark(ctx, time.Now().UTC()))
	var verified bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT source_verified FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour).Scan(&verified))
	require.True(t, verified)
	// This is the legacy publisher's ON CONFLICT shape: it writes computed_at
	// and counters without knowing the new source_verified column or GUC.
	_, err = db.ExecContext(ctx, `INSERT INTO usage_dashboard_hourly(bucket_start,total_requests,computed_at) VALUES($1,999,NOW()) ON CONFLICT(bucket_start) DO UPDATE SET total_requests=EXCLUDED.total_requests,computed_at=EXCLUDED.computed_at`, hour)
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT source_verified FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour).Scan(&verified))
	require.False(t, verified)
	usage := &usageLogRepository{sql: db}
	trend, err := usage.getUsageTrendFromAggregates(ctx, hour, hour.Add(time.Hour), "hour")
	require.NoError(t, err)
	require.Len(t, trend, 1)
	require.EqualValues(t, 1, trend[0].Requests)
}

func TestDashboardHourlyInvalidation_GroupAndHourLockOrder(t *testing.T) {
	db := hourlyInvalidationDB(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	hour := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	_, err := db.ExecContext(ctx, `INSERT INTO groups(id) VALUES(10); INSERT INTO usage_logs(id,group_id,created_at) VALUES(1,10,CURRENT_TIMESTAMP-INTERVAL '48 hours')`)
	require.NoError(t, err)
	cleanup, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = cleanup.Rollback() }()
	require.NoError(t, lockGroupUsageRollupState(ctx, cleanup))
	writer, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = writer.Rollback() }()
	var pid int
	require.NoError(t, writer.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
	insertDone := make(chan error, 1)
	go func() {
		_, err := writer.ExecContext(ctx, `INSERT INTO usage_logs(id,group_id,created_at) VALUES(2,10,$1)`, hour.Add(time.Minute))
		insertDone <- err
	}()
	require.Eventually(t, func() bool {
		var w sql.NullString
		return db.QueryRowContext(ctx, `SELECT wait_event_type FROM pg_stat_activity WHERE pid=$1`, pid).Scan(&w) == nil && w.String == "Lock"
	}, 3*time.Second, 10*time.Millisecond)
	// A blocked INSERT has not acquired its hour shared lock yet. The publisher
	// can acquire exclusive independently; this fails if INSERT locks hour first.
	publisher, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = publisher.Rollback() }()
	require.NoError(t, lockDashboardHour(ctx, publisher, hour))
	deleteDone := make(chan error, 1)
	go func() { _, err := cleanup.ExecContext(ctx, `DELETE FROM usage_logs WHERE id=1`); deleteDone <- err }()
	require.NoError(t, publisher.Commit())
	require.NoError(t, <-deleteDone)
	require.NoError(t, cleanup.Commit())
	require.NoError(t, <-insertDone)
	require.NoError(t, writer.Commit())
}

func TestDashboardHourlyInvalidation_RepairRechecksRetentionAfterSelection(t *testing.T) {
	db := hourlyInvalidationDB(t, false)
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	hour := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES (1,$1),(2,$2),(3,$3)`, hour.Add(-time.Hour), hour.Add(time.Minute), hour.Add(2*time.Hour))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO usage_dashboard_hourly(bucket_start,total_requests,computed_at) VALUES($1,777,$1::timestamptz+INTERVAL '2 hours')`, hour)
	require.NoError(t, err)
	// This candidate was raw-covered when the repair batch was selected.
	var covered bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT $1::timestamptz >= (SELECT MIN(created_at) FROM usage_logs)`, hour).Scan(&covered))
	require.True(t, covered)
	cleanup, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = cleanup.Rollback() }()
	_, err = cleanup.ExecContext(ctx, `SET LOCAL sub2api.dashboard_preserve_aggregates='true';DELETE FROM usage_logs WHERE id IN(1,2)`)
	require.NoError(t, err)
	require.NoError(t, cleanup.Commit())
	// The same repair method now acquires its source lock, checks coverage in
	// a new statement snapshot, and leaves the retained summary untouched.
	require.NoError(t, repo.aggregateHour(ctx, db, hour, true))
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT total_requests FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour).Scan(&count))
	require.Equal(t, 777, count)
}

func TestDashboardHourlyInvalidation_DailyRefreshFailureRollsBackCertification(t *testing.T) {
	db := hourlyInvalidationDB(t, false)
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	hour := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(1,$1)`, hour.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, repo.AggregateRange(ctx, hour, hour.Add(time.Hour)))
	_, err = db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(2,$1)`, hour.Add(2*time.Minute))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE FUNCTION reject_test_daily_publish() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'day refresh rejected'; END; $$; CREATE TRIGGER reject_daily_publish BEFORE INSERT OR UPDATE ON usage_dashboard_daily FOR EACH ROW EXECUTE FUNCTION reject_test_daily_publish()`)
	require.NoError(t, err)
	require.ErrorContains(t, repo.RepairInvalidatedBuckets(ctx, 1), "day refresh rejected")
	var verified bool
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT source_verified,total_requests FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour).Scan(&verified, &count))
	require.True(t, verified, "dirty marker must preserve the last publication's provenance")
	var dirty bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT computed_at=TIMESTAMPTZ 'epoch' FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour).Scan(&dirty))
	require.True(t, dirty)
	require.Equal(t, 1, count)
	_, err = db.ExecContext(ctx, `DROP TRIGGER reject_daily_publish ON usage_dashboard_daily`)
	require.NoError(t, err)
	require.NoError(t, repo.RepairInvalidatedBuckets(ctx, 1))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT source_verified,total_requests FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour).Scan(&verified, &count))
	require.True(t, verified)
	require.Equal(t, 2, count)
}

func TestDashboardHourlyInvalidation_BackfillPreservesArchivedSummaries(t *testing.T) {
	db := hourlyInvalidationDB(t, false)
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	hour := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(1,$1)`, hour.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, repo.AggregateRange(ctx, hour, hour.Add(time.Hour)))
	// Exercise the automatic-retention path, including its persistent frontier.
	_, err = cleanupUsageLogsBatchWithRollupInvalidation(ctx, db, hour.Add(2*time.Hour))
	require.NoError(t, err)
	require.NoError(t, repo.AggregateRange(ctx, hour, hour.Add(time.Hour)))
	var requests, users int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT total_requests,active_users FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour).Scan(&requests, &users))
	require.Equal(t, 1, requests, "backfill must preserve summaries after raw retention")
	require.Equal(t, 1, users)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT total_requests FROM usage_dashboard_daily WHERE bucket_date=$1::date`, hour).Scan(&requests))
	require.Equal(t, 1, requests)
}

func TestDashboardHourlyInvalidation_OldRawMinimumDoesNotProveCoverage(t *testing.T) {
	db := hourlyInvalidationDB(t, false)
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	hour := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	_, err := db.ExecContext(ctx, `INSERT INTO usage_dashboard_hourly(bucket_start,total_requests,computed_at) VALUES($1,777,$1::timestamptz+INTERVAL '2 hours')`, hour)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(1,$1)`, hour.Add(-2*time.Hour))
	require.NoError(t, err)
	// One late old row may move MIN(created_at) backward. It says nothing about
	// whether the intervening raw hours survived legacy retention.
	require.NoError(t, repo.RepairInvalidatedBuckets(ctx, 4))
	var count int
	var verified bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT total_requests,source_verified FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour).Scan(&count, &verified))
	require.Equal(t, 777, count)
	require.False(t, verified)
}

func TestDashboardHourlyInvalidation_HalfHourTimezoneHasConsistentBuckets(t *testing.T) {
	require.NoError(t, timezone.Init("Asia/Kolkata"))
	t.Cleanup(func() { require.NoError(t, timezone.Init("UTC")) })
	db := hourlyInvalidationDB(t, false)
	// Match the production DSN contract: Go and PostgreSQL use the configured TZ.
	db.SetMaxOpenConns(1)
	_, err := db.Exec(`SET TIME ZONE 'Asia/Kolkata'`)
	require.NoError(t, err)
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	hour := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	_, err = db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(1,$1),(2,$2),(3,$3),(4,$4)`, hour.Add(10*time.Minute), hour.Add(50*time.Minute), hour.Add(70*time.Minute), hour.Add(110*time.Minute))
	require.NoError(t, err)
	require.NoError(t, repo.AggregateRange(ctx, hour, hour.Add(time.Hour)))
	require.NoError(t, repo.AggregateRange(ctx, hour.Add(time.Hour), hour.Add(2*time.Hour)))
	var requests int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT SUM(total_requests) FROM usage_dashboard_hourly`).Scan(&requests))
	require.Equal(t, 4, requests, "adjacent ranges must not overwrite half of a local hour")
	_, err = db.ExecContext(ctx, `UPDATE usage_logs SET input_tokens=111 WHERE id=2`)
	require.NoError(t, err)
	var dirty bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT computed_at=TIMESTAMPTZ 'epoch' FROM usage_dashboard_hourly WHERE bucket_start=date_trunc('hour',$1::timestamptz)`, hour.Add(50*time.Minute)).Scan(&dirty))
	require.True(t, dirty, "writer invalidation must address the publisher's local hour")
}

func TestDashboardHourlyInvalidation_LegacyCertificationChecksEveryMetric(t *testing.T) {
	db := hourlyInvalidationDB(t, false)
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	hour := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(1,$1)`, hour.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, repo.AggregateRange(ctx, hour, hour.Add(time.Hour)))
	for _, field := range []string{"total_requests", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens", "total_cost", "actual_cost", "account_cost", "total_duration_ms", "active_users"} {
		t.Run(field, func(t *testing.T) {
			// The field name is from this fixed test list, never external input.
			_, err := db.ExecContext(ctx, `UPDATE usage_dashboard_hourly SET `+field+`=`+field+`+1 WHERE bucket_start=$1`, hour)
			require.NoError(t, err)
			require.NoError(t, repo.RepairInvalidatedBuckets(ctx, 1))
			var verified bool
			require.NoError(t, db.QueryRowContext(ctx, `SELECT source_verified FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour).Scan(&verified))
			require.False(t, verified, "a single mismatched metric must prevent certification")
			_, err = db.ExecContext(ctx, `UPDATE usage_dashboard_hourly SET `+field+`=`+field+`-1 WHERE bucket_start=$1`, hour)
			require.NoError(t, err)
			require.NoError(t, repo.RepairInvalidatedBuckets(ctx, 1))
			require.NoError(t, db.QueryRowContext(ctx, `SELECT source_verified FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour).Scan(&verified))
			require.True(t, verified, "identical raw and legacy summary can be certified")
		})
	}
}

func TestDashboardHourlyInvalidation_RetentionFrontierCommitsWithDeletion(t *testing.T) {
	db := hourlyInvalidationDB(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	hour := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(1,$1)`, hour.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, repo.AggregateRange(ctx, hour, hour.Add(time.Hour)))
	for _, commit := range []bool{false, true} {
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		require.NoError(t, lockGroupUsageRollupState(ctx, tx))
		require.NoError(t, advanceDashboardRawRetention(ctx, tx, hour.Add(time.Hour)))
		_, err = tx.ExecContext(ctx, `SET LOCAL sub2api.dashboard_preserve_aggregates='true'; DELETE FROM usage_logs`)
		require.NoError(t, err)
		done := make(chan error, 1)
		go func() { done <- repo.AggregateRange(ctx, hour, hour.Add(time.Hour)) }()
		select {
		case err := <-done:
			t.Fatalf("publisher bypassed the uncommitted retention frontier: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		if commit {
			require.NoError(t, tx.Commit())
		} else {
			require.NoError(t, tx.Rollback())
		}
		require.NoError(t, <-done)
		var requests int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT total_requests FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour).Scan(&requests))
		require.Equal(t, 1, requests)
		var frontier time.Time
		require.NoError(t, db.QueryRowContext(ctx, `SELECT raw_retained_from FROM usage_dashboard_source_state WHERE id=1`).Scan(&frontier))
		if commit {
			require.True(t, frontier.Equal(hour.Add(time.Hour)))
		} else {
			require.True(t, frontier.Equal(time.Unix(0, 0)))
		}
	}
}

func TestDashboardHourlyInvalidation_PartitionRetentionPreservesSummaries(t *testing.T) {
	db := hourlyInvalidationDB(t, true)
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	month := truncateToMonthUTC(time.Now().UTC().AddDate(0, -4, 0))
	hour := month.Add(time.Hour)
	require.NoError(t, repo.createUsageLogsPartition(ctx, month))
	_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(1,$1)`, hour.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, repo.AggregateRange(ctx, hour, hour.Add(time.Hour)))
	require.NoError(t, dropUsageLogsPartitionWithRollupInvalidation(ctx, db, "usage_logs_"+month.Format("200601"), month))
	require.NoError(t, repo.AggregateRange(ctx, hour, hour.Add(time.Hour)))
	var requests int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT total_requests FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour).Scan(&requests))
	require.Equal(t, 1, requests)
	var frontier time.Time
	require.NoError(t, db.QueryRowContext(ctx, `SELECT raw_retained_from FROM usage_dashboard_source_state WHERE id=1`).Scan(&frontier))
	require.True(t, frontier.Equal(month.AddDate(0, 1, 0)))
}

func TestDashboardHourlyInvalidation_ArchivedReaderKeepsLastClosedSnapshot(t *testing.T) {
	db := hourlyInvalidationDB(t, false)
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	usage := &usageLogRepository{sql: db}
	hour := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(1,$1),(2,$2),(3,$3)`, hour.Add(time.Minute), hour.Add(2*time.Minute), hour.Add(3*time.Minute))
	require.NoError(t, err)
	require.NoError(t, repo.AggregateRange(ctx, hour, hour.Add(time.Hour)))
	require.NoError(t, repo.UpdateAggregationWatermark(ctx, time.Now().UTC()))
	// A non-hour-aligned frontier makes the entire first hour incomplete in raw.
	_, err = cleanupUsageLogsBatchWithRollupInvalidation(ctx, db, hour.Add(30*time.Minute))
	require.NoError(t, err)
	assertSnapshot := func() {
		t.Helper()
		trend, err := usage.getUsageTrendFromAggregates(ctx, hour, hour.Add(time.Hour), "hour")
		require.NoError(t, err)
		require.Len(t, trend, 1)
		require.EqualValues(t, 3, trend[0].Requests, "the last closed snapshot survives partial raw changes")
		require.InDelta(t, 6, trend[0].Cost, 1e-9)
	}
	assertSnapshot()
	_, err = db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(4,$1)`, hour.Add(40*time.Minute))
	require.NoError(t, err)
	assertSnapshot()
	_, err = db.ExecContext(ctx, `UPDATE usage_logs SET total_cost=100 WHERE id=4; UPDATE usage_dashboard_aggregation_watermark SET updated_at=NOW()-INTERVAL '10 minutes' WHERE id=1`)
	require.NoError(t, err)
	assertSnapshot()
	// A request for part of an archived hour cannot use a whole-hour snapshot.
	partial, err := usage.getUsageTrendFromAggregates(ctx, hour.Add(30*time.Minute), hour.Add(time.Hour), "hour")
	require.NoError(t, err)
	require.Len(t, partial, 1)
	require.EqualValues(t, 1, partial[0].Requests)
	require.InDelta(t, 100, partial[0].Cost, 1e-9)
	_, err = db.ExecContext(ctx, `DELETE FROM usage_logs WHERE id=4`)
	require.NoError(t, err)
	assertSnapshot()
	// The archived value deliberately excludes the late change: a complete
	// reconstruction requires restoring the complete original raw source.
}

func TestDashboardHourlyInvalidation_ArchivedPartialSnapshotIsNotCertified(t *testing.T) {
	db := hourlyInvalidationDB(t, false)
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	usage := &usageLogRepository{sql: db}
	hour := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(1,$1)`, hour.Add(40*time.Minute))
	require.NoError(t, err)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SET LOCAL sub2api.dashboard_verified_publish='true'`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO usage_dashboard_hourly(bucket_start,total_requests,computed_at) VALUES($1,999,$1::timestamptz+INTERVAL '10 minutes')`, hour)
	require.NoError(t, err)
	require.NoError(t, advanceDashboardRawRetention(ctx, tx, hour.Add(30*time.Minute)))
	require.NoError(t, tx.Commit())
	require.NoError(t, repo.UpdateAggregationWatermark(ctx, time.Now().UTC()))
	var finalized bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT source_finalized FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour).Scan(&finalized))
	require.False(t, finalized)
	// Invalidation must retain the fact that this publication was partial.
	_, err = db.ExecContext(ctx, `UPDATE usage_logs SET input_tokens=111 WHERE id=1`)
	require.NoError(t, err)
	trend, err := usage.getUsageTrendFromAggregates(ctx, hour, hour.Add(time.Hour), "hour")
	require.NoError(t, err)
	require.Len(t, trend, 1)
	require.EqualValues(t, 1, trend[0].Requests)
}

func TestDashboardHourlyInvalidation_DSTRepeatedHoursRemainSeparate(t *testing.T) {
	require.NoError(t, timezone.Init("America/New_York"))
	t.Cleanup(func() { require.NoError(t, timezone.Init("UTC")) })
	db := hourlyInvalidationDB(t, false)
	db.SetMaxOpenConns(1)
	_, err := db.Exec(`SET TIME ZONE 'America/New_York'`)
	require.NoError(t, err)
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	// Both intervals are locally 01:00-02:00, first EDT and then EST.
	hour := time.Date(2025, 11, 2, 5, 0, 0, 0, time.UTC)
	_, err = db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(1,$1),(2,$2),(3,$3),(4,$4)`, hour.Add(10*time.Minute), hour.Add(50*time.Minute), hour.Add(70*time.Minute), hour.Add(110*time.Minute))
	require.NoError(t, err)
	require.NoError(t, repo.AggregateRange(ctx, hour, hour.Add(time.Hour)))
	require.NoError(t, repo.AggregateRange(ctx, hour.Add(time.Hour), hour.Add(2*time.Hour)))
	for _, start := range []time.Time{hour, hour.Add(time.Hour)} {
		var requests int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT total_requests FROM usage_dashboard_hourly WHERE bucket_start=$1`, start).Scan(&requests))
		require.Equal(t, 2, requests)
	}
	_, err = db.ExecContext(ctx, `UPDATE usage_logs SET input_tokens=111 WHERE id=1`)
	require.NoError(t, err)
	for i, start := range []time.Time{hour, hour.Add(time.Hour)} {
		var dirty bool
		require.NoError(t, db.QueryRowContext(ctx, `SELECT computed_at=TIMESTAMPTZ 'epoch' FROM usage_dashboard_hourly WHERE bucket_start=$1`, start).Scan(&dirty))
		require.Equal(t, i == 0, dirty)
	}
}

func TestDashboardHourlyInvalidation_RepairQueueSkipsUnverifiableLegacyBatch(t *testing.T) {
	db := hourlyInvalidationDB(t, false)
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	hour := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	for i := 0; i < 5; i++ {
		_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES($1,$2)`, i+1, hour.Add(time.Duration(i)*time.Hour+time.Minute))
		require.NoError(t, err)
	}
	require.NoError(t, repo.AggregateRange(ctx, hour, hour.Add(5*time.Hour)))
	_, err := db.ExecContext(ctx, `UPDATE usage_dashboard_hourly SET total_requests=999 WHERE bucket_start>$1`, hour)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE usage_dashboard_hourly SET computed_at=NOW() WHERE bucket_start=$1`, hour)
	require.NoError(t, err)
	require.NoError(t, repo.RepairInvalidatedBuckets(ctx, 4))
	var verified bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT source_verified FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour).Scan(&verified))
	require.False(t, verified, "first bounded batch examines the four newer mismatches")
	require.NoError(t, repo.RepairInvalidatedBuckets(ctx, 4))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT source_verified FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour).Scan(&verified))
	require.True(t, verified, "the next batch must reach the older equal snapshot")
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_dashboard_hourly WHERE total_requests=999 AND NOT source_verified`).Scan(&count))
	require.Equal(t, 4, count, "inconsistent legacy values remain untouched")
}

func TestDashboardHourlyInvalidation_DirtyBeforeRetentionMustNotArchiveStaleCounts(t *testing.T) {
	db := hourlyInvalidationDB(t, false)
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	usage := &usageLogRepository{sql: db}
	hour := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(1,$1),(2,$2),(3,$3)`, hour.Add(time.Minute), hour.Add(2*time.Minute), hour.Add(3*time.Minute))
	require.NoError(t, err)
	require.NoError(t, repo.AggregateRange(ctx, hour, hour.Add(time.Hour)))
	require.NoError(t, repo.UpdateAggregationWatermark(ctx, time.Now().UTC()))
	_, err = db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(4,$1)`, hour.Add(4*time.Minute))
	require.NoError(t, err)
	assertFour := func() {
		t.Helper()
		trend, err := usage.getUsageTrendFromAggregates(ctx, hour, hour.Add(time.Hour), "hour")
		require.NoError(t, err)
		require.Len(t, trend, 1)
		require.EqualValues(t, 4, trend[0].Requests, "retention must not freeze a stale snapshot from before the raw mutation")
	}
	assertFour()
	affected, err := cleanupUsageLogsBatchWithRollupInvalidation(ctx, db, hour.Add(2*time.Hour))
	require.NoError(t, err)
	assertFour()
	require.Zero(t, affected, "defer deletion until the certified dirty snapshot is repaired")
	require.NoError(t, repo.RepairInvalidatedBuckets(ctx, 4))
	affected, err = cleanupUsageLogsBatchWithRollupInvalidation(ctx, db, hour.Add(2*time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 4, affected)
	assertFour()
}

func TestDashboardHourlyInvalidation_RetentionFrontierOnlyCrossesActualBatch(t *testing.T) {
	db := hourlyInvalidationDB(t, false)
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	usage := &usageLogRepository{sql: db}
	hour := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) SELECT n,$1::timestamptz+INTERVAL '1 minute' FROM generate_series(1,$2) n`, hour, usageLogsCleanupBatchSize)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(10001,$1)`, hour.Add(2*time.Hour+time.Minute))
	require.NoError(t, err)
	require.NoError(t, repo.AggregateRange(ctx, hour, hour.Add(3*time.Hour)))
	require.NoError(t, repo.UpdateAggregationWatermark(ctx, time.Now().UTC()))
	_, err = db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(10002,$1)`, hour.Add(2*time.Hour+2*time.Minute))
	require.NoError(t, err)
	affected, err := cleanupUsageLogsBatchWithRollupInvalidation(ctx, db, hour.Add(4*time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, usageLogsCleanupBatchSize, affected)
	trend, err := usage.getUsageTrendFromAggregates(ctx, hour.Add(2*time.Hour), hour.Add(3*time.Hour), "hour")
	require.NoError(t, err)
	require.Len(t, trend, 1)
	require.EqualValues(t, 2, trend[0].Requests, "a later unprocessed dirty hour must still read complete raw")
	var frontier time.Time
	require.NoError(t, db.QueryRowContext(ctx, `SELECT raw_retained_from FROM usage_dashboard_source_state WHERE id=1`).Scan(&frontier))
	require.True(t, frontier.After(hour) && !frontier.After(hour.Add(time.Hour)), frontier)
}

func TestDashboardHourlyInvalidation_RetentionChecksDirtyHoursWithoutRaw(t *testing.T) {
	db := hourlyInvalidationDB(t, false)
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	hour := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(1,$1),(2,$2)`, hour.Add(time.Minute), hour.Add(2*time.Hour+time.Minute))
	require.NoError(t, err)
	require.NoError(t, repo.AggregateRange(ctx, hour, hour.Add(3*time.Hour)))
	_, err = db.ExecContext(ctx, `DELETE FROM usage_logs WHERE id=1`)
	require.NoError(t, err)
	affected, err := cleanupUsageLogsBatchWithRollupInvalidation(ctx, db, hour.Add(4*time.Hour))
	require.NoError(t, err)
	require.Zero(t, affected, "a gap in raw is not permission to cross a dirty certified hour")
	require.NoError(t, repo.RepairInvalidatedBuckets(ctx, 4))
	affected, err = cleanupUsageLogsBatchWithRollupInvalidation(ctx, db, hour.Add(4*time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 1, affected)
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT total_requests FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour).Scan(&count))
	require.Zero(t, count)
}

func TestDashboardHourlyInvalidation_RetentionRepairsPartialButDoesNotBlockLegacy(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			db := hourlyInvalidationDB(t, false)
			ctx := context.Background()
			repo := newDashboardAggregationRepositoryWithSQL(db)
			hour := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
			_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(1,$1)`, hour.Add(time.Minute))
			require.NoError(t, err)
			tx, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)
			if partial {
				_, err = tx.ExecContext(ctx, `SET LOCAL sub2api.dashboard_verified_publish='true'`)
				require.NoError(t, err)
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO usage_dashboard_hourly(bucket_start,total_requests,computed_at) VALUES($1,999,$1::timestamptz+INTERVAL '10 minutes')`, hour)
			require.NoError(t, err)
			require.NoError(t, tx.Commit())
			affected, err := cleanupUsageLogsBatchWithRollupInvalidation(ctx, db, hour.Add(2*time.Hour))
			require.NoError(t, err)
			if partial {
				require.Zero(t, affected)
				require.NoError(t, repo.RepairInvalidatedBuckets(ctx, 4))
				affected, err = cleanupUsageLogsBatchWithRollupInvalidation(ctx, db, hour.Add(2*time.Hour))
				require.NoError(t, err)
			}
			require.EqualValues(t, 1, affected, "legacy mismatches retain the existing raw cleanup policy")
			var count int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT total_requests FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour).Scan(&count))
			if partial {
				require.Equal(t, 1, count)
			} else {
				require.Equal(t, 999, count)
			}
		})
	}
}

func TestDashboardHourlyInvalidation_RetentionDefersLockedTupleOrHour(t *testing.T) {
	for _, tuple := range []bool{false, true} {
		t.Run(fmt.Sprint(tuple), func(t *testing.T) {
			db := hourlyInvalidationDB(t, false)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			hour := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
			_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(1,$1)`, hour.Add(time.Minute))
			require.NoError(t, err)
			tx, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			if tuple {
				_, err = tx.ExecContext(ctx, `SELECT id FROM usage_logs WHERE id=1 FOR UPDATE`)
			} else {
				_, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock_shared($1::integer,$2::integer)`, dashboardHourlyLockNamespace, hour.Unix()/3600)
			}
			require.NoError(t, err)
			affected, err := cleanupUsageLogsBatchWithRollupInvalidation(ctx, db, hour.Add(time.Hour))
			require.NoError(t, err, "retention must defer instead of waiting for a writer holding a raw tuple/hour")
			require.Zero(t, affected)
			var frontier time.Time
			require.NoError(t, db.QueryRowContext(context.Background(), `SELECT raw_retained_from FROM usage_dashboard_source_state WHERE id=1`).Scan(&frontier))
			require.True(t, frontier.Equal(time.Unix(0, 0)))
		})
	}
}

func TestDashboardHourlyInvalidation_PartitionRetentionDefersDirtySnapshot(t *testing.T) {
	db := hourlyInvalidationDB(t, true)
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	month := truncateToMonthUTC(time.Now().UTC().AddDate(0, -4, 0))
	hour := month.Add(time.Hour)
	require.NoError(t, repo.createUsageLogsPartition(ctx, month))
	_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(1,$1)`, hour.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, repo.AggregateRange(ctx, hour, hour.Add(time.Hour)))
	_, err = db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(2,$1)`, hour.Add(2*time.Minute))
	require.NoError(t, err)
	require.NoError(t, repo.CleanupUsageLogs(ctx, month.AddDate(0, 1, 0)))
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs`).Scan(&count))
	require.Equal(t, 2, count, "partition DROP must defer while a certified snapshot is dirty")
	require.NoError(t, repo.RepairInvalidatedBuckets(ctx, 4))
	require.NoError(t, repo.CleanupUsageLogs(ctx, month.AddDate(0, 1, 0)))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT total_requests FROM usage_dashboard_hourly WHERE bucket_start=$1`, hour).Scan(&count))
	require.Equal(t, 2, count)
}

func TestDashboardHourlyInvalidation_RetentionDefersSourcePublisher(t *testing.T) {
	for _, partitioned := range []bool{false, true} {
		t.Run(fmt.Sprint(partitioned), func(t *testing.T) {
			db := hourlyInvalidationDB(t, partitioned)
			ctx := context.Background()
			repo := newDashboardAggregationRepositoryWithSQL(db)
			month := truncateToMonthUTC(time.Now().UTC().AddDate(0, -4, 0))
			hour := month.Add(time.Hour)
			if partitioned {
				require.NoError(t, repo.createUsageLogsPartition(ctx, month))
			}
			_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(1,$1)`, hour.Add(time.Minute))
			require.NoError(t, err)
			tx, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			_, err = tx.ExecContext(ctx, `SELECT raw_retained_from FROM usage_dashboard_source_state WHERE id=1 FOR SHARE`)
			require.NoError(t, err)
			attempt, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			if partitioned {
				err = dropUsageLogsPartitionWithRollupInvalidation(attempt, db, "usage_logs_"+month.Format("200601"), month)
				require.ErrorIs(t, err, errDashboardRetentionDeferred, "DROP must defer without waiting behind a publisher while holding group state")
			} else {
				affected, err := cleanupUsageLogsBatchWithRollupInvalidation(attempt, db, hour.Add(time.Hour))
				require.NoError(t, err, "retention must defer without waiting behind a publisher while holding group state")
				require.Zero(t, affected)
			}
			var frontier time.Time
			require.NoError(t, db.QueryRowContext(ctx, `SELECT raw_retained_from FROM usage_dashboard_source_state WHERE id=1`).Scan(&frontier))
			require.True(t, frontier.Equal(time.Unix(0, 0)))
			var count int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs`).Scan(&count))
			require.Equal(t, 1, count)
		})
	}
}

func seedCertifiedRetentionHours(t *testing.T, db *sql.DB, hour time.Time, count int) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	_, err = tx.Exec(`SET LOCAL sub2api.dashboard_verified_publish='true'`)
	require.NoError(t, err)
	_, err = tx.Exec(`INSERT INTO usage_dashboard_hourly(bucket_start,computed_at)
		SELECT $1::timestamptz+n*INTERVAL '1 hour',NOW() FROM generate_series(0,$2::integer-1) n`, hour, count)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
}

func observeRetentionLocks(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`CREATE TABLE retention_lock_observations (frontier TIMESTAMPTZ,locks INTEGER,transaction_id BIGINT);
		CREATE FUNCTION observe_retention_locks() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
			INSERT INTO retention_lock_observations SELECT NEW.raw_retained_from,COUNT(*),txid_current()
			FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory' AND classid=1937072738::oid;
			RETURN NEW;
		END; $$;
		CREATE TRIGGER observe_retention_locks BEFORE UPDATE OF raw_retained_from ON usage_dashboard_source_state
		FOR EACH ROW EXECUTE FUNCTION observe_retention_locks()`)
	require.NoError(t, err)
}

func TestDashboardHourlyInvalidation_RetentionBoundsLongHistoryAndFinishesOneCycle(t *testing.T) {
	db := hourlyInvalidationDB(t, false)
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	hour := time.Now().UTC().Truncate(time.Hour).Add(-40000 * time.Hour)
	const count = 32768
	last := hour.Add((count - 1) * time.Hour)
	_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(1,$1)`, last.Add(time.Minute))
	require.NoError(t, err)
	seedCertifiedRetentionHours(t, db, hour, count)
	require.NoError(t, repo.AggregateRange(ctx, last, last.Add(time.Hour)))
	observeRetentionLocks(t, db)
	started := time.Now()
	require.NoError(t, repo.CleanupUsageLogs(ctx, last.Add(time.Hour)))
	var raw, snapshots, peak, steps, transactions int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs`).Scan(&raw))
	require.Zero(t, raw, "32K historical hours must finish within this cleanup cycle")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT total_requests FROM usage_dashboard_hourly WHERE bucket_start=$1`, last).Scan(&snapshots))
	require.Equal(t, 1, snapshots)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT MAX(locks),COUNT(*),COUNT(DISTINCT transaction_id) FROM retention_lock_observations`).Scan(&peak, &steps, &transactions))
	require.LessOrEqual(t, peak, dashboardRetentionHourLockLimit)
	require.Equal(t, count/dashboardRetentionHourLockLimit, steps)
	require.Equal(t, steps, transactions, "each prefix commits and releases locks before the next")
	t.Logf("RETENTION_CAPACITY hours=%d peak_locks=%d transactions=%d duration_us=%d raw_remaining=%d", count, peak, steps, time.Since(started).Microseconds(), raw)
}

func TestDashboardHourlyInvalidation_RetentionBoundsProgressPerCycle(t *testing.T) {
	db := hourlyInvalidationDB(t, false)
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	hour := time.Now().UTC().Truncate(time.Hour).Add(-80000 * time.Hour)
	count := dashboardRetentionPrefixBatchLimit*dashboardRetentionHourLockLimit + 2
	last := hour.Add(time.Duration(count-1) * time.Hour)
	_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(1,$1)`, last.Add(time.Minute))
	require.NoError(t, err)
	seedCertifiedRetentionHours(t, db, hour, count)
	require.NoError(t, repo.AggregateRange(ctx, last, last.Add(time.Hour)))
	observeRetentionLocks(t, db)
	require.NoError(t, repo.CleanupUsageLogs(ctx, last.Add(time.Hour)))
	var raw, peak, steps int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs`).Scan(&raw))
	require.Equal(t, 1, raw, "the 64-prefix budget must stop without crossing remaining raw")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT MAX(locks),COUNT(*) FROM retention_lock_observations`).Scan(&peak, &steps))
	require.Equal(t, dashboardRetentionPrefixBatchLimit, steps)
	require.LessOrEqual(t, peak, dashboardRetentionHourLockLimit)
	var frontier time.Time
	require.NoError(t, db.QueryRowContext(ctx, `SELECT raw_retained_from FROM usage_dashboard_source_state WHERE id=1`).Scan(&frontier))
	require.True(t, frontier.Equal(hour.Add(time.Duration(dashboardRetentionPrefixBatchLimit*dashboardRetentionHourLockLimit)*time.Hour)))
	require.NoError(t, repo.CleanupUsageLogs(ctx, last.Add(time.Hour)))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs`).Scan(&raw))
	require.Zero(t, raw, "the next cycle resumes the committed frontier")
}

func TestDashboardHourlyInvalidation_RetentionPrefixDoesNotCrossLaterDirtyHour(t *testing.T) {
	db := hourlyInvalidationDB(t, false)
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	hour := time.Now().UTC().Truncate(time.Hour).Add(-4000 * time.Hour)
	last := hour.Add((dashboardRetentionHourLockLimit + 2) * time.Hour)
	dirty := last.Add(-time.Hour)
	_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(1,$1)`, last.Add(time.Minute))
	require.NoError(t, err)
	seedCertifiedRetentionHours(t, db, hour, dashboardRetentionHourLockLimit+3)
	require.NoError(t, repo.AggregateRange(ctx, last, last.Add(time.Hour)))
	_, err = db.ExecContext(ctx, `UPDATE usage_dashboard_hourly SET computed_at=TIMESTAMPTZ 'epoch' WHERE bucket_start=$1`, dirty)
	require.NoError(t, err)
	affected, err := cleanupUsageLogsBatchWithRollupInvalidation(ctx, db, last.Add(time.Hour))
	require.NoError(t, err)
	require.Zero(t, affected, "a safe gap-only prefix reports its literal zero deletions")
	var frontier time.Time
	require.NoError(t, db.QueryRowContext(ctx, `SELECT raw_retained_from FROM usage_dashboard_source_state WHERE id=1`).Scan(&frontier))
	require.True(t, frontier.Equal(hour.Add(dashboardRetentionHourLockLimit*time.Hour)))
	affected, err = cleanupUsageLogsBatchWithRollupInvalidation(ctx, db, last.Add(time.Hour))
	require.NoError(t, err)
	require.Zero(t, affected)
	var blockedFrontier time.Time
	require.NoError(t, db.QueryRowContext(ctx, `SELECT raw_retained_from FROM usage_dashboard_source_state WHERE id=1`).Scan(&blockedFrontier))
	require.True(t, blockedFrontier.Equal(frontier), "the next prefix must stop at the dirty certified hour")
	require.NoError(t, repo.RepairInvalidatedBuckets(ctx, 4))
	affected, err = cleanupUsageLogsBatchWithRollupInvalidation(ctx, db, last.Add(time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 1, affected)
}

func TestDashboardHourlyInvalidation_RetentionBoundsDistinctRawHours(t *testing.T) {
	db := hourlyInvalidationDB(t, false)
	ctx := context.Background()
	hour := time.Now().UTC().Truncate(time.Hour).Add(-4000 * time.Hour)
	count := dashboardRetentionHourLockLimit + 20
	_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) SELECT n+1,$1::timestamptz+n*INTERVAL '1 hour'+INTERVAL '1 minute' FROM generate_series(0,$2::integer-1) n`, hour, count)
	require.NoError(t, err)
	observeRetentionLocks(t, db)
	affected, err := cleanupUsageLogsBatchWithRollupInvalidation(ctx, db, hour.Add(time.Duration(count)*time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, dashboardRetentionHourLockLimit, affected)
	var frontier, earliestRemaining time.Time
	require.NoError(t, db.QueryRowContext(ctx, `SELECT raw_retained_from FROM usage_dashboard_source_state WHERE id=1`).Scan(&frontier))
	require.True(t, frontier.Equal(hour.Add((dashboardRetentionHourLockLimit-1)*time.Hour+time.Minute+time.Microsecond)), "only advance through actual deleted timestamps")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT MIN(created_at) FROM usage_logs`).Scan(&earliestRemaining))
	require.True(t, earliestRemaining.Equal(hour.Add(dashboardRetentionHourLockLimit*time.Hour+time.Minute)))
	var peak int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT MAX(locks) FROM retention_lock_observations`).Scan(&peak))
	require.Equal(t, dashboardRetentionHourLockLimit, peak)
}

func TestDashboardHourlyInvalidation_PartitionRetentionUsesBoundedGapFallback(t *testing.T) {
	db := hourlyInvalidationDB(t, true)
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	month := truncateToMonthUTC(time.Now().UTC().AddDate(0, -4, 0))
	hour := month.Add(-(dashboardRetentionHourLockLimit + 50) * time.Hour)
	last := month.Add(time.Hour)
	require.NoError(t, repo.createUsageLogsPartition(ctx, month))
	_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(1,$1)`, last.Add(time.Minute))
	require.NoError(t, err)
	seedCertifiedRetentionHours(t, db, hour, dashboardRetentionHourLockLimit+52)
	require.NoError(t, repo.AggregateRange(ctx, last, last.Add(time.Hour)))
	observeRetentionLocks(t, db)
	name := "usage_logs_" + month.Format("200601")
	err = dropUsageLogsPartitionWithRollupInvalidation(ctx, db, name, month)
	require.ErrorIs(t, err, errDashboardRetentionCapacity)
	var frontier time.Time
	require.NoError(t, db.QueryRowContext(ctx, `SELECT raw_retained_from FROM usage_dashboard_source_state WHERE id=1`).Scan(&frontier))
	require.True(t, frontier.Equal(time.Unix(0, 0)), "oversized DROP must roll back before moving the frontier")
	require.NoError(t, repo.CleanupUsageLogs(ctx, month.AddDate(0, 1, 0)))
	var raw, peak int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs`).Scan(&raw))
	require.Zero(t, raw, "bounded batch fallback must make progress in this cleanup call")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT MAX(locks) FROM retention_lock_observations`).Scan(&peak))
	require.LessOrEqual(t, peak, dashboardRetentionHourLockLimit)
	require.NoError(t, repo.CleanupUsageLogs(ctx, month.AddDate(0, 1, 0)))
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists))
	require.False(t, exists, "a subsequent cycle can drop the guarded month")
}

func TestDashboardHourlyInvalidation_RetentionPrefixDefersConcurrentRowMove(t *testing.T) {
	db := hourlyInvalidationDB(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	hour := time.Now().UTC().Truncate(time.Hour).Add(-4000 * time.Hour)
	last := hour.Add((dashboardRetentionHourLockLimit + 9) * time.Hour)
	future := last.Add(20 * time.Hour)
	destination := hour.Add(10 * time.Hour)
	_, err := db.ExecContext(ctx, `INSERT INTO usage_logs(id,created_at) VALUES(1,$1),(2,$2)`, last.Add(time.Minute), future.Add(time.Minute))
	require.NoError(t, err)
	seedCertifiedRetentionHours(t, db, hour, dashboardRetentionHourLockLimit+10)
	require.NoError(t, repo.AggregateRange(ctx, last, last.Add(time.Hour)))
	writer, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = writer.Rollback() }()
	_, err = writer.ExecContext(ctx, `UPDATE usage_logs SET created_at=$1 WHERE id=2`, destination.Add(time.Minute))
	require.NoError(t, err)
	affected, err := cleanupUsageLogsBatchWithRollupInvalidation(ctx, db, last.Add(time.Hour))
	require.NoError(t, err)
	require.Zero(t, affected, "the writer moved from outside the raw selection into a certified gap; its hour SHARE must block the prefix")
	var frontier time.Time
	require.NoError(t, db.QueryRowContext(ctx, `SELECT raw_retained_from FROM usage_dashboard_source_state WHERE id=1`).Scan(&frontier))
	require.True(t, frontier.Equal(time.Unix(0, 0)))
	require.NoError(t, writer.Commit())
	require.NoError(t, repo.RepairInvalidatedBuckets(ctx, 4))
	require.NoError(t, repo.CleanupUsageLogs(ctx, last.Add(time.Hour)))
	var raw, requests int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs`).Scan(&raw))
	require.Zero(t, raw)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT total_requests FROM usage_dashboard_hourly WHERE bucket_start=$1`, destination).Scan(&requests))
	require.Equal(t, 1, requests, "the committed pre-retention move must be certified before archival")
}

func TestDashboardHourlyInvalidation_EmptyPartitionRetentionUsesBoundedGapFallback(t *testing.T) {
	db := hourlyInvalidationDB(t, true)
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(db)
	month := truncateToMonthUTC(time.Now().UTC().AddDate(0, -4, 0))
	hour := month.Add(-(dashboardRetentionHourLockLimit + 50) * time.Hour)
	require.NoError(t, repo.createUsageLogsPartition(ctx, month))
	seedCertifiedRetentionHours(t, db, hour, dashboardRetentionHourLockLimit+52)
	observeRetentionLocks(t, db)
	cutoff := month.AddDate(0, 1, 0)
	require.NoError(t, repo.CleanupUsageLogs(ctx, cutoff))
	var frontier time.Time
	require.NoError(t, db.QueryRowContext(ctx, `SELECT raw_retained_from FROM usage_dashboard_source_state WHERE id=1`).Scan(&frontier))
	require.True(t, frontier.Equal(hour.Add(dashboardRetentionHourLockLimit*time.Hour)), "an oversized empty gap must make guarded progress instead of deferring forever")
	require.NoError(t, repo.CleanupUsageLogs(ctx, cutoff))
	var exists bool
	name := "usage_logs_" + month.Format("200601")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists))
	require.False(t, exists)
	var peak int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT MAX(locks) FROM retention_lock_observations`).Scan(&peak))
	require.LessOrEqual(t, peak, dashboardRetentionHourLockLimit)
}

func TestDashboardHourlyInvalidation_EmptyRetentionPrefixKeepsSafetyGuards(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			db := hourlyInvalidationDB(t, false)
			ctx := context.Background()
			repo := newDashboardAggregationRepositoryWithSQL(db)
			hour := time.Now().UTC().Truncate(time.Hour).Add(-4000 * time.Hour)
			seedCertifiedRetentionHours(t, db, hour, dashboardRetentionHourLockLimit+5)
			tx, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)
			if partial {
				_, err = tx.ExecContext(ctx, `SET LOCAL sub2api.dashboard_verified_publish='true'`)
				require.NoError(t, err)
				_, err = tx.ExecContext(ctx, `UPDATE usage_dashboard_hourly SET computed_at=bucket_start+INTERVAL '10 minutes' WHERE bucket_start=$1`, hour)
			} else {
				_, err = tx.ExecContext(ctx, `UPDATE usage_dashboard_hourly SET computed_at=TIMESTAMPTZ 'epoch' WHERE bucket_start=$1`, hour)
			}
			require.NoError(t, err)
			require.NoError(t, tx.Commit())
			cutoff := hour.Add((dashboardRetentionHourLockLimit + 5) * time.Hour)
			affected, err := cleanupUsageLogsBatchWithRollupInvalidation(ctx, db, cutoff)
			require.NoError(t, err)
			require.Zero(t, affected)
			var frontier time.Time
			require.NoError(t, db.QueryRowContext(ctx, `SELECT raw_retained_from FROM usage_dashboard_source_state WHERE id=1`).Scan(&frontier))
			require.True(t, frontier.Equal(time.Unix(0, 0)), "empty raw does not permit crossing a certified dirty/partial snapshot")
			require.NoError(t, repo.RepairInvalidatedBuckets(ctx, 4))
			affected, err = cleanupUsageLogsBatchWithRollupInvalidation(ctx, db, cutoff)
			require.NoError(t, err)
			require.Zero(t, affected)
			require.NoError(t, db.QueryRowContext(ctx, `SELECT raw_retained_from FROM usage_dashboard_source_state WHERE id=1`).Scan(&frontier))
			require.True(t, frontier.Equal(hour.Add(dashboardRetentionHourLockLimit*time.Hour)))
		})
	}
}

func TestDashboardHourlyInvalidation_EmptyRetentionWithinBudgetRemainsNoop(t *testing.T) {
	db := hourlyInvalidationDB(t, false)
	ctx := context.Background()
	hour := time.Now().UTC().Truncate(time.Hour).Add(-4000 * time.Hour)
	seedCertifiedRetentionHours(t, db, hour, 100)
	affected, err := cleanupUsageLogsBatchWithRollupInvalidation(ctx, db, hour.Add(100*time.Hour))
	require.NoError(t, err)
	require.Zero(t, affected)
	var frontier time.Time
	require.NoError(t, db.QueryRowContext(ctx, `SELECT raw_retained_from FROM usage_dashboard_source_state WHERE id=1`).Scan(&frontier))
	require.True(t, frontier.Equal(time.Unix(0, 0)), "an empty request within budget must not broaden the source frontier")
}
