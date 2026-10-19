package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpsErrorLoggerMiddlewareInstallsAttemptClock(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	var first, second *service.OpsUpstreamAttemptClock
	router.POST("/v1/messages",
		OpsErrorLoggerMiddleware(nil),
		func(c *gin.Context) {
			first = service.OpsUpstreamAttemptClockFromContext(c.Request.Context())
			c.Next()
		},
		OpsErrorLoggerMiddleware(nil),
		func(c *gin.Context) {
			second = service.OpsUpstreamAttemptClockFromContext(c.Request.Context())
			c.Status(http.StatusOK)
		},
	)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, first, "upstream requests built from c.Request.Context() can reach the clock")
	require.Same(t, first, second, "installing twice reuses one clock")
}
