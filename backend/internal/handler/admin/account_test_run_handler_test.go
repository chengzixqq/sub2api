package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stubAccountTestRunAPI struct {
	startIn  service.StartAccountTestRunInput
	startErr error
	run      *service.AccountTestRun
	getErr   error
	gotRunID string
	latestID int64
}

func (s *stubAccountTestRunAPI) Start(_ context.Context, in service.StartAccountTestRunInput) (*service.AccountTestRun, error) {
	s.startIn = in
	if s.startErr != nil {
		return nil, s.startErr
	}
	return s.run, nil
}

func (s *stubAccountTestRunAPI) Latest(_ context.Context, accountID int64) (*service.AccountTestRun, error) {
	s.latestID = accountID
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.run, nil
}

func (s *stubAccountTestRunAPI) Get(_ context.Context, runID string) (*service.AccountTestRun, error) {
	s.gotRunID = runID
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.run, nil
}

func newAccountTestRunRouter(api accountTestRunAPI) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := &AccountTestRunHandler{runs: api}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 9})
		c.Next()
	})
	r.POST("/api/v1/admin/accounts/:id/test-runs", h.Start)
	r.GET("/api/v1/admin/accounts/:id/test-runs/latest", h.Latest)
	r.GET("/api/v1/admin/account-test-runs/:run_id", h.Get)
	return r
}

