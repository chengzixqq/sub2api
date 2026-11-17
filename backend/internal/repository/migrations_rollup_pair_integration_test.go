//go:build integration

package repository

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigrationsRunner_RollupPairFailurePreservesCustom258(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	f := newOptimisticGroupRollupFixtureWithMigrations(t, ctx, false, []string{"258_group_usage_rollup_optimistic.sql"})
	db := openGroupUsageRollupSchemaDB(t, f.schema, "UTC")
	_, err := db.ExecContext(ctx, schemaMigrationsTableDDL+atlasSchemaRevisionsTableDDL)
	require.NoError(t, err)
	upstream, err := migrations.FS.ReadFile(groupUsageRollupUpstreamMigration)
	require.NoError(t, err)
	compat, err := migrations.FS.ReadFile(groupUsageRollupCompatibilityMigration)
	require.NoError(t, err)
	fsys := fstest.MapFS{
		groupUsageRollupUpstreamMigration:      &fstest.MapFile{Data: upstream},
		groupUsageRollupCompatibilityMigration: &fstest.MapFile{Data: append(append([]byte(nil), compat...), []byte("\nDO $$ BEGIN RAISE EXCEPTION 'injected paired migration failure'; END $$;")...)},
	}
	var before string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT pg_get_functiondef('invalidate_group_usage_rollup_state()'::regprocedure)").Scan(&before))
	err = applyMigrationsFS(ctx, db, fsys)
	require.ErrorContains(t, err, "injected paired migration failure")
	var after string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT pg_get_functiondef('invalidate_group_usage_rollup_state()'::regprocedure)").Scan(&after))
	require.Equal(t, before, after, "the old running service must retain its full source_revision protocol when 262 fails")
	var hasExpired bool
	require.NoError(t, db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=$1 AND table_name='usage_group_rollup_state' AND column_name='expired_before')", f.schema).Scan(&hasExpired))
	require.False(t, hasExpired, "251 DDL must roll back with 262")
	var recorded int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&recorded))
	require.Zero(t, recorded, "neither migration ledger row may survive the failed pair")
	fsys[groupUsageRollupCompatibilityMigration] = &fstest.MapFile{Data: compat}
	require.NoError(t, applyMigrationsFS(ctx, db, fsys))
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&recorded))
	require.Equal(t, 2, recorded)
	require.NoError(t, db.QueryRowContext(ctx, "SELECT pg_get_functiondef('invalidate_group_usage_rollup_state()'::regprocedure)").Scan(&after))
	require.True(t, strings.Contains(after, "scan_before"))
	require.NoError(t, applyMigrationsFS(ctx, db, fsys), "successfully paired migration is idempotent")
}
