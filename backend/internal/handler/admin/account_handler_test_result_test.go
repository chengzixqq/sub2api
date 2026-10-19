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
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// accountTestResultRepo is the single source of account state for both the
// admin service stub (before / after snapshots) and the services under test.
type accountTestResultRepo struct {
	service.AccountRepository
	mu           sync.Mutex
	account      service.Account
	getByIDCalls int
	clearedError bool
}

func (r *accountTestResultRepo) snapshot() *service.Account {
	r.mu.Lock()
	defer r.mu.Unlock()
	acc := r.account
	return &acc
}

func (r *accountTestResultRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	r.mu.Lock()
	r.getByIDCalls++
	r.mu.Unlock()
	if id != r.account.ID {
		return nil, service.ErrAccountNotFound
	}
	return r.snapshot(), nil
}

func (r *accountTestResultRepo) SetError(_ context.Context, _ int64, msg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	r.account.Status = service.StatusError
	r.account.ErrorMessage = msg
	r.account.ErrorAt = &now
	r.account.Schedulable = false
	return nil
}

func (r *accountTestResultRepo) ClearError(_ context.Context, _ int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.account.Status = service.StatusActive
	r.account.ErrorMessage = ""
	r.account.ErrorAt = nil
	r.clearedError = true
	return nil
}

func (r *accountTestResultRepo) UpdateExtra(_ context.Context, _ int64, _ map[string]any) error {
	return nil
}

type accountTestResultAdmin struct {
	*stubAdminService
	repo   *accountTestResultRepo
	getErr error
}

func (s *accountTestResultAdmin) GetAccount(_ context.Context, id int64) (*service.Account, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if id != s.repo.account.ID {
		return nil, service.ErrAccountNotFound
	}
	return s.repo.snapshot(), nil
}

type accountTestResultUpstream struct {
	status int
	body   string
}

func (u *accountTestResultUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return &http.Response{StatusCode: u.status, Header: make(http.Header), Body: ioNopCloser(u.body)}, nil
}

func (u *accountTestResultUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func ioNopCloser(body string) *readCloser { return &readCloser{Reader: strings.NewReader(body)} }

type readCloser struct{ *strings.Reader }

func (readCloser) Close() error { return nil }

func runAccountTest(t *testing.T, admin *accountTestResultAdmin, upstream service.HTTPUpstream, ctx context.Context) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	testSvc := service.NewAccountTestService(admin.repo, nil, nil, nil, nil, upstream, &config.Config{}, nil)
	rateLimit := service.NewRateLimitService(admin.repo, nil, &config.Config{}, nil, nil)
	h := NewAccountHandler(admin, nil, nil, nil, nil, nil, rateLimit, nil, testSvc, nil, nil, nil, nil, nil)
	r := gin.New()
	r.POST("/api/v1/admin/accounts/:id/test", h.Test)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/"+jsonInt(admin.repo.account.ID)+"/test", strings.NewReader(`{}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func jsonInt(v int64) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func sseEvents(t *testing.T, body string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event map[string]any
		require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event), line)
		events = append(events, event)
	}
	return events
}

func eventIndex(events []map[string]any, eventType string) int {
	for i, event := range events {
		if event["type"] == eventType {
			return i
		}
	}
	return -1
}

