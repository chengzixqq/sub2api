//go:build unit

package service

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// memAccountTestRunRepo is an in-memory AccountTestRunRepository.
type memAccountTestRunRepo struct {
	mu       sync.Mutex
	nextID   int64
	runs     map[string]*AccountTestRun
	pruned   []int64
	finished chan string
}

func newMemAccountTestRunRepo() *memAccountTestRunRepo {
	return &memAccountTestRunRepo{runs: map[string]*AccountTestRun{}, finished: make(chan string, 16)}
}

func cloneAccountTestRun(r *AccountTestRun) *AccountTestRun {
	out := *r
	return &out
}

func (m *memAccountTestRunRepo) CreateRunning(_ context.Context, run *AccountTestRun) (*AccountTestRun, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.runs {
		if existing.AccountID == run.AccountID && existing.Status == AccountTestRunStatusRunning {
			return cloneAccountTestRun(existing), false, nil
		}
	}
	m.nextID++
	stored := cloneAccountTestRun(run)
	stored.ID = m.nextID
	stored.Status = AccountTestRunStatusRunning
	stored.StartedAt = time.Now()
	m.runs[stored.RunID] = stored
	return cloneAccountTestRun(stored), true, nil
}

func (m *memAccountTestRunRepo) GetRunningByAccount(_ context.Context, accountID int64) (*AccountTestRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.runs {
		if existing.AccountID == accountID && existing.Status == AccountTestRunStatusRunning {
			return cloneAccountTestRun(existing), nil
		}
	}
	return nil, ErrAccountTestRunNotFound
}

func (m *memAccountTestRunRepo) Finish(_ context.Context, run *AccountTestRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.runs[run.RunID]
	if ok && existing.Status == AccountTestRunStatusRunning {
		stored := cloneAccountTestRun(run)
		stored.ID = existing.ID
		stored.StartedAt = existing.StartedAt
		m.runs[run.RunID] = stored
	}
	m.finished <- run.RunID
	return nil
}

func (m *memAccountTestRunRepo) InsertFinished(_ context.Context, run *AccountTestRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	stored := cloneAccountTestRun(run)
	stored.ID = m.nextID
	m.runs[stored.RunID] = stored
	return nil
}

func (m *memAccountTestRunRepo) GetByRunID(_ context.Context, runID string) (*AccountTestRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[runID]
	if !ok {
		return nil, ErrAccountTestRunNotFound
	}
	return cloneAccountTestRun(run), nil
}

func (m *memAccountTestRunRepo) GetLatestByAccount(_ context.Context, accountID int64) (*AccountTestRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var list []*AccountTestRun
	for _, run := range m.runs {
		if run.AccountID == accountID {
			list = append(list, run)
		}
	}
	if len(list) == 0 {
		return nil, ErrAccountTestRunNotFound
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID > list[j].ID })
	return cloneAccountTestRun(list[0]), nil
}

func (m *memAccountTestRunRepo) InterruptStale(_ context.Context, accountID int64, olderThan time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	cutoff := time.Now().Add(-olderThan)
	for _, run := range m.runs {
		if run.Status != AccountTestRunStatusRunning || (accountID != 0 && run.AccountID != accountID) || !run.StartedAt.Before(cutoff) {
			continue
		}
		now := time.Now()
		run.Status = AccountTestRunStatusInterrupted
		run.FinishedAt = &now
		run.ErrorMessage = AccountTestRunInterruptedMessage
		n++
	}
	return n, nil
}

func (m *memAccountTestRunRepo) Prune(_ context.Context, accountID int64, _ int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruned = append(m.pruned, accountID)
	return nil
}

func (m *memAccountTestRunRepo) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.runs)
}

func (m *memAccountTestRunRepo) setStartedAt(runID string, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs[runID].StartedAt = at
}

type fakeAccountTestRunAccounts struct {
	hidden map[int64]bool
	calls  int
	mu     sync.Mutex
}

func (f *fakeAccountTestRunAccounts) GetByIDScoped(_ context.Context, id int64) (*Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.hidden[id] {
		return nil, ErrAccountNotFound
	}
	return &Account{ID: id}, nil
}

type fakeAccountTestRunRunner struct {
	mu     sync.Mutex
	run    func(ctx context.Context, in AccountTestCaptureInput) AccountTestCapture
	ctxs   []context.Context
	inputs []AccountTestCaptureInput
}

func (f *fakeAccountTestRunRunner) RunTestCaptured(ctx context.Context, in AccountTestCaptureInput) AccountTestCapture {
	f.mu.Lock()
	f.ctxs = append(f.ctxs, ctx)
	f.inputs = append(f.inputs, in)
	run := f.run
	f.mu.Unlock()
	return run(ctx, in)
}

