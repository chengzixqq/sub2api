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

// TestOpsMonitoringStatusRoutesRegisteredBehindAdminAuth: both methods sit in
// the admin ops group (a nil ops service answers 503, not 404) and still
// require admin authentication.
func TestOpsMonitoringStatusRoutesRegisteredBehindAdminAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{Ops: adminhandler.NewOpsHandler(nil)}}
	adminAuth := servermiddleware.AdminAuthMiddleware(func(c *gin.Context) {
		switch c.GetHeader("Authorization") {
		case "":
			servermiddleware.AbortWithError(c, http.StatusUnauthorized, "UNAUTHORIZED", "Authorization required")
		case "Bearer admin-token":
			c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 1})
			c.Set(string(servermiddleware.ContextKeyUserRole), service.RoleAdmin)
			c.Next()
		default:
			servermiddleware.AbortWithError(c, http.StatusForbidden, "FORBIDDEN", "Admin access required")
		}
	})
	auditLog := servermiddleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() })
	stepUp := servermiddleware.StepUpAuthMiddleware(func(c *gin.Context) { c.Next() })
	RegisterAdminRoutes(router.Group("/api/v1"), handlers, adminAuth, auditLog, stepUp, nil, nil, nil)

	for _, method := range []string{http.MethodGet, http.MethodPut} {
		for _, tc := range []struct {
			auth       string
			wantStatus int
		}{
			{wantStatus: http.StatusUnauthorized},
			{auth: "Bearer user-token", wantStatus: http.StatusForbidden},
			{auth: "Bearer admin-token", wantStatus: http.StatusServiceUnavailable},
		} {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(method, "/api/v1/admin/ops/monitoring-status", strings.NewReader(`{"monitoring_enabled":true}`))
			request.Header.Set("Content-Type", "application/json")
			if tc.auth != "" {
				request.Header.Set("Authorization", tc.auth)
			}
			router.ServeHTTP(recorder, request)
			require.Equal(t, tc.wantStatus, recorder.Code, "%s auth=%q: %s", method, tc.auth, recorder.Body.String())
		}
	}
}
