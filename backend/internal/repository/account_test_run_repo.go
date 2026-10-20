package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type accountTestRunRepository struct {
	db *sql.DB
}

// NewAccountTestRunRepository stores account test runs in account_test_runs.
func NewAccountTestRunRepository(db *sql.DB) service.AccountTestRunRepository {
	return &accountTestRunRepository{db: db}
}

const accountTestRunColumns = `id, run_id, account_id, source, status, model_id, latency_ms, first_token_ms,
 upstream_status_code, error_message, response_text, recovery, started_at, finished_at, created_by`

func scanAccountTestRun(row rowScanner) (*service.AccountTestRun, error) {
	var r service.AccountTestRun
	var latency, firstToken, createdBy sql.NullInt64
	var upstream sql.NullInt32
	var recovery []byte
	var finished sql.NullTime
	if err := row.Scan(&r.ID, &r.RunID, &r.AccountID, &r.Source, &r.Status, &r.ModelID, &latency, &firstToken,
		&upstream, &r.ErrorMessage, &r.ResponseText, &recovery, &r.StartedAt, &finished, &createdBy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrAccountTestRunNotFound
		}
		return nil, err
	}
	if latency.Valid {
		r.LatencyMs = &latency.Int64
	}
	if firstToken.Valid {
		r.FirstTokenMs = &firstToken.Int64
	}
	if upstream.Valid {
		code := int(upstream.Int32)
		r.UpstreamStatusCode = &code
	}
	if createdBy.Valid {
		r.CreatedBy = &createdBy.Int64
	}
	if finished.Valid {
		at := finished.Time
		r.FinishedAt = &at
	}
	if len(recovery) > 0 {
		var rec service.AccountTestRunRecovery
		if err := json.Unmarshal(recovery, &rec); err != nil {
			return nil, err
		}
		r.Recovery = &rec
	}
	return &r, nil
}

func accountTestRunRecoveryJSON(rec *service.AccountTestRunRecovery) (any, error) {
	if rec == nil {
		return nil, nil
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	return string(raw), nil
}

// accountTestRunCreateAttempts bounds the insert / read loop of CreateRunning:
// the running row that blocked an insert can finish before it is read.
const accountTestRunCreateAttempts = 3

func (r *accountTestRunRepository) CreateRunning(ctx context.Context, run *service.AccountTestRun) (*service.AccountTestRun, bool, error) {
	for attempt := 0; attempt < accountTestRunCreateAttempts; attempt++ {
		stored, err := scanAccountTestRun(r.db.QueryRowContext(ctx, `
INSERT INTO account_test_runs (run_id, account_id, source, status, model_id, created_by)
VALUES ($1, $2, $3, 'running', $4, $5)
ON CONFLICT (account_id) WHERE status = 'running' DO NOTHING
RETURNING `+accountTestRunColumns,
			run.RunID, run.AccountID, run.Source, run.ModelID, run.CreatedBy))
		if err == nil {
			return stored, true, nil
		}
		if !errors.Is(err, service.ErrAccountTestRunNotFound) {
			return nil, false, err
		}
		existing, err := r.GetRunningByAccount(ctx, run.AccountID)
		if err == nil {
			return existing, false, nil
		}
		if !errors.Is(err, service.ErrAccountTestRunNotFound) {
			return nil, false, err
		}
		// The blocking run finished between the two statements: insert again.
	}
	return nil, false, fmt.Errorf("account test run: account %d running row kept changing", run.AccountID)
}

func (r *accountTestRunRepository) GetRunningByAccount(ctx context.Context, accountID int64) (*service.AccountTestRun, error) {
	return scanAccountTestRun(r.db.QueryRowContext(ctx, `SELECT `+accountTestRunColumns+`
 FROM account_test_runs WHERE account_id = $1 AND status = 'running' LIMIT 1`, accountID))
}

func (r *accountTestRunRepository) Finish(ctx context.Context, run *service.AccountTestRun) error {
	recovery, err := accountTestRunRecoveryJSON(run.Recovery)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
UPDATE account_test_runs
SET status = $2, model_id = $3, latency_ms = $4, first_token_ms = $5, upstream_status_code = $6,
    error_message = $7, response_text = $8, recovery = $9::jsonb, finished_at = COALESCE($10, NOW())
WHERE run_id = $1 AND status = 'running'`,
		run.RunID, run.Status, run.ModelID, run.LatencyMs, run.FirstTokenMs, run.UpstreamStatusCode,
		run.ErrorMessage, run.ResponseText, recovery, run.FinishedAt)
	return err
}

func (r *accountTestRunRepository) InsertFinished(ctx context.Context, run *service.AccountTestRun) error {
	recovery, err := accountTestRunRecoveryJSON(run.Recovery)
	if err != nil {
		return err
	}
	startedAt := run.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return r.db.QueryRowContext(ctx, `
INSERT INTO account_test_runs (run_id, account_id, source, status, model_id, latency_ms, first_token_ms,
    upstream_status_code, error_message, response_text, recovery, started_at, finished_at, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, $12, COALESCE($13, NOW()), $14)
RETURNING id`,
		run.RunID, run.AccountID, run.Source, run.Status, run.ModelID, run.LatencyMs, run.FirstTokenMs,
		run.UpstreamStatusCode, run.ErrorMessage, run.ResponseText, recovery, startedAt, run.FinishedAt, run.CreatedBy).Scan(&run.ID)
}

func (r *accountTestRunRepository) GetByRunID(ctx context.Context, runID string) (*service.AccountTestRun, error) {
	return scanAccountTestRun(r.db.QueryRowContext(ctx,
		`SELECT `+accountTestRunColumns+` FROM account_test_runs WHERE run_id = $1`, runID))
}

func (r *accountTestRunRepository) GetLatestByAccount(ctx context.Context, accountID int64) (*service.AccountTestRun, error) {
	return scanAccountTestRun(r.db.QueryRowContext(ctx, `SELECT `+accountTestRunColumns+`
 FROM account_test_runs WHERE account_id = $1
 ORDER BY started_at DESC, id DESC LIMIT 1`, accountID))
}

// InterruptStale compares against the database clock so instances with skewed
// clocks agree on which runs are stale.
func (r *accountTestRunRepository) InterruptStale(ctx context.Context, accountID int64, olderThan time.Duration) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
UPDATE account_test_runs
SET status = 'interrupted', finished_at = NOW(), error_message = $3
WHERE status = 'running'
  AND ($1 = 0 OR account_id = $1)
  AND started_at < NOW() - make_interval(secs => $2)`,
		accountID, olderThan.Seconds(), service.AccountTestRunInterruptedMessage)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *accountTestRunRepository) Prune(ctx context.Context, accountID int64, keep int) error {
	_, err := r.db.ExecContext(ctx, `
DELETE FROM account_test_runs
WHERE account_id = $1 AND status <> 'running'
  AND id NOT IN (
    SELECT id FROM account_test_runs
    WHERE account_id = $1 AND status <> 'running'
    ORDER BY started_at DESC, id DESC
    LIMIT $2
  )`, accountID, keep)
	return err
}
