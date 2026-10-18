package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpsLatencyFields_DurationFromGatewayTiming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	synctest.Test(t, func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		ctx, _ := service.WithGatewayRequestTiming(c.Request.Context())
		c.Request = c.Request.WithContext(ctx)
		time.Sleep(3200 * time.Millisecond)

		entry := &service.OpsInsertErrorLogInput{}
		applyOpsLatencyFieldsFromContext(c, entry)
		require.NotNil(t, entry.DurationMs)
		require.EqualValues(t, 3200, *entry.DurationMs)
	})
}

func TestOpsLatencyFields_DurationFromMiddlewareStart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	synctest.Test(t, func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(context.Background())
		markOpsRequestStart(c)
		time.Sleep(450 * time.Millisecond)

		entry := &service.OpsInsertErrorLogInput{}
		applyOpsLatencyFieldsFromContext(c, entry)
		require.NotNil(t, entry.DurationMs)
		require.EqualValues(t, 450, *entry.DurationMs)
	})
}

func TestOpsLatencyFields_NoTimingLeavesDurationNil(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	entry := &service.OpsInsertErrorLogInput{}
	applyOpsLatencyFieldsFromContext(c, entry)
	require.Nil(t, entry.DurationMs)
}
