package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestProxyGetByID_ReturnsFullDTO(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newStubAdminService()
	svc.proxies = []service.Proxy{{ID: 7, Name: "hk-02", Protocol: "socks5", Host: "203.0.113.24", Port: 1080, Password: "secret", Status: "expired"}}
	r := gin.New()
	r.GET("/proxies/:id", NewProxyHandler(svc).GetByID)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/proxies/7", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	require.Contains(t, body, `"account_count":3`)
	require.Contains(t, body, `"fallback_account_count":2`)
	require.Contains(t, body, `"password":"secret"`, "same fields as before, password included")
	require.Contains(t, body, `"status":"expired"`)

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/proxies/abc", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