func (f *fakeAccountTestRunRunner) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.ctxs)
}

type fakeAccountTestRunRecoverer struct {
	mu     sync.Mutex
	result *SuccessfulTestRecoveryResult
	calls  int
}

func (f *fakeAccountTestRunRecoverer) RecoverAccountAfterSuccessfulTest(context.Context, int64) (*SuccessfulTestRecoveryResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.result, nil
}

func successfulCapture(ctx context.Context, in AccountTestCaptureInput) AccountTestCapture {
	started := time.Now()
	time.Sleep(2 * time.Millisecond)
	first := int64(1)
	return AccountTestCapture{
		Success: true, Model: "claude-sonnet-4-5", ResponseText: "ok", LatencyMs: 2,
		FirstTokenMs: &first, StartedAt: started, FinishedAt: time.Now(),
	}
}

type accountTestRunFixture struct {
	svc       *AccountTestRunService
	repo      *memAccountTestRunRepo
	accounts  *fakeAccountTestRunAccounts
	runner    *fakeAccountTestRunRunner
	recoverer *fakeAccountTestRunRecoverer
}

func newAccountTestRunFixture(run func(context.Context, AccountTestCaptureInput) AccountTestCapture) *accountTestRunFixture {
	f := &accountTestRunFixture{
		repo:      newMemAccountTestRunRepo(),
		accounts:  &fakeAccountTestRunAccounts{hidden: map[int64]bool{}},
		runner:    &fakeAccountTestRunRunner{run: run},
		recoverer: &fakeAccountTestRunRecoverer{result: &SuccessfulTestRecoveryResult{ClearedError: true}},
	}
	f.svc = NewAccountTestRunService(f.repo, f.accounts, f.runner, f.recoverer)
	return f
}

func (f *accountTestRunFixture) waitFinished(t *testing.T, runID string) *AccountTestRun {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		run, err := f.repo.GetByRunID(context.Background(), runID)
		require.NoError(t, err)
		if run.Status != AccountTestRunStatusRunning {
			break
		}
		select {
		case <-f.repo.finished:
		case <-deadline:
			t.Fatal("run did not finish")
		}
	}
	f.svc.wait()
	run, err := f.repo.GetByRunID(context.Background(), runID)
	require.NoError(t, err)
	return run
}

func TestAccountTestRunStart_DeniesAccountOutsideScope(t *testing.T) {
	f := newAccountTestRunFixture(successfulCapture)
	f.accounts.hidden[12] = true

	_, err := f.svc.Start(context.Background(), StartAccountTestRunInput{AccountID: 12})

	require.ErrorIs(t, err, ErrAccountNotFound)
	require.Equal(t, "ACCOUNT_NOT_FOUND", infraerrors.Reason(err))
	require.Zero(t, f.repo.count())
	require.Zero(t, f.runner.calls())
	require.Len(t, f.svc.slots, 0)
}

func TestAccountTestRunStart_ConflictReturnsExistingRunID(t *testing.T) {
	release := make(chan struct{})
	f := newAccountTestRunFixture(func(ctx context.Context, in AccountTestCaptureInput) AccountTestCapture {
		<-release
		return successfulCapture(ctx, in)
	})
	first, err := f.svc.Start(context.Background(), StartAccountTestRunInput{AccountID: 12})
	require.NoError(t, err)

	_, err = f.svc.Start(context.Background(), StartAccountTestRunInput{AccountID: 12})

	require.Equal(t, 409, infraerrors.Code(err))
	require.Equal(t, "ACCOUNT_TEST_RUNNING", infraerrors.Reason(err))
	require.Equal(t, first.RunID, infraerrors.FromError(err).Metadata["run_id"])
	require.Len(t, f.svc.slots, 1, "the rejected start must release its worker slot")

	other, err := f.svc.Start(context.Background(), StartAccountTestRunInput{AccountID: 13})
	require.NoError(t, err)
	close(release)
	f.waitFinished(t, first.RunID)
	f.waitFinished(t, other.RunID)
}

