package service

import (
	"context"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

// Account test run sources: async runs come from POST /accounts/:id/test-runs,
// manual runs record the interactive SSE test. scheduled is reserved; scheduled
// results stay in scheduled_test_results.
const (
	AccountTestRunSourceManual    = "manual"
	AccountTestRunSourceAsync     = "async"
	AccountTestRunSourceScheduled = "scheduled"
)

// Account test run statuses.
const (
	AccountTestRunStatusRunning     = "running"
	AccountTestRunStatusSuccess     = "success"
	AccountTestRunStatusFailed      = accountTestStatusFailed
	AccountTestRunStatusInterrupted = "interrupted"
)

const (
	accountTestRunIDPrefix = "tr_"
	// accountTestRunTimeout bounds one async run, detached from the request.
	accountTestRunTimeout = 120 * time.Second
	// AccountTestRunStaleAfter marks running rows older than this interrupted
	// (crash / restart); it exceeds the timeout so live runs are never touched.
	AccountTestRunStaleAfter = 180 * time.Second
	// AccountTestRunKeepPerAccount is how many finished runs each account keeps.
	AccountTestRunKeepPerAccount = 20
	// accountTestRunMaxWorkers caps concurrent async runs per instance.
	accountTestRunMaxWorkers = 10
	// accountTestRunMaxTextRunes caps the stored error_message / response_text.
	accountTestRunMaxTextRunes = 2000
	accountTestRunMaxModelLen  = 200
	// AccountTestRunInterruptedMessage is stored on runs that never finished.
	AccountTestRunInterruptedMessage = "Account test was interrupted before it finished"
)

var (
	ErrAccountTestRunning     = infraerrors.Conflict("ACCOUNT_TEST_RUNNING", "a test is already running for this account")
	ErrAccountTestRunNotFound = infraerrors.NotFound("ACCOUNT_TEST_RUN_NOT_FOUND", "account test run not found")
	ErrAccountTestBusy        = infraerrors.TooManyRequests("ACCOUNT_TEST_BUSY", "too many account tests are running, retry later")
)

// AccountTestRun is one stored account test outcome.
type AccountTestRun struct {
	ID                 int64
	RunID              string
	AccountID          int64
	Source             string
	Status             string
	ModelID            string
	LatencyMs          *int64
	FirstTokenMs       *int64
	UpstreamStatusCode *int
	ErrorMessage       string
	ResponseText       string
	Recovery           *AccountTestRunRecovery
	StartedAt          time.Time
	FinishedAt         *time.Time
	CreatedBy          *int64
}

// AccountTestRunRecovery reports what the post-success recovery cleared; nil
// on a run means recovery was not attempted.
type AccountTestRunRecovery struct {
	ClearedError     bool `json:"cleared_error"`
	ClearedRateLimit bool `json:"cleared_rate_limit"`
}

// ErrorKind classifies a failed run (C02 kinds plus rate_limit); nil otherwise.
func (r *AccountTestRun) ErrorKind() *string {
	return AccountTestErrorKind(r.Status, r.UpstreamStatusCode, r.ErrorMessage)
}

// ErrorSummary is the short redacted form of ErrorMessage.
func (r *AccountTestRun) ErrorSummary() string {
	return DescribeAccountError(r.ErrorMessage).Summary
}

// AccountTestRunRepository stores account test runs.
type AccountTestRunRepository interface {
	// CreateRunning inserts run as running unless the account already has a
	// running row; it returns the stored row and whether it was created.
	CreateRunning(ctx context.Context, run *AccountTestRun) (*AccountTestRun, bool, error)
	// GetRunningByAccount returns the running row of the account, or
	// ErrAccountTestRunNotFound when it has none.
	GetRunningByAccount(ctx context.Context, accountID int64) (*AccountTestRun, error)
	// Finish writes the outcome of a running row (no-op once it left running).
	Finish(ctx context.Context, run *AccountTestRun) error
	// InsertFinished stores an already finished run (manual SSE tests).
	InsertFinished(ctx context.Context, run *AccountTestRun) error
	GetByRunID(ctx context.Context, runID string) (*AccountTestRun, error)
	GetLatestByAccount(ctx context.Context, accountID int64) (*AccountTestRun, error)
	// InterruptStale marks running rows older than olderThan (by database
	// time) interrupted; accountID 0 covers all accounts.
	InterruptStale(ctx context.Context, accountID int64, olderThan time.Duration) (int64, error)
	// Prune keeps the latest keep finished rows of the account.
	Prune(ctx context.Context, accountID int64, keep int) error
}

// StartAccountTestRunInput is the request of an async account test.
type StartAccountTestRunInput struct {
	AccountID        int64
	ModelID          string
	Prompt           string
	Mode             string
	RecoverOnSuccess *bool
	CreatedBy        *int64
}

// ManualAccountTestRun is the outcome of an interactive SSE test to record.
type ManualAccountTestRun struct {
	AccountID   int64
	Capture     AccountTestCapture
	Interrupted bool
	Recovery    *SuccessfulTestRecoveryResult
	CreatedBy   *int64
}

func newAccountTestRunID() string {
	return accountTestRunIDPrefix + strings.ReplaceAll(uuid.Must(uuid.NewV7()).String(), "-", "")
}

// IsAccountTestRunID reports whether runID has the run ID shape.
func IsAccountTestRunID(runID string) bool {
	return strings.HasPrefix(runID, accountTestRunIDPrefix) && len(runID) <= 40
}

// applyAccountTestCapture copies a finished capture onto run.
func applyAccountTestCapture(run *AccountTestRun, capture AccountTestCapture) {
	run.Status = AccountTestRunStatusFailed
	if capture.Success {
		run.Status = AccountTestRunStatusSuccess
	}
	if capture.Model != "" {
		run.ModelID = truncateString(sanitizeAccountTestRunText(capture.Model), accountTestRunMaxModelLen)
	}
	latency := capture.LatencyMs
	run.LatencyMs = &latency
	run.FirstTokenMs = capture.FirstTokenMs
	run.UpstreamStatusCode = capture.UpstreamStatusCode
	run.ErrorMessage = ""
	if !capture.Success {
		run.ErrorMessage = redactAccountErrorText(sanitizeAccountTestRunText(capture.ErrorMessage), accountTestRunMaxTextRunes)
	}
	run.ResponseText = truncateRunes(sanitizeAccountTestRunText(capture.ResponseText), accountTestRunMaxTextRunes)
	finishedAt := capture.FinishedAt
	if finishedAt.IsZero() {
		finishedAt = time.Now()
	}
	run.FinishedAt = &finishedAt
}

func accountTestRunRecoveryOf(result *SuccessfulTestRecoveryResult) *AccountTestRunRecovery {
	if result == nil {
		return nil
	}
	return &AccountTestRunRecovery{ClearedError: result.ClearedError, ClearedRateLimit: result.ClearedRateLimit}
}

// sanitizeAccountTestRunText makes upstream text storable in a PostgreSQL TEXT
// column, which rejects NUL bytes and invalid UTF-8.
func sanitizeAccountTestRunText(s string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(s, "\uFFFD"), "\x00", "")
}

func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes-1]) + "…"
}
