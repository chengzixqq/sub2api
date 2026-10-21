package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Writing the APNs p8 key sits behind step-up; managing one's own push devices does not.
func TestPushConfigWriteRequiresStepUp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{Push: &adminhandler.PushHandler{}}}
	adminAuth := servermiddleware.AdminAuthMiddleware(func(c *gin.Context) {
		c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 1})
		c.Set(string(servermiddleware.ContextKeyUserRole), service.RoleAdmin)
		c.Next()
	})
	auditLog := servermiddleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() })
	stepUpHits := map[string]int{}
	stepUp := servermiddleware.StepUpAuthMiddleware(func(c *gin.Context) {
		stepUpHits[c.Request.Method+" "+c.FullPath()]++
		servermiddleware.AbortWithError(c, http.StatusForbidden, "STEP_UP_REQUIRED", "step-up required")
	})
	RegisterAdminRoutes(router.Group("/api/v1"), handlers, adminAuth, auditLog, stepUp, nil, nil, nil)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/admin/push/config", nil))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, 1, stepUpHits["PUT /api/v1/admin/push/config"])

	for _, route := range []struct{ method, path, full string }{
		{http.MethodPost, "/api/v1/admin/push/devices", "/api/v1/admin/push/devices"},
		{http.MethodGet, "/api/v1/admin/push/config", "/api/v1/admin/push/config"},
	} {
		func() {
			defer func() { _ = recover() }() // the zero handler has no service behind it
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(route.method, route.path, nil))
		}()
		require.Zero(t, stepUpHits[route.method+" "+route.full], route.path)
	}
}
