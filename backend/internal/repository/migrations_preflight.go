package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/migrations"
)

// MigrationChecksumStatus describes how an applied migration checksum compares
// with the migration embedded in this binary.
type MigrationChecksumStatus string

const (
	MigrationChecksumExact      MigrationChecksumStatus = "exact"
	MigrationChecksumCompatible MigrationChecksumStatus = "compatible"
	MigrationChecksumMismatch   MigrationChecksumStatus = "mismatch"
)

// AppliedMigration is one row read from schema_migrations.
type AppliedMigration struct {
	Filename string `json:"filename"`
	Checksum string `json:"checksum"`
}

// MigrationChecksumResult records the checksum comparison for an applied
// migration that is still present in the embedded migration set.
type MigrationChecksumResult struct {
	Filename         string                  `json:"filename"`
	DatabaseChecksum string                  `json:"database_checksum"`
	FileChecksum     string                  `json:"file_checksum"`
	Status           MigrationChecksumStatus `json:"status"`
}

// MigrationPreflightReport is the complete read-only comparison between the
// embedded migration set and schema_migrations.
//
// Pending is safe because it is a contiguous suffix of the sorted embedded
// migrations, plus registered same-number files that were inserted before an
// applied migration (also listed in OutOfOrderPending). Missing contains every
// other gap before the latest applied file.
type MigrationPreflightReport struct {
	EmbeddedCount      int                       `json:"embedded_count"`
	AppliedCount       int                       `json:"applied_count"`
	Pending            []string                  `json:"pending"`
	OutOfOrderPending  []string                  `json:"out_of_order_pending"`
	Missing            []string                  `json:"missing"`
	Extra              []AppliedMigration        `json:"extra"`
	Checksums          []MigrationChecksumResult `json:"checksums"`
	ChecksumMismatches []MigrationChecksumResult `json:"checksum_mismatches"`
}

// outOfOrderMigrationAllowlist 登记合并上游时插进已有编号中间的迁移文件。
//
// 迁移执行器按文件名逐个判断是否执行过，这类文件在已经执行过后续编号的库上
// 启动时会被补跑；只读预检据此把它们判为 pending（并在 OutOfOrderPending 中标注
// 乱序补跑），而不是 missing。只有同时满足下面两点才放行，其余中间缺口仍判 missing：
//   - 文件名在本表中登记；
//   - 同编号前缀下至少有一个文件已经执行（说明它是与已执行迁移并存的同编号插队文件，
//     而不是整段迁移历史缺失）。
//
// 登记的文件必须只依赖它之前的迁移、可以在任意后续迁移之后安全执行。
var outOfOrderMigrationAllowlist = map[string]struct{}{
	// 官方 v0.2.18：把 gemini/antigravity 分组的生图开关置为开启。只更新 groups
	// 的既有列，247-257 均不触及 groups，晚于它们执行结果相同。
	"246_enable_gemini_image_generation_groups.sql": {},
	"247_scoped_admin_api_keys.sql":                 {},
	"248_group_image_count_billing.sql":             {},
	"249_mcp_oauth.sql":                             {},
	// v0.2.21 adds an independent index and nullable/defaulted pricing fields;
	// neither changes the meaning of the custom migrations through 259.
	"249_add_usage_logs_api_key_cost_index_notx.sql": {},
	"250_openai_cache_write_ttl.sql":                 {},
}

// Valid reports whether applying the pending suffix is safe from migration
// history drift. Pending migrations do not make a report invalid.
func (r MigrationPreflightReport) Valid() bool {
	return len(r.Missing) == 0 && len(r.Extra) == 0 && len(r.ChecksumMismatches) == 0
}

// MigrationPreflightQueryer deliberately exposes only QueryContext. This keeps
// the preflight implementation unable to execute DDL or otherwise mutate the
// database. Both *sql.DB and read-only *sql.Tx satisfy the interface.
type MigrationPreflightQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// CheckMigrationPreflight compares every schema_migrations row with the SQL
// migrations embedded in this binary. It never creates tables or applies SQL.
func CheckMigrationPreflight(ctx context.Context, queryer MigrationPreflightQueryer) (MigrationPreflightReport, error) {
	return checkMigrationPreflightFS(ctx, queryer, migrations.FS)
}

type embeddedMigration struct {
	filename string
	checksum string
}

