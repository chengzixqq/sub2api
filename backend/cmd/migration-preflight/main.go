package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	_ "github.com/lib/pq"
)

const (
	exitOK       = 0
	exitError    = 1
	exitUnsafe   = 2
	defaultLimit = 30 * time.Second
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("migration-preflight", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configFile := flags.String("config", "", "config.yaml path (defaults to CONFIG_FILE or normal application lookup)")
	jsonOutput := flags.Bool("json", false, "write the complete report as JSON")
	timeout := flags.Duration("timeout", defaultLimit, "database preflight timeout")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUnsafe
	}
	if *timeout <= 0 {
		fmt.Fprintln(stderr, "migration preflight: --timeout must be greater than zero")
		return exitUnsafe
	}
	if configPath := strings.TrimSpace(*configFile); configPath != "" {
		// Follow symlinks intentionally: release layouts commonly point a stable
		// config path at an immutable versioned file. The target still must be a
		// regular file before it is handed to the bootstrap loader.
		info, err := os.Stat(configPath)
		if err != nil {
			fmt.Fprintf(stderr, "migration preflight: config file %q: %v\n", configPath, err)
			return exitError
		}
		if !info.Mode().IsRegular() {
			fmt.Fprintf(stderr, "migration preflight: config file %q is not a regular file\n", configPath)
			return exitError
		}
		previousConfig, hadPreviousConfig := os.LookupEnv("CONFIG_FILE")
		if err := os.Setenv("CONFIG_FILE", configPath); err != nil {
			fmt.Fprintf(stderr, "migration preflight: set config path: %v\n", err)
			return exitError
		}
		defer func() {
			if hadPreviousConfig {
				_ = os.Setenv("CONFIG_FILE", previousConfig)
				return
			}
			_ = os.Unsetenv("CONFIG_FILE")
		}()
	}

	cfg, err := config.LoadForBootstrap()
	if err != nil {
		fmt.Fprintf(stderr, "migration preflight: load config: %v\n", err)
		return exitError
	}

	db, err := sql.Open("postgres", cfg.Database.DSNWithTimezone(cfg.Timezone))
	if err != nil {
		fmt.Fprintf(stderr, "migration preflight: open database: %v\n", err)
		return exitError
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	report, err := runDatabasePreflight(ctx, db)
	if err != nil {
		fmt.Fprintf(stderr, "migration preflight: %v\n", err)
		return exitError
	}

	if *jsonOutput {
		if err := writeJSONReport(stdout, report); err != nil {
			fmt.Fprintf(stderr, "migration preflight: write report: %v\n", err)
			return exitError
		}
	} else {
		writeHumanReport(stdout, report)
	}
	if !report.Valid() {
		return exitUnsafe
	}
	return exitOK
}

func runDatabasePreflight(ctx context.Context, db *sql.DB) (repository.MigrationPreflightReport, error) {
	var empty repository.MigrationPreflightReport
	if db == nil {
		return empty, errors.New("nil sql db")
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return empty, fmt.Errorf("begin read-only transaction: %w", err)
	}

	var readOnly string
	if err := tx.QueryRowContext(ctx, "SHOW transaction_read_only").Scan(&readOnly); err != nil {
		_ = tx.Rollback()
		return empty, fmt.Errorf("verify transaction_read_only: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(readOnly), "on") {
		_ = tx.Rollback()
		return empty, fmt.Errorf("transaction_read_only is %q, refusing migration preflight", readOnly)
	}

	report, checkErr := repository.CheckMigrationPreflight(ctx, tx)
	rollbackErr := tx.Rollback()
	if checkErr != nil {
		return report, checkErr
	}
	if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
		return report, fmt.Errorf("close read-only transaction: %w", rollbackErr)
	}
	return report, nil
}

func writeJSONReport(w io.Writer, report repository.MigrationPreflightReport) error {
	payload := struct {
		Valid bool `json:"valid"`
		repository.MigrationPreflightReport
	}{
		Valid:                    report.Valid(),
		MigrationPreflightReport: report,
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(payload)
}

func writeHumanReport(w io.Writer, report repository.MigrationPreflightReport) {
	status := "PASS"
	if !report.Valid() {
		status = "FAIL"
	}
	fmt.Fprintf(
		w,
		"status=%s embedded=%d applied=%d pending=%d missing=%d extra=%d checksum_mismatches=%d\n",
		status,
		report.EmbeddedCount,
		report.AppliedCount,
		len(report.Pending),
		len(report.Missing),
		len(report.Extra),
		len(report.ChecksumMismatches),
	)
	for _, result := range report.Checksums {
		fmt.Fprintf(
			w,
			"checksum filename=%s status=%s database=%s file=%s\n",
			result.Filename,
			result.Status,
			result.DatabaseChecksum,
			result.FileChecksum,
		)
	}
	for _, name := range report.Missing {
		fmt.Fprintf(w, "missing filename=%s\n", name)
	}
	for _, item := range report.Extra {
		fmt.Fprintf(w, "extra filename=%s database=%s\n", item.Filename, item.Checksum)
	}
	for _, name := range report.Pending {
		fmt.Fprintf(w, "pending filename=%s\n", name)
	}
}
