package repository

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"testing/fstest"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestApplyMigrationsFS_RollupReplacementCommitsAtomically(t *testing.T) {
	for _, applied := range []int{0, 1, 2} {
		t.Run([]string{"fresh", "custom258", "upstream251_and_custom258"}[applied], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			db.SetMaxOpenConns(1)
			prepareMigrationsBootstrapExpectations(mock)
			const original = "258_group_usage_rollup_optimistic.sql"
			contents := map[string]string{groupUsageRollupUpstreamMigration: "SELECT 'upstream protocol';", original: "SELECT 'optimistic protocol';", groupUsageRollupCompatibilityMigration: "SELECT 'compatible protocol';"}
			fsys := fstest.MapFS{}
			for name, content := range contents {
				fsys[name] = &fstest.MapFile{Data: []byte(content)}
			}
			expectCheck := func(name string, done bool) {
				q := mock.ExpectQuery(regexp.QuoteMeta("SELECT checksum FROM schema_migrations WHERE filename = $1")).WithArgs(name)
				if done {
					q.WillReturnRows(sqlmock.NewRows([]string{"checksum"}).AddRow(calculateMigrationChecksum(contents[name])))
				} else {
					q.WillReturnError(sql.ErrNoRows)
				}
			}
			expectExec := func(name string) {
				mock.ExpectExec(regexp.QuoteMeta(contents[name])).WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectExec(regexp.QuoteMeta("INSERT INTO schema_migrations (filename, checksum) VALUES ($1, $2)")).WithArgs(name, calculateMigrationChecksum(contents[name])).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			expectCheck(groupUsageRollupUpstreamMigration, applied == 2)
			expectCheck(original, applied > 0)
			if applied == 0 {
				mock.ExpectBegin()
				expectExec(original)
				mock.ExpectCommit()
			}
			expectCheck(groupUsageRollupCompatibilityMigration, false)
			mock.ExpectBegin()
			if applied < 2 {
				mock.ExpectExec(regexp.QuoteMeta(groupUsageRollupMigrationSourceGuard)).WillReturnResult(sqlmock.NewResult(0, 0))
				expectExec(groupUsageRollupUpstreamMigration)
			}
			expectExec(groupUsageRollupCompatibilityMigration)
			mock.ExpectCommit()
			expectRollupMigrationUnlock(mock)
			require.NoError(t, applyMigrationsFS(context.Background(), db, fsys))
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestApplyMigrationsFS_RollupPairFailureRollsBack(t *testing.T) {
	for _, failure := range []string{"source_guard", "upstream", "upstream_ledger", "compatibility", "compatibility_ledger", "commit"} {
		t.Run(failure, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			prepareMigrationsBootstrapExpectations(mock)
			const upstream, compat = "SELECT 'upstream';", "SELECT 'compatibility';"
			fsys := fstest.MapFS{groupUsageRollupUpstreamMigration: &fstest.MapFile{Data: []byte(upstream)}, groupUsageRollupCompatibilityMigration: &fstest.MapFile{Data: []byte(compat)}}
			for _, name := range []string{groupUsageRollupUpstreamMigration, groupUsageRollupCompatibilityMigration} {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT checksum FROM schema_migrations WHERE filename = $1")).WithArgs(name).WillReturnError(sql.ErrNoRows)
			}
			mock.ExpectBegin()
			boom := errors.New("injected rollup migration failure")
			steps := []struct {
				name, query string
				args        []any
			}{
				{"source_guard", groupUsageRollupMigrationSourceGuard, nil},
				{"upstream", upstream, nil},
				{"upstream_ledger", "INSERT INTO schema_migrations (filename, checksum) VALUES ($1, $2)", []any{groupUsageRollupUpstreamMigration, calculateMigrationChecksum(upstream)}},
				{"compatibility", compat, nil},
				{"compatibility_ledger", "INSERT INTO schema_migrations (filename, checksum) VALUES ($1, $2)", []any{groupUsageRollupCompatibilityMigration, calculateMigrationChecksum(compat)}},
			}
			for _, step := range steps {
				e := mock.ExpectExec(regexp.QuoteMeta(step.query))
				if len(step.args) > 0 {
					e.WithArgs(step.args[0], step.args[1])
				}
				if step.name == failure {
					e.WillReturnError(boom)
					break
				}
				e.WillReturnResult(sqlmock.NewResult(0, 1))
			}
			if failure == "commit" {
				mock.ExpectCommit().WillReturnError(boom)
			} else {
				mock.ExpectRollback()
			}
			expectRollupMigrationUnlock(mock)
			require.ErrorIs(t, applyMigrationsFS(context.Background(), db, fsys), boom)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestApplyMigrationsFS_RollupPairRejectsInvalidCompanion(t *testing.T) {
	for _, alreadyApplied := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "already_applied_without_upstream"}[alreadyApplied], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			prepareMigrationsBootstrapExpectations(mock)
			mock.ExpectQuery(regexp.QuoteMeta("SELECT checksum FROM schema_migrations WHERE filename = $1")).WithArgs(groupUsageRollupUpstreamMigration).WillReturnError(sql.ErrNoRows)
			compat := ""
			if alreadyApplied {
				compat = "SELECT 'compat';"
				mock.ExpectQuery(regexp.QuoteMeta("SELECT checksum FROM schema_migrations WHERE filename = $1")).WithArgs(groupUsageRollupCompatibilityMigration).WillReturnRows(sqlmock.NewRows([]string{"checksum"}).AddRow(calculateMigrationChecksum(compat)))
			}
			expectRollupMigrationUnlock(mock)
			fsys := fstest.MapFS{groupUsageRollupUpstreamMigration: &fstest.MapFile{Data: []byte("SELECT 'upstream';")}, groupUsageRollupCompatibilityMigration: &fstest.MapFile{Data: []byte(compat)}}
			require.Error(t, applyMigrationsFS(context.Background(), db, fsys))
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func expectRollupMigrationUnlock(mock sqlmock.Sqlmock) {
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).WithArgs(migrationsAdvisoryLockID).WillReturnResult(sqlmock.NewResult(0, 1))
}
