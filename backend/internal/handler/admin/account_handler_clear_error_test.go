package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// clearErrorAdmin records the options each clear-error call received and
// simulates inactive accounts: with PreserveInactive they keep their status
// and are reported as skipped; otherwise they come back active.
type clearErrorAdmin struct {
	*stubAdminService
	inactiveIDs map[int64]bool
	failIDs     map[int64]bool
	options     map[int64][]service.ClearAccountErrorOptions
}

func newClearErrorAdmin(inactiveIDs ...int64) *clearErrorAdmin {
	admin := &clearErrorAdmin{
		stubAdminService: newStubAdminService(),
		inactiveIDs:      map[int64]bool{},
		failIDs:          map[int64]bool{},
		options:          map[int64][]service.ClearAccountErrorOptions{},
	}
	for _, id := range inactiveIDs {
		admin.inactiveIDs[id] = true
	}
	return admin
}

func (a *clearErrorAdmin) ClearAccountErrorWithOptions(_ context.Context, id int64, opts service.ClearAccountErrorOptions) (*service.ClearAccountErrorResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.options[id] = append(a.options[id], opts)
	if a.failIDs[id] {
		return nil, errors.New("clear failed")
	}
	account := &service.Account{ID: id, Name: "account", Status: service.StatusActive}
	if opts.PreserveInactive && a.inactiveIDs[id] {
		account.Status = "inactive"
		return &service.ClearAccountErrorResult{Account: account, SkippedInactive: true}, nil
	}
	return &service.ClearAccountErrorResult{Account: account}, nil
}

func (a *clearErrorAdmin) recordedOptions(id int64) []service.ClearAccountErrorOptions {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]service.ClearAccountErrorOptions(nil), a.options[id]...)
}

func setupClearErrorRouter(admin service.AdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewAccountHandler(admin, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router.POST("/accounts/:id/clear-error", handler.ClearError)
	router.POST("/accounts/batch-clear-error", handler.BatchClearError)
	return router
}

func decodeClearErrorData(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var envelope struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))
	require.Equal(t, 0, envelope.Code)
	return envelope.Data
}

func TestAccountHandlerClearError_OptionalPreserveInactiveBody(t *testing.T) {
	t.Run("no body keeps the historical behavior", func(t *testing.T) {
		admin := newClearErrorAdmin(5)
		router := setupClearErrorRouter(admin)

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/accounts/5/clear-error", nil))

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, []service.ClearAccountErrorOptions{{PreserveInactive: false}}, admin.recordedOptions(5))
		require.Equal(t, service.StatusActive, decodeClearErrorData(t, rec.Body.Bytes())["status"])
	})

	t.Run("empty object keeps the historical behavior", func(t *testing.T) {
		admin := newClearErrorAdmin(5)
		router := setupClearErrorRouter(admin)

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, jsonRequest(http.MethodPost, "/accounts/5/clear-error", `{}`))

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, []service.ClearAccountErrorOptions{{PreserveInactive: false}}, admin.recordedOptions(5))
	})

	t.Run("preserve_inactive true keeps an inactive account inactive", func(t *testing.T) {
		admin := newClearErrorAdmin(5)
		router := setupClearErrorRouter(admin)

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, jsonRequest(http.MethodPost, "/accounts/5/clear-error", `{"preserve_inactive":true}`))

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, []service.ClearAccountErrorOptions{{PreserveInactive: true}}, admin.recordedOptions(5))
		data := decodeClearErrorData(t, rec.Body.Bytes())
		require.Equal(t, "inactive", data["status"])
		require.EqualValues(t, 5, data["id"])
	})

	t.Run("wrong type is rejected before any write", func(t *testing.T) {
		admin := newClearErrorAdmin(5)
		router := setupClearErrorRouter(admin)

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, jsonRequest(http.MethodPost, "/accounts/5/clear-error", `{"preserve_inactive":"yes"}`))

		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		require.Empty(t, admin.recordedOptions(5))
	})

	t.Run("malformed json is rejected before any write", func(t *testing.T) {
		admin := newClearErrorAdmin(5)
		router := setupClearErrorRouter(admin)

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, jsonRequest(http.MethodPost, "/accounts/5/clear-error", `{"preserve_inactive":`))

		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		require.Empty(t, admin.recordedOptions(5))
	})
}

func TestAccountHandlerBatchClearError_PreserveInactiveReportsSkippedIDs(t *testing.T) {
	admin := newClearErrorAdmin(1, 3)
	router := setupClearErrorRouter(admin)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, jsonRequest(http.MethodPost, "/accounts/batch-clear-error", `{"account_ids":[3,1,2],"preserve_inactive":true}`))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"skipped_inactive_ids":[1,3]`)
	data := decodeClearErrorData(t, rec.Body.Bytes())
	require.EqualValues(t, 3, data["total"])
	require.EqualValues(t, 3, data["success"], "被跳过的停用账号也计入 success")
	require.EqualValues(t, 0, data["failed"])
	require.Nil(t, data["errors"])
	for _, id := range []int64{1, 2, 3} {
		require.Equal(t, []service.ClearAccountErrorOptions{{PreserveInactive: true}}, admin.recordedOptions(id), "account %d", id)
	}
}

func TestAccountHandlerBatchClearError_SkippedListExcludesFailures(t *testing.T) {
	admin := newClearErrorAdmin(1)
	admin.failIDs[2] = true
	router := setupClearErrorRouter(admin)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, jsonRequest(http.MethodPost, "/accounts/batch-clear-error", `{"account_ids":[1,2],"preserve_inactive":true}`))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"skipped_inactive_ids":[1]`)
	data := decodeClearErrorData(t, rec.Body.Bytes())
	require.EqualValues(t, 2, data["total"])
	require.EqualValues(t, 1, data["success"])
	require.EqualValues(t, 1, data["failed"])
	require.Len(t, data["errors"], 1)
}

func TestAccountHandlerBatchClearError_DefaultOutputsEmptySkippedList(t *testing.T) {
	admin := newClearErrorAdmin(1, 3)
	router := setupClearErrorRouter(admin)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, jsonRequest(http.MethodPost, "/accounts/batch-clear-error", `{"account_ids":[3,1,2]}`))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"skipped_inactive_ids":[]`)
	require.Contains(t, rec.Body.String(), `"errors":null`)
	data := decodeClearErrorData(t, rec.Body.Bytes())
	require.EqualValues(t, 3, data["total"])
	require.EqualValues(t, 3, data["success"])
	for _, id := range []int64{1, 2, 3} {
		require.Equal(t, []service.ClearAccountErrorOptions{{PreserveInactive: false}}, admin.recordedOptions(id), "account %d", id)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, jsonRequest(http.MethodPost, "/accounts/batch-clear-error", `{"account_ids":[],"preserve_inactive":true}`))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, jsonRequest(http.MethodPost, "/accounts/batch-clear-error", `{"account_ids":[1],"preserve_inactive":"yes"}`))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}
