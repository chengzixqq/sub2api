//go:build unit

package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicOpsCaptureLimit(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 2)
	r := gin.New()
	r.Use(OpsErrorLoggerMiddleware(service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)))
	r.POST("/v1/messages", func(c *gin.Context) {
		(&GatewayHandler{}).errorResponse(c, 499, "api_error", strings.Repeat("x", 2*opsCaptureWriterLimit))
		service.SetOpsUpstreamError(c, 502, "upstream failed", "")
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", nil))
	require.Len(t, opsErrorLogQueue, 1)
	e := (<-opsErrorLogQueue).entry
	require.LessOrEqual(t, len(e.ErrorMessage), 1024+len("..."))
	require.True(t, strings.HasPrefix(e.ErrorBody, `{"error":{"message":"xxx`))
	require.LessOrEqual(t, len(e.ErrorBody), opsCaptureWriterLimit)
}