func doAccountTestRunRequest(r *gin.Engine, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func runningAccountTestRun() *service.AccountTestRun {
	return &service.AccountTestRun{
		RunID:     "tr_0192a0b1c2d37e4f8a9b0c1d2e3f4a5b",
		AccountID: 12,
		Source:    service.AccountTestRunSourceAsync,
		Status:    service.AccountTestRunStatusRunning,
		ModelID:   "claude-sonnet-4-5",
		StartedAt: time.Date(2026, 10, 17, 9, 40, 0, 0, time.UTC),
	}
}

func TestAccountTestRunHandler_StartReturns202(t *testing.T) {
	api := &stubAccountTestRunAPI{run: runningAccountTestRun()}
	r := newAccountTestRunRouter(api)

	rec, body := doAccountTestRunRequest(r, http.MethodPost, "/api/v1/admin/accounts/12/test-runs", "")

	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Equal(t, float64(0), body["code"])
	require.Equal(t, "accepted", body["message"])
	data := body["data"].(map[string]any)
	require.Equal(t, "running", data["status"])
	require.True(t, strings.HasPrefix(data["run_id"].(string), "tr_"))
	require.Equal(t, float64(12), data["account_id"])
	require.Equal(t, int64(12), api.startIn.AccountID)
	require.Nil(t, api.startIn.RecoverOnSuccess, "an absent flag defaults to recovering")
	require.NotNil(t, api.startIn.CreatedBy)
	require.Equal(t, int64(9), *api.startIn.CreatedBy)

	_, _ = doAccountTestRunRequest(r, http.MethodPost, "/api/v1/admin/accounts/12/test-runs",
		`{"model_id":"gpt-5","prompt":"hi","mode":"compact","recover_on_success":false}`)
	require.Equal(t, "gpt-5", api.startIn.ModelID)
	require.Equal(t, "hi", api.startIn.Prompt)
	require.Equal(t, "compact", api.startIn.Mode)
	require.NotNil(t, api.startIn.RecoverOnSuccess)
	require.False(t, *api.startIn.RecoverOnSuccess)

	rec, _ = doAccountTestRunRequest(r, http.MethodPost, "/api/v1/admin/accounts/12/test-runs", `{"model_id":`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	rec, _ = doAccountTestRunRequest(r, http.MethodPost, "/api/v1/admin/accounts/abc/test-runs", "")
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAccountTestRunHandler_ConflictAndBusyReasons(t *testing.T) {
	api := &stubAccountTestRunAPI{startErr: service.ErrAccountTestRunning.WithMetadata(map[string]string{"run_id": "tr_existing"})}
	r := newAccountTestRunRouter(api)

	rec, body := doAccountTestRunRequest(r, http.MethodPost, "/api/v1/admin/accounts/12/test-runs", "")
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "ACCOUNT_TEST_RUNNING", body["reason"])
	require.Equal(t, "tr_existing", body["metadata"].(map[string]any)["run_id"])
	require.NotContains(t, body, "data")

	api.startErr = service.ErrAccountTestBusy
	rec, body = doAccountTestRunRequest(r, http.MethodPost, "/api/v1/admin/accounts/12/test-runs", "")
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, "ACCOUNT_TEST_BUSY", body["reason"])

	api.getErr = service.ErrAccountTestRunNotFound
	rec, body = doAccountTestRunRequest(r, http.MethodGet, "/api/v1/admin/accounts/12/test-runs/latest", "")
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "ACCOUNT_TEST_RUN_NOT_FOUND", body["reason"])
	rec, body = doAccountTestRunRequest(r, http.MethodGet, "/api/v1/admin/account-test-runs/tr_missing", "")
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "ACCOUNT_TEST_RUN_NOT_FOUND", body["reason"])
	require.Equal(t, "tr_missing", api.gotRunID)
	rec, _ = doAccountTestRunRequest(r, http.MethodGet, "/api/v1/admin/accounts/x/test-runs/latest", "")
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAccountTestRunHandler_ViewJSONShape(t *testing.T) {
	latency, firstToken, code := int64(3420), int64(1210), 200
	finishedAt := time.Date(2026, 10, 17, 9, 40, 3, 0, time.UTC)
	done := runningAccountTestRun()
	done.Status = service.AccountTestRunStatusSuccess
	done.LatencyMs = &latency
	done.FirstTokenMs = &firstToken
	done.UpstreamStatusCode = &code
	done.ResponseText = "pong"
	done.Recovery = &service.AccountTestRunRecovery{ClearedError: true}
	done.FinishedAt = &finishedAt
	api := &stubAccountTestRunAPI{run: done}
	r := newAccountTestRunRouter(api)

	rec, body := doAccountTestRunRequest(r, http.MethodGet, "/api/v1/admin/accounts/12/test-runs/latest", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(12), api.latestID)
	data := body["data"].(map[string]any)
	for _, key := range []string{"run_id", "account_id", "source", "status", "model", "latency_ms", "first_token_ms",
		"upstream_status_code", "error_kind", "error_summary", "error_message", "response_text", "recovery", "started_at", "finished_at"} {
		require.Contains(t, data, key)
	}
	require.Equal(t, "async", data["source"])
	require.Equal(t, "claude-sonnet-4-5", data["model"])
	require.Equal(t, float64(3420), data["latency_ms"])
	require.Equal(t, float64(200), data["upstream_status_code"])
	require.Nil(t, data["error_kind"])
	require.Equal(t, "", data["error_summary"])
	require.Equal(t, map[string]any{"cleared_error": true, "cleared_rate_limit": false}, data["recovery"])
	require.Equal(t, "2026-10-17T09:40:03Z", data["finished_at"])

	failed := runningAccountTestRun()
	failed.Status = service.AccountTestRunStatusFailed
	failed.ErrorMessage = "API returned 401: invalid key"
	failed.FinishedAt = &finishedAt
	api.run = failed
	_, body = doAccountTestRunRequest(r, http.MethodGet, "/api/v1/admin/account-test-runs/"+failed.RunID, "")
	data = body["data"].(map[string]any)
	require.Equal(t, service.AccountErrorKindAuth, data["error_kind"])
	require.Equal(t, "API returned 401: invalid key", data["error_summary"])

	api.run = runningAccountTestRun()
	_, body = doAccountTestRunRequest(r, http.MethodGet, "/api/v1/admin/account-test-runs/"+api.run.RunID, "")
	data = body["data"].(map[string]any)
	require.Nil(t, data["latency_ms"])
	require.Nil(t, data["finished_at"])
	require.Nil(t, data["recovery"])
	require.Nil(t, data["first_token_ms"])
	require.Nil(t, data["upstream_status_code"])
}

type recordingManualRuns struct {
	mu    sync.Mutex
	calls []service.ManualAccountTestRun
	ctxOK []bool
}

