package repository

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestCheckMigrationPreflightFS_AllowsUnappliedSuffixAndCompatibleChecksum(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	fsys := fstest.MapFS{
		"001_init.sql":    &fstest.MapFile{Data: []byte(" SELECT 1;\n")},
		"002_compat.sql":  &fstest.MapFile{Data: []byte("SELECT 2;")},
		"003_pending.sql": &fstest.MapFile{Data: []byte("SELECT 3;")},
	}
	exact := migrationChecksum("SELECT 1;")
	current := migrationChecksum("SELECT 2;")
	legacy := "legacy-compatible-checksum"

	previousRule, hadRule := migrationChecksumCompatibilityRules["002_compat.sql"]
	migrationChecksumCompatibilityRules["002_compat.sql"] = newMigrationChecksumCompatibilityRule(current, legacy)
	t.Cleanup(func() {
		if hadRule {
			migrationChecksumCompatibilityRules["002_compat.sql"] = previousRule
			return
		}
		delete(migrationChecksumCompatibilityRules, "002_compat.sql")
	})

	mock.ExpectQuery(`SELECT filename, checksum FROM schema_migrations ORDER BY filename`).
		WillReturnRows(sqlmock.NewRows([]string{"filename", "checksum"}).
			AddRow("001_init.sql", exact).
			AddRow("002_compat.sql", legacy))

	report, err := checkMigrationPreflightFS(context.Background(), db, fsys)
	require.NoError(t, err)
	require.True(t, report.Valid())
	require.Equal(t, 3, report.EmbeddedCount)
	require.Equal(t, 2, report.AppliedCount)
	require.Equal(t, []string{"003_pending.sql"}, report.Pending)
	require.Empty(t, report.Missing)
	require.Empty(t, report.Extra)
	require.Empty(t, report.ChecksumMismatches)
	require.Equal(t, []MigrationChecksumResult{
		{
			Filename:         "001_init.sql",
			DatabaseChecksum: exact,
			FileChecksum:     exact,
			Status:           MigrationChecksumExact,
		},
		{
			Filename:         "002_compat.sql",
			DatabaseChecksum: legacy,
			FileChecksum:     current,
			Status:           MigrationChecksumCompatible,
		},
	}, report.Checksums)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCheckMigrationPreflightFS_ReportsExtraMismatchAndSequenceGap(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	fsys := fstest.MapFS{
		"001_init.sql":    &fstest.MapFile{Data: []byte("SELECT 1;")},
		"002_missing.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
		"003_later.sql":   &fstest.MapFile{Data: []byte("SELECT 3;")},
		"004_pending.sql": &fstest.MapFile{Data: []byte("SELECT 4;")},
	}
	exact := migrationChecksum("SELECT 1;")
	fileChecksum := migrationChecksum("SELECT 3;")

	mock.ExpectQuery(`SELECT filename, checksum FROM schema_migrations ORDER BY filename`).
		WillReturnRows(sqlmock.NewRows([]string{"filename", "checksum"}).
			AddRow("001_init.sql", exact).
			AddRow("003_later.sql", "unexpected-checksum").
			AddRow("999_removed.sql", "orphan-checksum"))

	report, err := checkMigrationPreflightFS(context.Background(), db, fsys)
	require.NoError(t, err)
	require.False(t, report.Valid())
	require.Equal(t, []string{"002_missing.sql"}, report.Missing)
	require.Equal(t, []string{"004_pending.sql"}, report.Pending)
	require.Equal(t, []AppliedMigration{{Filename: "999_removed.sql", Checksum: "orphan-checksum"}}, report.Extra)
	require.Equal(t, []MigrationChecksumResult{{
		Filename:         "003_later.sql",
		DatabaseChecksum: "unexpected-checksum",
		FileChecksum:     fileChecksum,
		Status:           MigrationChecksumMismatch,
	}}, report.ChecksumMismatches)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCheckMigrationPreflightFS_EmptyDatabaseHasOnlyPendingSuffix(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(`SELECT filename, checksum FROM schema_migrations ORDER BY filename`).
		WillReturnRows(sqlmock.NewRows([]string{"filename", "checksum"}))

	report, err := checkMigrationPreflightFS(context.Background(), db, fstest.MapFS{
		"001_init.sql":  &fstest.MapFile{Data: []byte("SELECT 1;")},
		"002_empty.sql": &fstest.MapFile{Data: []byte(" \n\t")},
		"003_next.sql":  &fstest.MapFile{Data: []byte("SELECT 3;")},
	})
	require.NoError(t, err)
	require.True(t, report.Valid())
	require.Equal(t, []string{"001_init.sql", "003_next.sql"}, report.Pending)
	require.Equal(t, 2, report.EmbeddedCount)
	require.Zero(t, report.AppliedCount)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCheckMigrationPreflightFS_IsReadOnlyAndPropagatesReadFailures(t *testing.T) {
	t.Run("database query", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		mock.ExpectQuery(`SELECT filename, checksum FROM schema_migrations ORDER BY filename`).
			WillReturnError(errors.New("permission denied"))

		_, err = checkMigrationPreflightFS(context.Background(), db, fstest.MapFS{})
		require.ErrorContains(t, err, "read schema_migrations")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("migration file", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		_, err = checkMigrationPreflightFS(context.Background(), db, fstest.MapFS{
			"001_bad.sql": &fstest.MapFile{Mode: fs.ModeDir},
		})
		require.ErrorContains(t, err, "read migration 001_bad.sql")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestCheckMigrationPreflightFS_RejectsNilQueryer(t *testing.T) {
	_, err := checkMigrationPreflightFS(context.Background(), nil, fstest.MapFS{})
	require.ErrorContains(t, err, "nil migration queryer")
}