func TestAccountTestRunStart_DetachedFromRequestContext(t *testing.T) {
	f := newAccountTestRunFixture(func(ctx context.Context, in AccountTestCaptureInput) AccountTestCapture {
		time.Sleep(20 * time.Millisecond)
		if ctx.Err() != nil {
			return AccountTestCapture{ErrorMessage: ctx.Err().Error(), FinishedAt: time.Now()}
		}
		return successfulCapture(ctx, in)
	})
	reqCtx, cancel := context.WithCancel(context.Background())
	createdBy := int64(7)

	run, err := f.svc.Start(reqCtx, StartAccountTestRunInput{AccountID: 12, ModelID: "claude-sonnet-4-5", Prompt: "hi", Mode: "compact", CreatedBy: &createdBy})
	cancel()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(run.RunID, "tr_"))
	require.Len(t, run.RunID, 35)
	require.Equal(t, AccountTestRunStatusRunning, run.Status)
	require.Equal(t, AccountTestRunSourceAsync, run.Source)
	require.Equal(t, &createdBy, run.CreatedBy)

	done := f.waitFinished(t, run.RunID)
	require.Equal(t, AccountTestRunStatusSuccess, done.Status)
	require.NotNil(t, done.LatencyMs)
	require.Greater(t, *done.LatencyMs, int64(0))
	require.NotNil(t, done.FinishedAt)
	require.Equal(t, &AccountTestRunRecovery{ClearedError: true}, done.Recovery)
	require.Equal(t, 1, f.recoverer.calls)
	require.Equal(t, []int64{12}, f.repo.pruned)

	runCtx := f.runner.ctxs[0]
	deadline, ok := runCtx.Deadline()
	require.True(t, ok)
	require.LessOrEqual(t, time.Until(deadline), accountTestRunTimeout)
	require.Equal(t, AccountTestCaptureInput{AccountID: 12, ModelID: "claude-sonnet-4-5", Prompt: "hi", Mode: "compact"}, f.runner.inputs[0])
	require.Len(t, f.svc.slots, 0)
}

func TestAccountTestRunStart_FailureRecordsStatusCodeAndKind(t *testing.T) {
	code := 429
	f := newAccountTestRunFixture(func(context.Context, AccountTestCaptureInput) AccountTestCapture {
		return AccountTestCapture{
			ErrorMessage:       "API returned 429: slow down " + strings.Repeat("x", 300) + " sk-abcdefgh1234567890abcdef",
			UpstreamStatusCode: &code,
			LatencyMs:          5,
			FinishedAt:         time.Now(),
		}
	})

	run, err := f.svc.Start(context.Background(), StartAccountTestRunInput{AccountID: 12})
	require.NoError(t, err)
	done := f.waitFinished(t, run.RunID)

	require.Equal(t, AccountTestRunStatusFailed, done.Status)
	require.Equal(t, &code, done.UpstreamStatusCode)
	require.Equal(t, AccountErrorKindRateLimit, *done.ErrorKind())
	require.NotContains(t, done.ErrorMessage, "abcdefgh1234567890")
	require.Contains(t, done.ErrorMessage, "sk-abcdefgh***")
	require.LessOrEqual(t, len([]rune(done.ErrorSummary())), 160)
	require.Nil(t, done.Recovery)
	require.Zero(t, f.recoverer.calls)
}

func TestAccountTestRunStart_RecoverOnSuccessFalseSkipsRecovery(t *testing.T) {
	f := newAccountTestRunFixture(successfulCapture)
	no := false

	run, err := f.svc.Start(context.Background(), StartAccountTestRunInput{AccountID: 12, RecoverOnSuccess: &no})
	require.NoError(t, err)
	done := f.waitFinished(t, run.RunID)

	require.Equal(t, AccountTestRunStatusSuccess, done.Status)
	require.Nil(t, done.Recovery)
	require.Zero(t, f.recoverer.calls)
	require.Nil(t, done.ErrorKind())
}

func TestAccountTestRunStart_PanicMarksRunFailed(t *testing.T) {
	f := newAccountTestRunFixture(func(context.Context, AccountTestCaptureInput) AccountTestCapture {
		panic("boom")
	})

	run, err := f.svc.Start(context.Background(), StartAccountTestRunInput{AccountID: 12})
	require.NoError(t, err)
	done := f.waitFinished(t, run.RunID)

	require.Equal(t, AccountTestRunStatusFailed, done.Status)
	require.NotEmpty(t, done.ErrorMessage)
	require.NotNil(t, done.FinishedAt)
	require.Len(t, f.svc.slots, 0)
}

func TestAccountTestRunStart_TimeoutMarksFailed(t *testing.T) {
	f := newAccountTestRunFixture(func(ctx context.Context, _ AccountTestCaptureInput) AccountTestCapture {
		<-ctx.Done()
		return AccountTestCapture{ErrorMessage: "Request failed: " + ctx.Err().Error(), FinishedAt: time.Now()}
	})
	f.svc.timeout = 20 * time.Millisecond

	run, err := f.svc.Start(context.Background(), StartAccountTestRunInput{AccountID: 12})
	require.NoError(t, err)
	done := f.waitFinished(t, run.RunID)

	require.Equal(t, AccountTestRunStatusFailed, done.Status)
	require.Contains(t, done.ErrorMessage, "timed out")
	require.Zero(t, f.recoverer.calls)
}

