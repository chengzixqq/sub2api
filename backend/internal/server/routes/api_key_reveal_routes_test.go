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

// Revealing a key's plaintext must sit behind step-up; the masked reads must not.
func TestAPIKeyRevealRouteRequiresStepUp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{APIKey: &adminhandler.AdminAPIKeyHandler{}}}
	adminAuth := servermiddleware.AdminAuthMiddleware(func(c *gin.Context) {
		c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 1})
		c.Set(string(servermiddleware.ContextKeyUserRole), service.RoleAdmin)
		c.Next()
	})
	auditLog := servermiddleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() })
	stepUpHits := map[string]int{}
	stepUp := servermiddleware.StepUpAuthMiddleware(func(c *gin.Context) {
		stepUpHits[c.FullPath()]++
		servermiddleware.AbortWithError(c, http.StatusForbidden, "STEP_UP_REQUIRED", "step-up required")
	})
	RegisterAdminRoutes(router.Group("/api/v1"), handlers, adminAuth, auditLog, stepUp, nil, nil, nil)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/api-keys/7/reveal", nil))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_REQUIRED")
	require.Equal(t, 1, stepUpHits["/api/v1/admin/api-keys/:id/reveal"])
	require.Zero(t, stepUpHits["/api/v1/admin/api-keys/:id"])
}
