package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// accountTestRunWriteTimeout bounds each detached database write of a run.
const accountTestRunWriteTimeout = 5 * time.Second

type accountTestRunAccountReader interface {
	GetByIDScoped(ctx context.Context, id int64) (*Account, error)
}

type accountTestRunRunner interface {
	RunTestCaptured(ctx context.Context, in AccountTestCaptureInput) AccountTestCapture
}

type accountTestRunRecoverer interface {
	RecoverAccountAfterSuccessfulTest(ctx context.Context, accountID int64) (*SuccessfulTestRecoveryResult, error)
}

// AccountTestRunService runs account tests detached from the request and
// stores every outcome (async and interactive) in account_test_runs. The state
// lives in the database, so any instance can answer reads.
type AccountTestRunService struct {
	repo      AccountTestRunRepository
	accounts  accountTestRunAccountReader
	runner    accountTestRunRunner
	recoverer accountTestRunRecoverer
	// slots caps async runs on this instance; a full channel means busy.
	slots   chan struct{}
	timeout time.Duration
	running sync.WaitGroup
}

// NewAccountTestRunService creates an AccountTestRunService.
func NewAccountTestRunService(repo AccountTestRunRepository, accounts accountTestRunAccountReader, runner accountTestRunRunner, recoverer accountTestRunRecoverer) *AccountTestRunService {
	return &AccountTestRunService{
		repo:      repo,
		accounts:  accounts,
		runner:    runner,
		recoverer: recoverer,
		slots:     make(chan struct{}, accountTestRunMaxWorkers),
		timeout:   accountTestRunTimeout,
	}
}

// ProvideAccountTestRunService wires the service and marks runs a previous
// process left running as interrupted once they are past the stale window
// (time based, so runs of other live instances are untouched).
func ProvideAccountTestRunService(repo AccountTestRunRepository, accountRepo AccountRepository, accountTestSvc *AccountTestService, rateLimitSvc *RateLimitService) *AccountTestRunService {
	var runner accountTestRunRunner
	if accountTestSvc != nil {
		runner = accountTestSvc
	}
	var recoverer accountTestRunRecoverer
	if rateLimitSvc != nil {
		recoverer = rateLimitSvc
	}
	svc := NewAccountTestRunService(repo, accountRepo, runner, recoverer)
	if repo != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := repo.InterruptStale(ctx, 0, AccountTestRunStaleAfter); err != nil {
				logger.LegacyPrintf("service.account_test_run", "[AccountTestRun] startup stale sweep failed: %v", err)
			}
		}()
	}
	return svc
}

func (s *AccountTestRunService) ready() bool {
	return s != nil && s.repo != nil && s.accounts != nil && s.runner != nil
}

// Start checks the account is in scope, takes a worker slot and stores a
// running row, then runs the test in the background. A second start for an
// account with a running test returns ErrAccountTestRunning with metadata
// run_id, even when the worker pool is full; otherwise a full pool returns
// ErrAccountTestBusy.
func (s *AccountTestRunService) Start(ctx context.Context, in StartAccountTestRunInput) (*AccountTestRun, error) {
	if !s.ready() {
		return nil, ErrServiceUnavailable
	}
	if _, err := s.accounts.GetByIDScoped(ctx, in.AccountID); err != nil {
		return nil, err
	}
	if _, err := s.repo.InterruptStale(ctx, in.AccountID, AccountTestRunStaleAfter); err != nil {
		return nil, err
	}
	select {
	case s.slots <- struct{}{}:
	default:
		return nil, s.busyError(ctx, in.AccountID)
	}
	run, created, err := s.repo.CreateRunning(ctx, &AccountTestRun{
		RunID:     newAccountTestRunID(),
		AccountID: in.AccountID,
		Source:    AccountTestRunSourceAsync,
		Status:    AccountTestRunStatusRunning,
		ModelID:   truncateString(sanitizeAccountTestRunText(in.ModelID), accountTestRunMaxModelLen),
		CreatedBy: in.CreatedBy,
	})
	if err != nil || !created {
		<-s.slots
		if err != nil {
			return nil, err
		}
		return nil, accountTestRunningError(run.RunID)
	}
	recoverOnSuccess := in.RecoverOnSuccess == nil || *in.RecoverOnSuccess
	captureIn := AccountTestCaptureInput{AccountID: in.AccountID, ModelID: in.ModelID, Prompt: in.Prompt, Mode: in.Mode}
	s.running.Add(1)
	go s.execute(cloneRun(run), captureIn, recoverOnSuccess)
	return run, nil
}

// busyError answers a start that found the worker pool full: a running test
// of the same account is reported as a conflict with its run_id, so the client
// can poll it instead of retrying.
func (s *AccountTestRunService) busyError(ctx context.Context, accountID int64) error {
	existing, err := s.repo.GetRunningByAccount(ctx, accountID)
	switch {
	case err == nil:
		return accountTestRunningError(existing.RunID)
	case errors.Is(err, ErrAccountTestRunNotFound):
		return ErrAccountTestBusy
	default:
		return err
	}
}

func accountTestRunningError(runID string) error {
	return ErrAccountTestRunning.WithMetadata(map[string]string{"run_id": runID})
}

// execute runs one async test; it never lets a panic escape and always writes
// the outcome with a fresh context.
func (s *AccountTestRunService) execute(run *AccountTestRun, in AccountTestCaptureInput, recoverOnSuccess bool) {
	defer s.running.Done()
	defer func() { <-s.slots }()
	startedAt := time.Now()
	capture := s.runCapture(in, startedAt)
	applyAccountTestCapture(run, capture)
	if capture.Success && recoverOnSuccess && s.recoverer != nil {
		run.Recovery = s.recoverAccount(run.AccountID)
	}
	s.persist(run.RunID, run.AccountID, func(ctx context.Context) error { return s.repo.Finish(ctx, run) })
}