func TestAccountTestRunStart_BusyWhenWorkersExhausted(t *testing.T) {
	release := make(chan struct{})
	f := newAccountTestRunFixture(func(ctx context.Context, in AccountTestCaptureInput) AccountTestCapture {
		<-release
		return successfulCapture(ctx, in)
	})
	f.svc.slots = make(chan struct{}, 1)
	first, err := f.svc.Start(context.Background(), StartAccountTestRunInput{AccountID: 12})
	require.NoError(t, err)

	_, err = f.svc.Start(context.Background(), StartAccountTestRunInput{AccountID: 13})

	require.Equal(t, 429, infraerrors.Code(err))
	require.Equal(t, "ACCOUNT_TEST_BUSY", infraerrors.Reason(err))
	require.Equal(t, 1, f.repo.count(), "a busy start must not insert a running row")
	close(release)
	f.waitFinished(t, first.RunID)
}

func TestAccountTestRunStart_RunningConflictWinsOverBusyWorkers(t *testing.T) {
	release := make(chan struct{})
	f := newAccountTestRunFixture(func(ctx context.Context, in AccountTestCaptureInput) AccountTestCapture {
		<-release
		return successfulCapture(ctx, in)
	})
	f.svc.slots = make(chan struct{}, 1)
	first, err := f.svc.Start(context.Background(), StartAccountTestRunInput{AccountID: 12})
	require.NoError(t, err)

	_, err = f.svc.Start(context.Background(), StartAccountTestRunInput{AccountID: 12})

	require.Equal(t, 409, infraerrors.Code(err), "a running test of the same account is reported even when workers are full")
	require.Equal(t, "ACCOUNT_TEST_RUNNING", infraerrors.Reason(err))
	require.Equal(t, first.RunID, infraerrors.FromError(err).Metadata["run_id"])
	require.Len(t, f.svc.slots, 1, "the rejected start must not hold a worker slot")
	require.Equal(t, 1, f.repo.count())
	close(release)
	f.waitFinished(t, first.RunID)
}

func TestAccountTestRunStart_SanitizesModelIDForStorage(t *testing.T) {
	release := make(chan struct{})
	f := newAccountTestRunFixture(func(ctx context.Context, in AccountTestCaptureInput) AccountTestCapture {
		<-release
		return successfulCapture(ctx, in)
	})

	run, err := f.svc.Start(context.Background(), StartAccountTestRunInput{AccountID: 12, ModelID: "claude\x00x"})
	require.NoError(t, err)

	require.Equal(t, "claudex", run.ModelID, "PostgreSQL rejects NUL bytes in model_id")
	stored, err := f.repo.GetByRunID(context.Background(), run.RunID)
	require.NoError(t, err)
	require.NotContains(t, stored.ModelID, "\x00")
	close(release)
	f.waitFinished(t, run.RunID)
}

func TestAccountTestRunGet_HidesOtherWorkspaceRun(t *testing.T) {
	f := newAccountTestRunFixture(successfulCapture)
	run, err := f.svc.Start(context.Background(), StartAccountTestRunInput{AccountID: 12})
	require.NoError(t, err)
	f.waitFinished(t, run.RunID)

	got, err := f.svc.Get(context.Background(), run.RunID)
	require.NoError(t, err)
	require.Equal(t, run.RunID, got.RunID)

	f.accounts.hidden[12] = true
	_, err = f.svc.Get(context.Background(), run.RunID)
	require.Equal(t, "ACCOUNT_TEST_RUN_NOT_FOUND", infraerrors.Reason(err))
	_, err = f.svc.Latest(context.Background(), 12)
	require.Equal(t, "ACCOUNT_TEST_RUN_NOT_FOUND", infraerrors.Reason(err))

	_, err = f.svc.Get(context.Background(), "not-a-run")
	require.Equal(t, "ACCOUNT_TEST_RUN_NOT_FOUND", infraerrors.Reason(err))
	_, err = f.svc.Get(context.Background(), "tr_missing")
	require.Equal(t, "ACCOUNT_TEST_RUN_NOT_FOUND", infraerrors.Reason(err))
}

