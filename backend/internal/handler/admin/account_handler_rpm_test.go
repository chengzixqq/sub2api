package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type accountRuntimeRPMCacheStub struct {
	counts     map[int64]int
	err        error
	batchErr   error
	getIDs     []int64
	batchCalls [][]int64
}

func (s *accountRuntimeRPMCacheStub) IncrementRPM(context.Context, int64) (int, error) {
	panic("account read handlers must not increment RPM")
}

func (s *accountRuntimeRPMCacheStub) GetRPM(_ context.Context, id int64) (int, error) {
	s.getIDs = append(s.getIDs, id)
	return s.counts[id], s.err
}

func (s *accountRuntimeRPMCacheStub) GetRPMBatch(_ context.Context, ids []int64) (map[int64]int, error) {
	s.batchCalls = append(s.batchCalls, append([]int64(nil), ids...))
	return s.counts, s.batchErr
}

func setupAccountRuntimeRPMRouter(accounts []service.Account, cache service.RPMCache) (*gin.Engine, *stubAdminService) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	adminSvc := newStubAdminService()
	adminSvc.accounts = accounts
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, cache, nil)
	router.GET("/api/v1/admin/accounts", handler.List)
	router.GET("/api/v1/admin/accounts/:id", handler.GetByID)
	return router, adminSvc
}

func TestAccountHandlerCurrentRPMAllAccountTypesWithoutLimits(t *testing.T) {
	accounts := []service.Account{
		{ID: 11, Name: "openai-key", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey},
		{ID: 12, Name: "anthropic-key", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey},
		{ID: 13, Name: "openai-oauth", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth},
		{ID: 14, Name: "anthropic-oauth", Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth},
		{ID: 15, Name: "gemini-key", Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey},
	}
	counts := map[int64]int{11: 37, 12: 0, 13: 8, 14: 5, 15: 2}
	cache := &accountRuntimeRPMCacheStub{counts: counts}
	router, adminSvc := setupAccountRuntimeRPMRouter(accounts, cache)

	for _, suffix := range []string{"", "&lite=1"} {
		t.Run("list"+suffix, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20"+suffix, nil))
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			var payload struct {
				Data struct {
					Items []struct {
						ID         int64 `json:"id"`
						CurrentRPM *int  `json:"current_rpm"`
					} `json:"items"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
			require.Len(t, payload.Data.Items, len(accounts))
			for _, account := range payload.Data.Items {
				require.NotNil(t, account.CurrentRPM, "account %d", account.ID)
				require.Equal(t, counts[account.ID], *account.CurrentRPM)
			}
		})
	}
	// Both full and compact responses query only the loaded page, once each.
	require.Equal(t, [][]int64{{11, 12, 13, 14, 15}, {11, 12, 13, 14, 15}}, cache.batchCalls)
	require.Empty(t, cache.getIDs, "list must not add per-row reads")

	for _, account := range accounts {
		adminSvc.getAccountResult = &account
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/"+strconv.FormatInt(account.ID, 10), nil))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		var payload struct {
			Data struct {
				CurrentRPM *int `json:"current_rpm"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
		require.NotNil(t, payload.Data.CurrentRPM)
		require.Equal(t, counts[account.ID], *payload.Data.CurrentRPM)
	}
	require.Equal(t, []int64{11, 12, 13, 14, 15}, cache.getIDs)
}

func TestAccountHandlerCurrentRPMReadFailuresRemainUnavailable(t *testing.T) {
	accounts := []service.Account{
		{ID: 21, Name: "known-key", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey},
		{ID: 22, Name: "failed-key", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey},
	}
	cache := &accountRuntimeRPMCacheStub{
		counts:   map[int64]int{21: 9},
		err:      errors.New("redis unavailable"),
		batchErr: errors.New("one counter failed"),
	}
	router, adminSvc := setupAccountRuntimeRPMRouter(accounts, cache)
	for _, suffix := range []string{"", "&lite=1"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20"+suffix, nil))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		var payload struct {
			Data struct {
				Items []map[string]any `json:"items"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
		require.Equal(t, float64(9), payload.Data.Items[0]["current_rpm"])
		require.NotContains(t, payload.Data.Items[1], "current_rpm")
	}

	adminSvc.getAccountResult = &accounts[1]
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/22", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.NotContains(t, payload.Data, "current_rpm")
}

func TestAccountHandlerCurrentRPMMissingCacheRemainsUnavailable(t *testing.T) {
	account := service.Account{ID: 31, Name: "key", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey}
	router, adminSvc := setupAccountRuntimeRPMRouter([]service.Account{account}, nil)
	adminSvc.getAccountResult = &account
	for _, url := range []string{
		"/api/v1/admin/accounts?page=1&page_size=20",
		"/api/v1/admin/accounts?page=1&page_size=20&lite=1",
		"/api/v1/admin/accounts/31",
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, url, nil))
		require.Equal(t, http.StatusOK, recorder.Code)
		require.NotContains(t, recorder.Body.String(), `"current_rpm"`)
	}
}
