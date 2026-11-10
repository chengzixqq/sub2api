//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestGroupUsageRollupMaintenance_SlowRetentionRollsBackAndReleasesIngest(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f := newOptimisticGroupRollupFixture(t, ctx, false)
	observer := f.conn(t, ctx)
	at := f.today.Add(-12 * time.Hour)
	f.seed(t, ctx, observer, 1, at, "1.25")
	_, err := observer.ExecContext(ctx, `
		CREATE TABLE usage_dashboard_source_state(id smallint PRIMARY KEY,raw_retained_from timestamptz NOT NULL);
		INSERT INTO usage_dashboard_source_state VALUES(1,'1970-01-01');
		CREATE TABLE usage_dashboard_hourly(bucket_start timestamptz,source_verified boolean,source_finalized boolean,computed_at timestamptz);
		CREATE FUNCTION slow_retention_delete() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_sleep(3); RETURN OLD; END; $$;
		CREATE TRIGGER slow_retention_delete BEFORE DELETE ON usage_logs FOR EACH ROW EXECUTE FUNCTION slow_retention_delete();
		UPDATE usage_group_rollup_state SET closed_before='2026-11-01' WHERE id=1;
	`)
	require.NoError(t, err)
	var revision int64
	require.NoError(t, observer.QueryRowContext(ctx, `SELECT source_revision FROM usage_group_rollup_state WHERE id=1`).Scan(&revision))
	db := groupUsageBudgetTestDB(t, f.schema)
	type result struct {
		deleted int64
		err     error
	}
	done := make(chan result, 1)
	started := time.Now()
	go func() {
		deleted, cleanupErr := cleanupUsageLogsBatchWithRollupInvalidation(ctx, db, f.today)
		done <- result{deleted, cleanupErr}
	}()
	require.Eventually(t, func() bool {
		var sleeping bool
		return observer.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
			WHERE pid<>pg_backend_pid() AND wait_event='PgSleep' AND query LIKE 'DELETE FROM usage_logs WHERE%')`).Scan(&sleeping) == nil && sleeping
	}, 2*time.Second, 10*time.Millisecond, "real retention DELETE must reach the slow trigger")
	writer := f.conn(t, ctx)
	writeCtx, stopWrite := context.WithTimeout(ctx, 2*time.Second)
	_, err = writer.ExecContext(writeCtx, `INSERT INTO usage_logs(id,user_id,group_id,actual_cost,created_at) VALUES(2,1,10,2,$1)`, f.today.Add(time.Hour))
	stopWrite()
	require.NoError(t, err, "the timed-out retention transaction must release the global gate")
	resultValue := <-done
	require.Error(t, resultValue.err)
	require.Zero(t, resultValue.deleted)
	require.Less(t, time.Since(started), 2500*time.Millisecond)
	var count int
	var closed string
	var afterRevision int64
	var retained time.Time
	require.NoError(t, observer.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs WHERE id=1`).Scan(&count))
	require.Equal(t, 1, count, "the source DELETE must roll back")
	require.NoError(t, observer.QueryRowContext(ctx, `SELECT closed_before::text,source_revision FROM usage_group_rollup_state WHERE id=1`).Scan(&closed, &afterRevision))
	require.Equal(t, "2026-11-01", closed)
	require.Equal(t, revision, afterRevision)
	require.NoError(t, observer.QueryRowContext(ctx, `SELECT raw_retained_from FROM usage_dashboard_source_state WHERE id=1`).Scan(&retained))
	require.Equal(t, time.Unix(0, 0).UTC(), retained.UTC(), "retention frontier written before DELETE must roll back too")
}

func TestGroupUsageRollupMaintenance_BusyMigrationAbortsBeforeStateDDL(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "UTC")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f := newOptimisticGroupRollupFixture(t, ctx, false)
	writer, migrator := f.conn(t, ctx), f.conn(t, ctx)
	writeTx, err := writer.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = writeTx.Rollback() }()
	_, err = writeTx.ExecContext(ctx, `INSERT INTO usage_logs(id,user_id,group_id,actual_cost,created_at) VALUES(1,1,10,1.25,$1)`, f.today.Add(time.Hour))
	require.NoError(t, err)
	body, err := migrations.FS.ReadFile("258_group_usage_rollup_optimistic.sql")
	require.NoError(t, err)
	migrationTx, err := migrator.BeginTx(ctx, nil)
	require.NoError(t, err)
	started := time.Now()
	_, err = migrationTx.ExecContext(ctx, string(body))
	var pgErr *pq.Error
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, pq.ErrorCode("55P03"), pgErr.Code, "busy source must cause a short lock timeout, not a deadlock")
	require.Less(t, time.Since(started), time.Second)
	require.NoError(t, migrationTx.Rollback())
	var revision int64
	require.NoError(t, writeTx.QueryRowContext(ctx, `SELECT source_revision FROM usage_group_rollup_state WHERE id=1`).Scan(&revision))
	require.NoError(t, writeTx.Commit())
	retryTx, err := migrator.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = retryTx.Rollback() }()
	_, err = retryTx.ExecContext(ctx, string(body))
	require.NoError(t, err, "identical migration must be retryable once the writer commits")
	require.NoError(t, retryTx.Commit())
	var afterRevision int64
	require.NoError(t, migrator.QueryRowContext(ctx, `SELECT source_revision FROM usage_group_rollup_state WHERE id=1`).Scan(&afterRevision))
	require.Equal(t, revision, afterRevision)
}
