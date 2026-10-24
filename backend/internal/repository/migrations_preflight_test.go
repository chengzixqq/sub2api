package repository

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/migrations"
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

// 官方 246 插在已执行的定制 246 与 247-257 之间。迁移执行器逐文件判断是否执行过，启动时会补跑它；
// 预检把这种已登记的同编号插队文件判为 pending 并单独标注乱序补跑，不算缺失。
func TestCheckMigrationPreflightFS_AllowsRegisteredSameNumberOutOfOrderMigration(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	fsys := fstest.MapFS{
		"245_base.sql":         &fstest.MapFile{Data: []byte("SELECT 245;")},
		"246_custom.sql":       &fstest.MapFile{Data: []byte("SELECT 246;")},
		"246_upstream.sql":     &fstest.MapFile{Data: []byte("SELECT 'upstream';")},
		"247_custom.sql":       &fstest.MapFile{Data: []byte("SELECT 247;")},
		"248_pending.sql":      &fstest.MapFile{Data: []byte("SELECT 248;")},
		"249_unregistered.sql": &fstest.MapFile{Data: []byte("SELECT 249;")},
	}
	withOutOfOrderMigrations(t, "246_upstream.sql")

	mock.ExpectQuery(`SELECT filename, checksum FROM schema_migrations ORDER BY filename`).
		WillReturnRows(sqlmock.NewRows([]string{"filename", "checksum"}).
			AddRow("245_base.sql", migrationChecksum("SELECT 245;")).
			AddRow("246_custom.sql", migrationChecksum("SELECT 246;")).
			AddRow("247_custom.sql", migrationChecksum("SELECT 247;")))

	report, err := checkMigrationPreflightFS(context.Background(), db, fsys)
	require.NoError(t, err)
	require.True(t, report.Valid())
	require.Empty(t, report.Missing)
	require.Equal(t, []string{"246_upstream.sql", "248_pending.sql", "249_unregistered.sql"}, report.Pending)
	require.Equal(t, []string{"246_upstream.sql"}, report.OutOfOrderPending)
	require.NoError(t, mock.ExpectationsWereMet())
}

// 放行只针对登记过且与已执行迁移同编号的文件：真正缺失的迁移、未登记的插队文件、
// 登记了但编号没有任何已执行同伴的文件，仍然判为 missing。
func TestCheckMigrationPreflightFS_OutOfOrderAllowanceDoesNotHideRealGaps(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	fsys := fstest.MapFS{
		"245_base.sql":         &fstest.MapFile{Data: []byte("SELECT 245;")},
		"245_unregistered.sql": &fstest.MapFile{Data: []byte("SELECT 'u';")},
		"246_lonely.sql":       &fstest.MapFile{Data: []byte("SELECT 'lonely';")},
		"247_custom.sql":       &fstest.MapFile{Data: []byte("SELECT 247;")},
	}
	withOutOfOrderMigrations(t, "246_lonely.sql")

	mock.ExpectQuery(`SELECT filename, checksum FROM schema_migrations ORDER BY filename`).
		WillReturnRows(sqlmock.NewRows([]string{"filename", "checksum"}).
			AddRow("245_base.sql", migrationChecksum("SELECT 245;")).
			AddRow("247_custom.sql", migrationChecksum("SELECT 247;")))

	report, err := checkMigrationPreflightFS(context.Background(), db, fsys)
	require.NoError(t, err)
	require.False(t, report.Valid())
	require.Equal(t, []string{"245_unregistered.sql", "246_lonely.sql"}, report.Missing)
	require.Empty(t, report.Pending)
	require.Empty(t, report.OutOfOrderPending)
	require.NoError(t, mock.ExpectationsWereMet())
}

