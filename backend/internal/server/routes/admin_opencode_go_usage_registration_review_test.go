package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAccountRoutesOpenCodeGoUsageSettingsReachHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(service.WithScope(c.Request.Context(), service.AdminScope()))
		c.Next()
	})
	registerAccountRoutes(router.Group("/api/v1/admin"), &handler.Handlers{
		Admin: &handler.AdminHandlers{Account: &admin.AccountHandler{}},
	}, func(c *gin.Context) { c.Next() })

	for _, method := range []string{http.MethodGet, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(method, "/api/v1/admin/accounts/opencode-go-usage/settings", nil))
			// A missing optional service must reach the handler's typed error,
			// rather than return a routing 404 or be parsed as an account ID.
			require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
			require.Contains(t, recorder.Body.String(), "OPENCODE_GO_USAGE_UNAVAILABLE")
		})
	}
}
