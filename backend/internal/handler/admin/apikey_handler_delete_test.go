package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func setupAPIKeyDeleteHandler(adminSvc service.AdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := NewAdminAPIKeyHandler(adminSvc)
	router.DELETE("/api/v1/admin/api-keys/:id", h.Delete)
	return router
}

func TestAdminAPIKeyHandler_Delete(t *testing.T) {
	stub := newStubAdminService()
	stub.apiKeys = []service.APIKey{{ID: 4087, UserID: 1, Key: "sk-live"}}
	router := setupAPIKeyDeleteHandler(stub)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/admin/api-keys/4087", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"id":4087,"deleted":true}`, extractData(t, rec.Body.Bytes()))
	require.Empty(t, stub.apiKeys)
}

func extractData(t *testing.T, body []byte) string {
	t.Helper()
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))
	return string(envelope.Data)
}

func TestAdminAPIKeyHandler_Delete_NotFoundAndInvalidID(t *testing.T) {
	router := setupAPIKeyDeleteHandler(newStubAdminService())

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/admin/api-keys/999", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/admin/api-keys/abc", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
