package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestProxyFallbackHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newStubAdminService()
	h := NewProxyHandler(svc)
	r := gin.New()
	r.GET("/proxies/:id/fallback-accounts", h.GetProxyFallbackAccounts)
	r.POST("/proxies/:id/fallback-accounts/revert", h.RevertProxyFallbackAccounts)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/proxies/1/fallback-accounts", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"current_proxy_id":2`)
	require.Contains(t, rec.Body.String(), `"current_proxy_name":"sg-01"`)
	require.Contains(t, rec.Body.String(), `"current_proxy_id":null`)

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/proxies/1/fallback-accounts/revert", nil))
	require.Equal(t, http.StatusOK, rec.Code, "an empty body means all accounts")
	require.Nil(t, svc.lastRevertAccountIDs)

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, jsonRequest(http.MethodPost, "/proxies/1/fallback-accounts/revert", `{"account_ids":[12,15]}`))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []int64{12, 15}, svc.lastRevertAccountIDs)
	require.Contains(t, rec.Body.String(), `"reverted_ids":[12]`)
}
