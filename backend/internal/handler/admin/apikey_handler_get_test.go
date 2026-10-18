package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAdminAPIKeyHandler_Get(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := newStubAdminService()
	stub.apiKeys = []service.APIKey{{ID: 4087, UserID: 1, Key: "sk-live", Name: "脚本", Status: "active"}}
	r := gin.New()
	r.GET("/api/v1/admin/api-keys/:id", NewAdminAPIKeyHandler(stub, nil).Get)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/api-keys/4087", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"name":"脚本"`)
	require.Contains(t, rec.Body.String(), `"user_id":1`)

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/api-keys/999", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/api-keys/x", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