func TestAccountHandlerTest_SuccessAppendsTestResultWithRecovery(t *testing.T) {
	errAt := time.Now().Add(-time.Hour)
	repo := &accountTestResultRepo{account: service.Account{
		ID:           12,
		Name:         "synthetic",
		Platform:     service.PlatformAnthropic,
		Type:         service.AccountTypeOAuth,
		Status:       service.StatusError,
		ErrorMessage: "Authentication failed (401): old",
		ErrorAt:      &errAt,
		Schedulable:  false,
		Extra:        map[string]any{"synthetic_ui_test": true},
	}}
	admin := &accountTestResultAdmin{stubAdminService: newStubAdminService(), repo: repo}

	rec := runAccountTest(t, admin, nil, context.Background())

	require.Equal(t, http.StatusOK, rec.Code)
	events := sseEvents(t, rec.Body.String())
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	require.Equal(t, "test_result", last["type"])
	require.Equal(t, true, last["success"])
	completeAt := eventIndex(events, "test_complete")
	require.GreaterOrEqual(t, completeAt, 0)
	require.Less(t, completeAt, len(events)-1)

	data := last["data"].(map[string]any)
	require.Equal(t, true, data["success"])
	require.GreaterOrEqual(t, data["total_ms"].(float64), float64(0))
	require.NotNil(t, data["first_token_ms"])
	require.Nil(t, data["upstream_status"], "synthetic accounts never reach an upstream")
	recovery := data["recovery"].(map[string]any)
	require.Equal(t, true, recovery["cleared_error"])
	require.Equal(t, false, recovery["cleared_rate_limit"])
	account := data["account"].(map[string]any)
	require.Equal(t, service.StatusActive, account["status"])
	require.Equal(t, false, account["schedulable"])
	require.Equal(t, "", account["error_summary"])
	require.Equal(t, map[string]any{"error": false, "rate_limited": false, "temp_unschedulable": false}, data["marked"])
	require.True(t, repo.clearedError)
}

func TestAccountHandlerTest_FailureStillSendsTestResult(t *testing.T) {
	repo := &accountTestResultRepo{account: service.Account{
		ID:          13,
		Name:        "openai",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token"},
	}}
	admin := &accountTestResultAdmin{stubAdminService: newStubAdminService(), repo: repo}

	rec := runAccountTest(t, admin, &accountTestResultUpstream{status: http.StatusUnauthorized, body: `{"error":"bad token"}`}, context.Background())

	events := sseEvents(t, rec.Body.String())
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	require.Equal(t, "test_result", last["type"])
	require.NotContains(t, last, "success")
	errorAt := eventIndex(events, "error")
	require.GreaterOrEqual(t, errorAt, 0)
	require.Less(t, errorAt, len(events)-1)

	data := last["data"].(map[string]any)
	require.Equal(t, false, data["success"])
	require.Nil(t, data["recovery"])
	require.Equal(t, float64(http.StatusUnauthorized), data["upstream_status"])
	require.NotContains(t, data, "usage")
	account := data["account"].(map[string]any)
	require.Equal(t, service.StatusError, account["status"])
	require.Equal(t, service.AccountErrorKindAuth, account["error_kind"])
	marked := data["marked"].(map[string]any)
	require.Equal(t, true, marked["error"])
	require.Equal(t, false, marked["rate_limited"])
	require.Equal(t, false, repo.clearedError, "a failed test must not run recovery")
}

func TestAccountHandlerTest_ClientGoneSkipsTestResult(t *testing.T) {
	repo := &accountTestResultRepo{account: service.Account{
		ID:       14,
		Platform: service.PlatformAnthropic,
		Type:     service.AccountTypeOAuth,
		Status:   service.StatusActive,
		Extra:    map[string]any{"synthetic_ui_test": true},
	}}
	admin := &accountTestResultAdmin{stubAdminService: newStubAdminService(), repo: repo}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rec := runAccountTest(t, admin, nil, ctx)

	events := sseEvents(t, rec.Body.String())
	require.GreaterOrEqual(t, eventIndex(events, "test_complete"), 0)
	require.Equal(t, -1, eventIndex(events, "test_result"))
}

func TestAccountHandlerTest_ScopeDeniedReturnsJSONWithoutStream(t *testing.T) {
	repo := &accountTestResultRepo{account: service.Account{ID: 15, Status: service.StatusActive}}
	admin := &accountTestResultAdmin{stubAdminService: newStubAdminService(), repo: repo, getErr: service.ErrAccountNotFound}

	rec := runAccountTest(t, admin, nil, context.Background())

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.NotContains(t, rec.Body.String(), "test_result")
	require.NotContains(t, rec.Body.String(), "data: ")
	require.Zero(t, repo.getByIDCalls, "the probe must not start for an out-of-scope account")
}
