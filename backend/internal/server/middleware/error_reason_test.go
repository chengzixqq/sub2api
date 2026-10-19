//go:build unit

package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func decodeErrorBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body
}

func TestAbortWithErrorCarriesReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	AbortWithError(c, http.StatusUnauthorized, "TOKEN_EXPIRED", "Token has expired")

	body := decodeErrorBody(t, w)
	require.Equal(t, "TOKEN_EXPIRED", body["code"], "code stays a string")
	require.Equal(t, "TOKEN_EXPIRED", body["reason"])
	require.Equal(t, "Token has expired", body["message"])
}

func TestAdminComplianceRequiredCarriesReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := service.NewSettingService(&complianceGuardRepoStub{}, &config.Config{})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyUser), AuthSubject{UserID: 1})
		c.Next()
	})
	router.Use(AdminComplianceGuard(svc))
	router.GET("/api/v1/admin/users", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/users", nil))
	require.Equal(t, http.StatusLocked, w.Code)
	body := decodeErrorBody(t, w)
	require.Equal(t, "ADMIN_COMPLIANCE_ACK_REQUIRED", body["code"])
	require.Equal(t, "ADMIN_COMPLIANCE_ACK_REQUIRED", body["reason"])
	require.NotNil(t, body["metadata"])
}

func TestBackendModeGuardsCarryReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newBackendModeSettingService(t, "true")

	userRouter := gin.New()
	userRouter.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyUserRole), "user")
		c.Next()
	})
	userRouter.Use(BackendModeUserGuard(svc))
	userRouter.GET("/api/v1/user/profile", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	userRouter.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/user/profile", nil))
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Equal(t, "BACKEND_MODE_SELF_SERVICE_DISABLED", decodeErrorBody(t, w)["reason"])

	authRouter := gin.New()
	authRouter.Use(BackendModeAuthGuard(svc))
	authRouter.POST("/api/v1/auth/register", func(c *gin.Context) { c.Status(http.StatusOK) })
	w = httptest.NewRecorder()
	authRouter.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", nil))
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Equal(t, "BACKEND_MODE_SELF_SERVICE_DISABLED", decodeErrorBody(t, w)["reason"])
}
