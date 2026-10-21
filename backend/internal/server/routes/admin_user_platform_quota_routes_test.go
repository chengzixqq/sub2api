package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestUserPlatformQuotaItemRoutesRegistered: the per-platform PUT / DELETE /
// restore sit next to the whole-table routes behind admin authentication, and
// the static reset segment still wins over :platform. A handler without a
// quota repository answers 503, which proves the request reached it.
func TestUserPlatformQuotaItemRoutesRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	userHandler := adminhandler.NewUserHandler(nil, nil, nil, nil, nil, nil, nil)
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{User: userHandler}}
	adminAuth := servermiddleware.AdminAuthMiddleware(func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer admin-token" {
			servermiddleware.AbortWithError(c, http.StatusUnauthorized, "UNAUTHORIZED", "Authorization required")
			return
		}
		c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 1})
		c.Set(string(servermiddleware.ContextKeyUserRole), service.RoleAdmin)
		c.Next()
	})
	auditLog := servermiddleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() })
	stepUp := servermiddleware.StepUpAuthMiddleware(func(c *gin.Context) { c.Next() })
	RegisterAdminRoutes(router.Group("/api/v1"), handlers, adminAuth, auditLog, stepUp, nil, nil, nil)

	routes := map[string]string{}
	for _, r := range router.Routes() {
		routes[r.Method+" "+r.Path] = r.Handler
	}
	for _, key := range []string{
		"PUT /api/v1/admin/users/:id/platform-quotas/:platform",
		"DELETE /api/v1/admin/users/:id/platform-quotas/:platform",
		"POST /api/v1/admin/users/:id/platform-quotas/:platform/restore",
		"POST /api/v1/admin/users/:id/platform-quotas/reset",
	} {
		require.Contains(t, routes, key)
	}
	require.True(t, strings.HasSuffix(routes["PUT /api/v1/admin/users/:id/platform-quotas/:platform"], "UpsertUserPlatformQuota-fm"))
	require.True(t, strings.HasSuffix(routes["DELETE /api/v1/admin/users/:id/platform-quotas/:platform"], "DeleteUserPlatformQuota-fm"))
	require.True(t, strings.HasSuffix(routes["POST /api/v1/admin/users/:id/platform-quotas/:platform/restore"], "RestoreUserPlatformQuota-fm"))
	require.True(t, strings.HasSuffix(routes["POST /api/v1/admin/users/:id/platform-quotas/reset"], "ResetUserPlatformQuotaWindow-fm"))

	anonymous := httptest.NewRecorder()
	router.ServeHTTP(anonymous, httptest.NewRequest(http.MethodDelete, "/api/v1/admin/users/42/platform-quotas/anthropic", nil))
	require.Equal(t, http.StatusUnauthorized, anonymous.Code)

	for _, tc := range []struct{ method, path string }{
		{http.MethodPut, "/api/v1/admin/users/42/platform-quotas/anthropic"},
		{http.MethodDelete, "/api/v1/admin/users/42/platform-quotas/anthropic"},
		{http.MethodPost, "/api/v1/admin/users/42/platform-quotas/anthropic/restore"},
		{http.MethodPost, "/api/v1/admin/users/42/platform-quotas/reset"},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer admin-token")
		router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code, "%s %s: %s", tc.method, tc.path, recorder.Body.String())
	}
}