func (r *recordingManualRuns) RecordManual(ctx context.Context, in service.ManualAccountTestRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, in)
	r.ctxOK = append(r.ctxOK, ctx.Err() == nil)
	return nil
}

func runAccountTestWithRecorder(t *testing.T, admin *accountTestResultAdmin, recorder accountTestRunRecorder, ctx context.Context) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	testSvc := service.NewAccountTestService(admin.repo, nil, nil, nil, nil, nil, &config.Config{}, nil)
	rateLimit := service.NewRateLimitService(admin.repo, nil, &config.Config{}, nil, nil)
	h := NewAccountHandler(admin, nil, nil, nil, nil, nil, rateLimit, nil, testSvc, nil, nil, nil, nil, nil)
	h.testRuns = recorder
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 5})
		c.Next()
	})
	r.POST("/api/v1/admin/accounts/:id/test", h.Test)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/"+jsonInt(admin.repo.account.ID)+"/test", strings.NewReader(`{}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func syntheticRecordedAccount(id int64) *accountTestResultRepo {
	errAt := time.Now().Add(-time.Hour)
	return &accountTestResultRepo{account: service.Account{
		ID: id, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth,
		Status: service.StatusError, ErrorMessage: "Authentication failed (401): old", ErrorAt: &errAt,
		Extra: map[string]any{"synthetic_ui_test": true},
	}}
}

func TestAccountHandlerTestRecordsManualRun(t *testing.T) {
	repo := syntheticRecordedAccount(21)
	admin := &accountTestResultAdmin{stubAdminService: newStubAdminService(), repo: repo}
	recorder := &recordingManualRuns{}

	rec := runAccountTestWithRecorder(t, admin, recorder, context.Background())

	require.Equal(t, http.StatusOK, rec.Code)
	events := sseEvents(t, rec.Body.String())
	require.Equal(t, "test_result", events[len(events)-1]["type"], "the SSE stream is unchanged")
	require.Len(t, recorder.calls, 1)
	got := recorder.calls[0]
	require.True(t, recorder.ctxOK[0])
	require.Equal(t, int64(21), got.AccountID)
	require.True(t, got.Capture.Success)
	require.NotEmpty(t, got.Capture.Model)
	require.NotEmpty(t, got.Capture.ResponseText)
	require.NotNil(t, got.Capture.FirstTokenMs)
	require.False(t, got.Interrupted)
	require.NotNil(t, got.Recovery)
	require.True(t, got.Recovery.ClearedError)
	require.NotNil(t, got.CreatedBy)
	require.Equal(t, int64(5), *got.CreatedBy)
}

func TestAccountHandlerTestRecordsInterruptedWhenClientGone(t *testing.T) {
	repo := syntheticRecordedAccount(22)
	admin := &accountTestResultAdmin{stubAdminService: newStubAdminService(), repo: repo}
	recorder := &recordingManualRuns{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rec := runAccountTestWithRecorder(t, admin, recorder, ctx)

	require.Equal(t, -1, eventIndex(sseEvents(t, rec.Body.String()), "test_result"))
	require.Len(t, recorder.calls, 1)
	require.True(t, recorder.calls[0].Interrupted)
	require.True(t, recorder.ctxOK[0], "the record is written with a context detached from the gone client")
}

func TestAccountHandlerTestWithoutRecorderKeepsBehavior(t *testing.T) {
	repo := syntheticRecordedAccount(23)
	admin := &accountTestResultAdmin{stubAdminService: newStubAdminService(), repo: repo}

	rec := runAccountTestWithRecorder(t, admin, nil, context.Background())

	events := sseEvents(t, rec.Body.String())
	require.Equal(t, "test_result", events[len(events)-1]["type"])
}

func TestAccountHandlerTestScopeDeniedRecordsNothing(t *testing.T) {
	repo := syntheticRecordedAccount(24)
	admin := &accountTestResultAdmin{stubAdminService: newStubAdminService(), repo: repo, getErr: service.ErrAccountNotFound}
	recorder := &recordingManualRuns{}

	rec := runAccountTestWithRecorder(t, admin, recorder, context.Background())

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Empty(t, recorder.calls)
}
