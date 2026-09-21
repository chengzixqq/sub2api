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
// Pending is safe only because it is a contiguous suffix of the sorted
// embedded migrations. Missing contains gaps before the latest applied file.
type MigrationPreflightReport struct {
	EmbeddedCount      int                       `json:"embedded_count"`
	AppliedCount       int                       `json:"applied_count"`
	Pending            []string                  `json:"pending"`
	Missing            []string                  `json:"missing"`
	Extra              []AppliedMigration        `json:"extra"`
	Checksums          []MigrationChecksumResult `json:"checksums"`
	ChecksumMismatches []MigrationChecksumResult `json:"checksum_mismatches"`
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
	lastAppliedIndex := -1
	for i, item := range embedded {
		embeddedIndex[item.filename] = i
		if _, ok := appliedByName[item.filename]; ok {
			lastAppliedIndex = i
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
		if i <= lastAppliedIndex {
			report.Missing = append(report.Missing, item.filename)
		} else {
			report.Pending = append(report.Pending, item.filename)
		}
	}

	return report, nil
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
