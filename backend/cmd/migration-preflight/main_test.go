package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/stretchr/testify/require"
)

func TestRunDatabasePreflight_UsesVerifiedReadOnlyTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery(`SHOW transaction_read_only`).
		WillReturnRows(sqlmock.NewRows([]string{"transaction_read_only"}).AddRow("on"))
	mock.ExpectQuery(`SELECT filename, checksum FROM schema_migrations ORDER BY filename`).
		WillReturnRows(sqlmock.NewRows([]string{"filename", "checksum"}))
	mock.ExpectRollback()

	report, err := runDatabasePreflight(context.Background(), db)
	require.NoError(t, err)
	require.True(t, report.Valid())
	require.NotEmpty(t, report.Pending)
	require.Contains(t, report.Pending, "238_purge_unlimited_user_platform_quotas.sql")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRunDatabasePreflight_StopsIfTransactionIsNotReadOnly(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery(`SHOW transaction_read_only`).
		WillReturnRows(sqlmock.NewRows([]string{"transaction_read_only"}).AddRow("off"))
	mock.ExpectRollback()

	_, err = runDatabasePreflight(context.Background(), db)
	require.ErrorContains(t, err, "transaction_read_only")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRunDatabasePreflight_PropagatesBeginFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectBegin().WillReturnError(errors.New("database unavailable"))

	_, err = runDatabasePreflight(context.Background(), db)
	require.ErrorContains(t, err, "begin read-only transaction")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWriteJSONReport_IncludesVerdictAndStableEmptyArrays(t *testing.T) {
	report := repository.MigrationPreflightReport{
		EmbeddedCount:      2,
		AppliedCount:       1,
		Pending:            []string{"002_next.sql"},
		Missing:            []string{},
		Extra:              []repository.AppliedMigration{},
		Checksums:          []repository.MigrationChecksumResult{},
		ChecksumMismatches: []repository.MigrationChecksumResult{},
	}
	var output bytes.Buffer

	require.NoError(t, writeJSONReport(&output, report))
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &decoded))
	require.Equal(t, true, decoded["valid"])
	require.Equal(t, []any{}, decoded["missing"])
	require.Equal(t, []any{}, decoded["extra"])
}

func TestRun_HelpExitsSuccessfullyWithoutLoadingConfig(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run([]string{"-h"}, &stdout, &stderr)

	require.Equal(t, exitOK, exitCode)
	require.Contains(t, stderr.String(), "migration-preflight")
}

func TestRun_ExplicitMissingConfigFailsClosed(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run([]string{"-config", t.TempDir() + "/missing.yaml", "-json"}, &stdout, &stderr)

	require.Equal(t, exitError, exitCode)
	require.Contains(t, stderr.String(), "config file")
	require.Empty(t, stdout.String())
}

func TestRun_ExplicitDirectoryConfigFailsClosed(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run([]string{"-config", t.TempDir(), "-json"}, &stdout, &stderr)

	require.Equal(t, exitError, exitCode)
	require.Contains(t, stderr.String(), "not a regular file")
	require.Empty(t, stdout.String())
}

func TestRun_ExplicitConfigRestoresProcessEnvironment(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("not: [valid"), 0o600))
	t.Setenv("CONFIG_FILE", "sentinel-config-path")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"-config", configPath, "-json"}, &stdout, &stderr)

	require.Equal(t, exitError, exitCode)
	got, ok := os.LookupEnv("CONFIG_FILE")
	require.True(t, ok)
	require.Equal(t, "sentinel-config-path", got)
}
