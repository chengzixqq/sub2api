//go:build integration

package repository

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupUsageCleanupBudget_SlowDeleteRollsBackAndReleasesIngest(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f := newOptimisticGroupRollupFixture(t, ctx, false)
	setup, writer := f.conn(t, ctx), f.conn(t, ctx)
	at := f.today.Add(-12 * time.Hour)
	f.seed(t, ctx, setup, 1, at, "1.25")
	_, err := setup.ExecContext(ctx, `UPDATE usage_group_rollup_state SET closed_before=$1::date,scan_before=$2 WHERE id=1`, f.today.Format("2006-01-02"), f.today)
	require.NoError(t, err)
	var initialRevision int64
	require.NoError(t, setup.QueryRowContext(ctx, `SELECT source_revision FROM usage_group_rollup_state WHERE id=1`).Scan(&initialRevision))
	_, err = setup.ExecContext(ctx, `CREATE FUNCTION slow_cleanup_budget_delete() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_sleep(3); RETURN OLD; END; $$;
		CREATE TRIGGER slow_cleanup_budget_delete BEFORE DELETE ON usage_logs FOR EACH ROW EXECUTE FUNCTION slow_cleanup_budget_delete()`)
	require.NoError(t, err)
	pool := groupUsageBudgetTestDB(t, f.schema)
	repo := &usageCleanupRepository{sql: pool}
	type cleanupResult struct {
		deleted int64
		err     error
		elapsed time.Duration
	}
	done := make(chan cleanupResult, 1)
	go func() {
		started := time.Now()
		deleted, cleanupErr := repo.DeleteUsageLogsBatch(ctx, service.UsageCleanupFilters{
			StartTime: f.today.AddDate(0, 0, -1), EndTime: f.today,
		}, 5)
		done <- cleanupResult{deleted, cleanupErr, time.Since(started)}
	}()
	require.Eventually(t, func() bool {
		var sleeping bool
		return setup.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
			WHERE application_name=$1 AND wait_event='PgSleep' AND query LIKE '%DELETE FROM usage_logs%')`, "cleanup_budget_"+f.schema).Scan(&sleeping) == nil && sleeping
	}, 2*time.Second, 10*time.Millisecond, "actual cleanup DELETE must reach the slow row trigger while holding the group gate")
	writeCtx, writeCancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	writeStarted := time.Now()
	_, err = writer.ExecContext(writeCtx, `INSERT INTO usage_logs(id,user_id,group_id,actual_cost,created_at) VALUES(2,1,10,2,$1)`, f.today.Add(time.Hour))
	writeElapsed := time.Since(writeStarted)
	writeCancel()
	require.NoError(t, err, "cleanup timeout must release the group gate before ingest's deadline")
	result := <-done
	require.Error(t, result.err, "slow maintenance DELETE must hit its bounded statement/transaction budget")
	require.Zero(t, result.deleted, "rolled-back rows must not be counted as committed progress")
	require.Less(t, result.elapsed, 2500*time.Millisecond)
	require.Less(t, writeElapsed, 2500*time.Millisecond)
	var oldRows, newRows int
	var closed string
	var revision int64
	require.NoError(t, setup.QueryRowContext(ctx, `SELECT COUNT(*) FILTER(WHERE id=1),COUNT(*) FILTER(WHERE id=2) FROM usage_logs`).Scan(&oldRows, &newRows))
	require.Equal(t, 1, oldRows, "timeout must preserve the historical row")
	require.Equal(t, 1, newRows)
	require.NoError(t, setup.QueryRowContext(ctx, `SELECT closed_before::text,source_revision FROM usage_group_rollup_state WHERE id=1`).Scan(&closed, &revision))
	require.Equal(t, f.today.Format("2006-01-02"), closed)
	require.Equal(t, initialRevision, revision, "failed deletion must not publish invalidation metadata")
}

func TestGroupUsageCleanupBudget_InFlightWriterDefersCleanup(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f := newOptimisticGroupRollupFixture(t, ctx, false)
	setup, writer := f.conn(t, ctx), f.conn(t, ctx)
	f.seed(t, ctx, setup, 1, f.today.Add(-time.Hour), "1.25")
	pool := groupUsageBudgetTestDB(t, f.schema)
	tx, err := writer.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO usage_logs(id,user_id,group_id,actual_cost,created_at) VALUES(2,1,10,2,$1)`, f.today.Add(time.Hour))
	require.NoError(t, err)
	started := time.Now()
	deleted, err := (&usageCleanupRepository{sql: pool}).DeleteUsageLogsBatch(ctx, service.UsageCleanupFilters{
		StartTime: f.today.AddDate(0, 0, -1), EndTime: f.today,
	}, 5)
	require.Error(t, err)
	require.Zero(t, deleted)
	require.Less(t, time.Since(started), time.Second, "cleanup gate wait must use the 250ms maintenance lock budget")
	require.NoError(t, tx.Commit(), "maintenance failure must not abort the original writer")
	var count int
	require.NoError(t, setup.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs`).Scan(&count))
	require.Equal(t, 2, count)
}

// The production cleanup entry point accepts *sql.DB. A separate pool with a
// startup search_path keeps every pooled connection inside this test's schema.
func groupUsageBudgetTestDB(t *testing.T, schema string) *sql.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	dsn := strings.TrimSpace(os.Getenv("SUB2API_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("cleanup pool isolation requires SUB2API_TEST_POSTGRES_DSN from the loopback integration harness")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Equal(t, "postgres", u.Scheme)
	require.Contains(t, []string{"127.0.0.1", "::1"}, u.Hostname())
	require.True(t, strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), "sub2api_") && strings.HasSuffix(u.Path, "_test"))
	query := u.Query()
	query.Set("search_path", schema)
	query.Set("TimeZone", "UTC")
	query.Set("application_name", "cleanup_budget_"+schema)
	u.RawQuery = query.Encode()
	pool, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	pool.SetMaxOpenConns(2)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	require.NoError(t, pool.PingContext(ctx))
	return pool
}
