package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// recoverStateRepo holds the shared account state seen by both the rate limit
// service (recovery) and the admin service stub (ownership check, echo).
type recoverStateRepo struct {
	service.AccountRepository
	mu            sync.Mutex
	account       service.Account
	calls         []string
	clearErrorErr error
}

func (r *recoverStateRepo) record(call string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
}

func (r *recoverStateRepo) snapshot() *service.Account {
	r.mu.Lock()
	defer r.mu.Unlock()
	acc := r.account
	return &acc
}

func (r *recoverStateRepo) count(call string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		if c == call {
			n++
		}
	}
	return n
}

func (r *recoverStateRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	r.record("GetByID")
	if id != r.account.ID {
		return nil, service.ErrAccountNotFound
	}
	return r.snapshot(), nil
}

func (r *recoverStateRepo) ClearError(_ context.Context, _ int64) error {
	r.record("ClearError")
	if r.clearErrorErr != nil {
		return r.clearErrorErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.account.Status = service.StatusActive
	r.account.ErrorMessage = ""
	r.account.ErrorAt = nil
	return nil
}

type recoverStateAdmin struct {
	*stubAdminService
	repo            *recoverStateRepo
	getErr          error
	setSchedErr     error
	setSchedValues  []bool
	getAccountCalls int
}

func (s *recoverStateAdmin) GetAccount(_ context.Context, id int64) (*service.Account, error) {
	s.getAccountCalls++
	if s.getErr != nil {
		return nil, s.getErr
	}
	if id != s.repo.account.ID {
		return nil, service.ErrAccountNotFound
	}
	return s.repo.snapshot(), nil
}

func (s *recoverStateAdmin) SetAccountSchedulable(_ context.Context, _ int64, schedulable bool) (*service.Account, error) {
	s.repo.record("SetAccountSchedulable")
	s.setSchedValues = append(s.setSchedValues, schedulable)
	if s.setSchedErr != nil {
		return nil, s.setSchedErr
	}
	s.repo.mu.Lock()
	s.repo.account.Schedulable = schedulable
	s.repo.mu.Unlock()
	return s.repo.snapshot(), nil
}

func newRecoverStateFixture(schedulable bool) *recoverStateAdmin {
	repo := &recoverStateRepo{account: service.Account{
		ID:           12,
		Name:         "recover-me",
		Platform:     service.PlatformAnthropic,
		Type:         service.AccountTypeAPIKey,
		Status:       service.StatusError,
		ErrorMessage: "upstream 401",
		Schedulable:  schedulable,
	}}
	return &recoverStateAdmin{stubAdminService: newStubAdminService(), repo: repo}
}

// doRecoverState sends the request; a nil body pointer means no body at all.
func doRecoverState(t *testing.T, admin *recoverStateAdmin, body *string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rateLimit := service.NewRateLimitService(admin.repo, nil, &config.Config{}, nil, nil)
	h := NewAccountHandler(admin, nil, nil, nil, nil, nil, rateLimit, nil, nil, nil, nil, nil, nil, nil)
	r := gin.New()
	r.POST("/api/v1/admin/accounts/:id/recover-state", h.RecoverState)

	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(*body)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/12/recover-state", reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return rec, out
}

func recoverStateData(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	data, ok := out["data"].(map[string]any)
	require.True(t, ok, "missing data: %v", out)
	return data
}

func recoverBody(s string) *string { return &s }

func TestAccountHandlerRecoverState_NoBodyKeepsSchedulable(t *testing.T) {
	admin := newRecoverStateFixture(false)

	rec, out := doRecoverState(t, admin, nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, admin.repo.count("ClearError"))
	require.Equal(t, 0, admin.repo.count("SetAccountSchedulable"))
	data := recoverStateData(t, out)
	require.Equal(t, service.StatusActive, data["status"])
	require.Equal(t, false, data["schedulable"])
	require.Equal(t, "", data["error_message"])
}

func TestAccountHandlerRecoverState_EmptyOrNullSchedulableIsNoop(t *testing.T) {
	for _, body := range []string{`{}`, `{"schedulable":null}`, "  \n "} {
		t.Run(body, func(t *testing.T) {
			admin := newRecoverStateFixture(false)

			rec, out := doRecoverState(t, admin, recoverBody(body))

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, 1, admin.repo.count("ClearError"))
			require.Equal(t, 0, admin.repo.count("SetAccountSchedulable"))
			require.Equal(t, false, recoverStateData(t, out)["schedulable"])
		})
	}
}

func TestAccountHandlerRecoverState_SchedulableTrueEnablesAfterRecovery(t *testing.T) {
	admin := newRecoverStateFixture(false)

	rec, out := doRecoverState(t, admin, recoverBody(`{"schedulable":true}`))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []bool{true}, admin.setSchedValues)
	require.Equal(t, []string{"GetByID", "ClearError", "SetAccountSchedulable"}, admin.repo.calls)
	data := recoverStateData(t, out)
	require.Equal(t, service.StatusActive, data["status"])
	require.Equal(t, true, data["schedulable"])
	require.Equal(t, "", data["error_message"])
}

func TestAccountHandlerRecoverState_SchedulableFalseExplicitlyPauses(t *testing.T) {
	admin := newRecoverStateFixture(true)

	rec, out := doRecoverState(t, admin, recoverBody(`{"schedulable":false}`))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []bool{false}, admin.setSchedValues)
	require.Equal(t, false, recoverStateData(t, out)["schedulable"])
}

func TestAccountHandlerRecoverState_SchedulableFailureReportsRecovered(t *testing.T) {
	admin := newRecoverStateFixture(false)
	admin.setSchedErr = errors.New("db down")

	rec, out := doRecoverState(t, admin, recoverBody(`{"schedulable":true}`))

	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	require.Equal(t, "ACCOUNT_RECOVERED_SCHEDULABLE_FAILED", out["reason"])
	require.Equal(t, map[string]any{"recovered": "true"}, out["metadata"])
	require.Equal(t, 1, admin.repo.count("ClearError"))
	require.Equal(t, service.StatusActive, admin.repo.snapshot().Status)
}

func TestAccountHandlerRecoverState_RecoverFailureSkipsSchedulable(t *testing.T) {
	admin := newRecoverStateFixture(false)
	admin.repo.clearErrorErr = errors.New("clear failed")

	rec, _ := doRecoverState(t, admin, recoverBody(`{"schedulable":true}`))

	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	require.Equal(t, 0, admin.repo.count("SetAccountSchedulable"))
}

func TestAccountHandlerRecoverState_MalformedBodyRejectedBeforeAnyWrite(t *testing.T) {
	for _, body := range []string{`{"schedulable":"yes"}`, `{bad`} {
		t.Run(body, func(t *testing.T) {
			admin := newRecoverStateFixture(false)

			rec, _ := doRecoverState(t, admin, recoverBody(body))

			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Empty(t, admin.repo.calls)
			require.Equal(t, 0, admin.getAccountCalls)
		})
	}
}

func TestAccountHandlerRecoverState_ForeignAccountTouchesNothing(t *testing.T) {
	admin := newRecoverStateFixture(false)
	admin.getErr = domain.ErrWorkspaceScopeViolation

	rec, out := doRecoverState(t, admin, recoverBody(`{"schedulable":true}`))

	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.Equal(t, "RESOURCE_NOT_FOUND", out["reason"])
	require.Equal(t, 0, admin.repo.count("ClearError"))
	require.Equal(t, 0, admin.repo.count("SetAccountSchedulable"))
}