func checkMigrationPreflightFS(ctx context.Context, queryer MigrationPreflightQueryer, fsys fs.FS) (MigrationPreflightReport, error) {
	report := MigrationPreflightReport{
		Pending:            make([]string, 0),
		OutOfOrderPending:  make([]string, 0),
		Missing:            make([]string, 0),
		Extra:              make([]AppliedMigration, 0),
		Checksums:          make([]MigrationChecksumResult, 0),
		ChecksumMismatches: make([]MigrationChecksumResult, 0),
	}
	if queryer == nil {
		return report, errors.New("nil migration queryer")
	}

	embedded, err := loadEmbeddedMigrationChecksums(fsys)
	if err != nil {
		return report, err
	}
	report.EmbeddedCount = len(embedded)

	rows, err := queryer.QueryContext(ctx, "SELECT filename, checksum FROM schema_migrations ORDER BY filename")
	if err != nil {
		return report, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := make([]AppliedMigration, 0, len(embedded))
	appliedByName := make(map[string]AppliedMigration, len(embedded))
	for rows.Next() {
		var item AppliedMigration
		if err := rows.Scan(&item.Filename, &item.Checksum); err != nil {
			return report, fmt.Errorf("scan schema_migrations: %w", err)
		}
		if _, exists := appliedByName[item.Filename]; exists {
			return report, fmt.Errorf("duplicate schema_migrations filename %q", item.Filename)
		}
		applied = append(applied, item)
		appliedByName[item.Filename] = item
	}
	if err := rows.Err(); err != nil {
		return report, fmt.Errorf("iterate schema_migrations: %w", err)
	}
	report.AppliedCount = len(applied)

	embeddedIndex := make(map[string]int, len(embedded))
	appliedPrefixes := make(map[string]struct{}, len(embedded))
	lastAppliedIndex := -1
	for i, item := range embedded {
		embeddedIndex[item.filename] = i
		if _, ok := appliedByName[item.filename]; ok {
			lastAppliedIndex = i
			appliedPrefixes[migrationNumberPrefix(item.filename)] = struct{}{}
		}
	}

	for _, item := range applied {
		idx, ok := embeddedIndex[item.Filename]
		if !ok {
			report.Extra = append(report.Extra, item)
			continue
		}

		fileChecksum := embedded[idx].checksum
		status := MigrationChecksumExact
		if item.Checksum != fileChecksum {
			if isMigrationChecksumCompatible(item.Filename, item.Checksum, fileChecksum) {
				status = MigrationChecksumCompatible
			} else {
				status = MigrationChecksumMismatch
			}
		}
		result := MigrationChecksumResult{
			Filename:         item.Filename,
			DatabaseChecksum: item.Checksum,
			FileChecksum:     fileChecksum,
			Status:           status,
		}
		report.Checksums = append(report.Checksums, result)
		if status == MigrationChecksumMismatch {
			report.ChecksumMismatches = append(report.ChecksumMismatches, result)
		}
	}

	for i, item := range embedded {
		if _, ok := appliedByName[item.filename]; ok {
			continue
		}
		switch {
		case i > lastAppliedIndex:
			report.Pending = append(report.Pending, item.filename)
		case isAllowedOutOfOrderMigration(item.filename, appliedPrefixes):
			report.Pending = append(report.Pending, item.filename)
			report.OutOfOrderPending = append(report.OutOfOrderPending, item.filename)
		default:
			report.Missing = append(report.Missing, item.filename)
		}
	}

	return report, nil
}

// isAllowedOutOfOrderMigration 判断一个排在最后已执行迁移之前、自身尚未执行的文件
// 能否作为乱序补跑的 pending，规则见 outOfOrderMigrationAllowlist。
func isAllowedOutOfOrderMigration(filename string, appliedPrefixes map[string]struct{}) bool {
	if _, ok := outOfOrderMigrationAllowlist[filename]; !ok {
		return false
	}
	prefix := migrationNumberPrefix(filename)
	if prefix == "" {
		return false
	}
	_, ok := appliedPrefixes[prefix]
	return ok
}

// migrationNumberPrefix 返回迁移文件名开头的数字编号（如 "246"）；没有数字前缀时返回空串。
func migrationNumberPrefix(filename string) string {
	end := 0
	for end < len(filename) && filename[end] >= '0' && filename[end] <= '9' {
		end++
	}
	return filename[:end]
}

func loadEmbeddedMigrationChecksums(fsys fs.FS) ([]embeddedMigration, error) {
	files, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(files)

	result := make([]embeddedMigration, 0, len(files))
	for _, name := range files {
		contentBytes, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		content := strings.TrimSpace(string(contentBytes))
		if content == "" {
			continue
		}
		result = append(result, embeddedMigration{
			filename: name,
			checksum: calculateMigrationChecksum(content),
		})
	}
	return result, nil
}

func calculateMigrationChecksum(content string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(content)))
	return hex.EncodeToString(sum[:])
}