func (s *AccountTestRunService) runCapture(in AccountTestCaptureInput, startedAt time.Time) (capture AccountTestCapture) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			logger.LegacyPrintf("service.account_test_run", "[AccountTestRun] account=%d test panicked: %v", in.AccountID, r)
			capture = failedAccountTestCapture(in.ModelID, startedAt, fmt.Sprintf("Account test crashed: %v", r))
		}
	}()
	capture = s.runner.RunTestCaptured(ctx, in)
	if !capture.Success && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		capture.ErrorMessage = fmt.Sprintf("Account test timed out after %s: %s", s.timeout, capture.ErrorMessage)
	}
	return capture
}

func (s *AccountTestRunService) recoverAccount(accountID int64) *AccountTestRunRecovery {
	ctx, cancel := context.WithTimeout(context.Background(), accountTestRunWriteTimeout)
	defer cancel()
	result, err := s.recoverer.RecoverAccountAfterSuccessfulTest(ctx, accountID)
	if err != nil {
		logger.LegacyPrintf("service.account_test_run", "[AccountTestRun] account=%d recovery failed: %v", accountID, err)
		return nil
	}
	return accountTestRunRecoveryOf(result)
}

func (s *AccountTestRunService) persist(runID string, accountID int64, write func(ctx context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), accountTestRunWriteTimeout)
	defer cancel()
	if err := write(ctx); err != nil {
		logger.LegacyPrintf("service.account_test_run", "[AccountTestRun] account=%d run=%s write failed: %v", accountID, runID, err)
		return
	}
	if err := s.repo.Prune(ctx, accountID, AccountTestRunKeepPerAccount); err != nil {
		logger.LegacyPrintf("service.account_test_run", "[AccountTestRun] account=%d prune failed: %v", accountID, err)
	}
}

// Latest returns the newest run of an account in scope.
func (s *AccountTestRunService) Latest(ctx context.Context, accountID int64) (*AccountTestRun, error) {
	if !s.ready() {
		return nil, ErrServiceUnavailable
	}
	if err := s.checkRunAccount(ctx, accountID); err != nil {
		return nil, err
	}
	if _, err := s.repo.InterruptStale(ctx, accountID, AccountTestRunStaleAfter); err != nil {
		return nil, err
	}
	return s.repo.GetLatestByAccount(ctx, accountID)
}

// Get returns one run; runs of accounts outside the scope read as not found.
func (s *AccountTestRunService) Get(ctx context.Context, runID string) (*AccountTestRun, error) {
	if !s.ready() {
		return nil, ErrServiceUnavailable
	}
	if !IsAccountTestRunID(runID) {
		return nil, ErrAccountTestRunNotFound
	}
	run, err := s.repo.GetByRunID(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := s.checkRunAccount(ctx, run.AccountID); err != nil {
		return nil, err
	}
	if run.Status == AccountTestRunStatusRunning && time.Since(run.StartedAt) > AccountTestRunStaleAfter {
		if _, err := s.repo.InterruptStale(ctx, run.AccountID, AccountTestRunStaleAfter); err != nil {
			return nil, err
		}
		return s.repo.GetByRunID(ctx, runID)
	}
	return run, nil
}

// checkRunAccount reads an account outside the scope as a missing run, so the
// run endpoints are not an account existence probe.
func (s *AccountTestRunService) checkRunAccount(ctx context.Context, accountID int64) error {
	_, err := s.accounts.GetByIDScoped(ctx, accountID)
	if errors.Is(err, ErrAccountNotFound) {
		return ErrAccountTestRunNotFound
	}
	return err
}

// RecordManual stores the outcome of an interactive SSE test. A failure after
// the client left is recorded as interrupted: the cancellation most likely
// caused it.
func (s *AccountTestRunService) RecordManual(ctx context.Context, in ManualAccountTestRun) error {
	if s == nil || s.repo == nil {
		return nil
	}
	run := &AccountTestRun{
		RunID:     newAccountTestRunID(),
		AccountID: in.AccountID,
		Source:    AccountTestRunSourceManual,
		StartedAt: in.Capture.StartedAt,
		CreatedBy: in.CreatedBy,
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now()
	}
	applyAccountTestCapture(run, in.Capture)
	switch {
	case in.Interrupted && !in.Capture.Success:
		run.Status = AccountTestRunStatusInterrupted
		run.ErrorMessage = AccountTestRunInterruptedMessage
	case in.Capture.Success:
		run.Recovery = accountTestRunRecoveryOf(in.Recovery)
	}
	if err := s.repo.InsertFinished(ctx, run); err != nil {
		return err
	}
	return s.repo.Prune(ctx, run.AccountID, AccountTestRunKeepPerAccount)
}

// wait blocks until background runs started so far have finished (tests).
func (s *AccountTestRunService) wait() {
	s.running.Wait()
}

func failedAccountTestCapture(modelID string, startedAt time.Time, message string) AccountTestCapture {
	finishedAt := time.Now()
	return AccountTestCapture{
		Model:        modelID,
		ErrorMessage: message,
		LatencyMs:    nonNegativeMs(finishedAt.Sub(startedAt)),
		StartedAt:    startedAt,
		FinishedAt:   finishedAt,
	}
}

func cloneRun(run *AccountTestRun) *AccountTestRun {
	out := *run
	return &out
}