// 登记的插队文件一旦执行过，就和普通已执行迁移一样做校验和比对，不能借放行掩盖篡改。
func TestCheckMigrationPreflightFS_OutOfOrderMigrationStillChecksChecksum(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	fsys := fstest.MapFS{
		"246_custom.sql":   &fstest.MapFile{Data: []byte("SELECT 246;")},
		"246_upstream.sql": &fstest.MapFile{Data: []byte("SELECT 'upstream';")},
		"247_custom.sql":   &fstest.MapFile{Data: []byte("SELECT 247;")},
	}
	withOutOfOrderMigrations(t, "246_upstream.sql")

	mock.ExpectQuery(`SELECT filename, checksum FROM schema_migrations ORDER BY filename`).
		WillReturnRows(sqlmock.NewRows([]string{"filename", "checksum"}).
			AddRow("246_custom.sql", migrationChecksum("SELECT 246;")).
			AddRow("246_upstream.sql", "tampered").
			AddRow("247_custom.sql", migrationChecksum("SELECT 247;")))

	report, err := checkMigrationPreflightFS(context.Background(), db, fsys)
	require.NoError(t, err)
	require.False(t, report.Valid())
	require.Empty(t, report.Pending)
	require.Empty(t, report.OutOfOrderPending)
	require.Len(t, report.ChecksumMismatches, 1)
	require.Equal(t, "246_upstream.sql", report.ChecksumMismatches[0].Filename)
	require.NoError(t, mock.ExpectationsWereMet())
}

// 真实迁移集：已从原 main 执行到 257（含定制 246）的库只剩官方 246 待补跑；
// 生产 e5ab7e4f4 停在定制 246，官方 246 与 247-257 都是正常的后缀 pending。
func TestCheckMigrationPreflight_UpstreamGeminiImage246OnUpgradedDatabases(t *testing.T) {
	embedded, err := loadEmbeddedMigrationChecksums(migrations.FS)
	require.NoError(t, err)
	const upstream246 = "246_enable_gemini_image_generation_groups.sql"
	const custom246 = "246_dashboard_hourly_invalidation.sql"

	appliedUpTo := func(last string, skip string) *sqlmock.Rows {
		rows := sqlmock.NewRows([]string{"filename", "checksum"})
		for _, item := range embedded {
			if item.filename != skip {
				rows.AddRow(item.filename, item.checksum)
			}
			if item.filename == last {
				break
			}
		}
		return rows
	}

	t.Run("database migrated by the previous main", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer func() { _ = db.Close() }()
		mock.ExpectQuery(`SELECT filename, checksum FROM schema_migrations ORDER BY filename`).
			WillReturnRows(appliedUpTo("257_push_notifications.sql", upstream246))

		report, err := CheckMigrationPreflight(context.Background(), db)
		require.NoError(t, err)
		require.True(t, report.Valid())
		require.Empty(t, report.Missing)
		require.Equal(t, []string{upstream246}, report.Pending)
		require.Equal(t, []string{upstream246}, report.OutOfOrderPending)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("production database at e5ab7e4f4", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer func() { _ = db.Close() }()
		mock.ExpectQuery(`SELECT filename, checksum FROM schema_migrations ORDER BY filename`).
			WillReturnRows(appliedUpTo(custom246, ""))

		report, err := CheckMigrationPreflight(context.Background(), db)
		require.NoError(t, err)
		require.True(t, report.Valid())
		require.Empty(t, report.Missing)
		require.Empty(t, report.OutOfOrderPending)
		require.Equal(t, []string{
			upstream246,
			"247_restore_ops_alert_silences.sql",
			"248_redeem_code_batches.sql",
			"249_admin_user_adjustments_request_id_index.sql",
			"250_add_account_error_at.sql",
			"251_add_account_config_version.sql",
			"252_admin_access_tokens.sql",
			"253_ops_alert_event_rule_snapshot.sql",
			"254_add_scheduled_test_result_upstream_status.sql",
			"255_account_test_runs.sql",
			"256_admin_user_adjustments_redeem_code_id.sql",
			"257_push_notifications.sql",
		}, report.Pending)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func withOutOfOrderMigrations(t *testing.T, names ...string) {
	t.Helper()
	previous := outOfOrderMigrationAllowlist
	allowlist := make(map[string]struct{}, len(names))
	for _, name := range names {
		allowlist[name] = struct{}{}
	}
	outOfOrderMigrationAllowlist = allowlist
	t.Cleanup(func() { outOfOrderMigrationAllowlist = previous })
}