func TestAccountTestRunLatest_InterruptsStaleRunning(t *testing.T) {
	f := newAccountTestRunFixture(successfulCapture)
	_, err := f.svc.Latest(context.Background(), 12)
	require.Equal(t, "ACCOUNT_TEST_RUN_NOT_FOUND", infraerrors.Reason(err))

	stale, created, err := f.repo.CreateRunning(context.Background(), &AccountTestRun{RunID: newAccountTestRunID(), AccountID: 12, Source: AccountTestRunSourceAsync})
	require.NoError(t, err)
	require.True(t, created)
	f.repo.setStartedAt(stale.RunID, time.Now().Add(-AccountTestRunStaleAfter-time.Minute))

	latest, err := f.svc.Latest(context.Background(), 12)
	require.NoError(t, err)
	require.Equal(t, stale.RunID, latest.RunID)
	require.Equal(t, AccountTestRunStatusInterrupted, latest.Status)

	fresh, _, err := f.repo.CreateRunning(context.Background(), &AccountTestRun{RunID: newAccountTestRunID(), AccountID: 13, Source: AccountTestRunSourceAsync})
	require.NoError(t, err)
	got, err := f.svc.Get(context.Background(), fresh.RunID)
	require.NoError(t, err)
	require.Equal(t, AccountTestRunStatusRunning, got.Status, "a live run is not interrupted")
}

func TestAccountTestRunRecordManual_InterruptedWhenClientGone(t *testing.T) {
	f := newAccountTestRunFixture(successfulCapture)
	started := time.Now().Add(-time.Second)
	createdBy := int64(3)

	require.NoError(t, f.svc.RecordManual(context.Background(), ManualAccountTestRun{
		AccountID: 12,
		Capture:   AccountTestCapture{Success: true, Model: "m", ResponseText: "hello", LatencyMs: 900, StartedAt: started, FinishedAt: time.Now()},
		Recovery:  &SuccessfulTestRecoveryResult{ClearedRateLimit: true},
		CreatedBy: &createdBy,
	}))
	ok, err := f.repo.GetLatestByAccount(context.Background(), 12)
	require.NoError(t, err)
	require.Equal(t, AccountTestRunSourceManual, ok.Source)
	require.Equal(t, AccountTestRunStatusSuccess, ok.Status)
	require.Equal(t, "m", ok.ModelID)
	require.Equal(t, &AccountTestRunRecovery{ClearedRateLimit: true}, ok.Recovery)
	require.Equal(t, &createdBy, ok.CreatedBy)
	require.True(t, ok.StartedAt.Equal(started))
	require.Equal(t, []int64{12}, f.repo.pruned)

	// A failure after the client left is most likely the cancellation itself.
	require.NoError(t, f.svc.RecordManual(context.Background(), ManualAccountTestRun{
		AccountID:   12,
		Capture:     AccountTestCapture{Model: "m", ErrorMessage: "Request failed: context canceled", StartedAt: time.Now(), FinishedAt: time.Now()},
		Interrupted: true,
	}))
	gone, err := f.repo.GetLatestByAccount(context.Background(), 12)
	require.NoError(t, err)
	require.Equal(t, AccountTestRunStatusInterrupted, gone.Status)
	require.Equal(t, AccountTestRunInterruptedMessage, gone.ErrorMessage)
	require.Nil(t, gone.Recovery)
	require.Nil(t, gone.ErrorKind())
	require.NotNil(t, gone.FinishedAt)

	// A test that completed before the client left keeps its real outcome.
	require.NoError(t, f.svc.RecordManual(context.Background(), ManualAccountTestRun{
		AccountID:   12,
		Capture:     AccountTestCapture{Success: true, Model: "m", StartedAt: time.Now(), FinishedAt: time.Now()},
		Interrupted: true,
	}))
	done, err := f.repo.GetLatestByAccount(context.Background(), 12)
	require.NoError(t, err)
	require.Equal(t, AccountTestRunStatusSuccess, done.Status)
}

func TestAccountTestRunText_SanitizedForStorage(t *testing.T) {
	run := &AccountTestRun{}
	applyAccountTestCapture(run, AccountTestCapture{
		Success:      false,
		ErrorMessage: "bad\x00byte \xff",
		ResponseText: strings.Repeat("字", 2500) + "\x00",
		FinishedAt:   time.Now(),
	})
	require.NotContains(t, run.ErrorMessage, "\x00")
	require.NotContains(t, run.ResponseText, "\x00")
	require.LessOrEqual(t, len([]rune(run.ResponseText)), accountTestRunMaxTextRunes)
	require.True(t, strings.HasSuffix(run.ResponseText, "…"))
}

func TestAccountTestRunService_NilDependencies(t *testing.T) {
	svc := NewAccountTestRunService(nil, nil, nil, nil)
	_, err := svc.Start(context.Background(), StartAccountTestRunInput{AccountID: 1})
	require.True(t, errors.Is(err, ErrServiceUnavailable))
}
