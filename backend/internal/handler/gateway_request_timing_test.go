package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type delayedTimingWriter struct{ gin.ResponseWriter }

func TestGatewayPayloadTooLargeKeepsRequestClassification(t *testing.T) {
	canonical := normalizeOpsErrorType("request_too_large", "")
	require.Equal(t, "invalid_request_error", canonical)
	require.Equal(t, "request", classifyOpsPhase(canonical, "Request body is too large", ""))
	require.Equal(t, "P3", classifyOpsSeverity(canonical, http.StatusRequestEntityTooLarge))
	require.Equal(t, "invalid_request", service.ClassifyChannelMonitorV2Error(service.ChannelMonitorV2ErrorInput{
		ErrorType: canonical, StatusCode: http.StatusRequestEntityTooLarge, UpstreamStatusCode: http.StatusRequestEntityTooLarge,
	}))
}

func (w *delayedTimingWriter) Write(data []byte) (int, error) {
	time.Sleep(10 * time.Millisecond)
	return w.ResponseWriter.Write(data)
}

func (w *delayedTimingWriter) Flush() {
	time.Sleep(20 * time.Millisecond)
	w.ResponseWriter.Flush()
}

func TestGatewayRequestTiming_PublishesOpsAndRestoresWriter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	synctest.Test(t, func(t *testing.T) {
		core, logs := observer.New(zap.InfoLevel)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		ctx := logger.IntoContext(context.Background(), zap.New(core))
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages?key=hidden", nil).WithContext(ctx)
		c.Request.Header.Set("Authorization", "Bearer hidden")
		original := &delayedTimingWriter{c.Writer}
		c.Writer = original
		finish := beginGatewayRequestTiming(c)
		admission := service.MeasureGatewayTiming(c.Request.Context(), service.GatewayTimingUserAdmission)
		time.Sleep(10 * time.Millisecond)
		wait := service.MeasureGatewayTiming(c.Request.Context(), service.GatewayTimingUserWait)
		time.Sleep(30 * time.Millisecond)
		wait()
		admission()
		setOpsSelectedAccount(c, 66, service.PlatformAnthropic)
		c.Header("X-Test", "preserved")
		c.Status(http.StatusRequestEntityTooLarge)
		_, err := c.Writer.Write([]byte("request too large"))
		require.NoError(t, err)
		c.Writer.Flush()
		finish()
		require.Same(t, original, c.Writer)
		require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		require.Equal(t, "request too large", rec.Body.String())
		require.Equal(t, "preserved", rec.Header().Get("X-Test"))
		entry := &service.OpsInsertErrorLogInput{}
		applyOpsLatencyFieldsFromContext(c, entry)
		require.NotNil(t, entry.RoutingLatencyMs)
		require.EqualValues(t, 40, *entry.RoutingLatencyMs)
		require.Nil(t, entry.UpstreamLatencyMs)
		snapshot := service.GatewayRequestTimingFromContext(c.Request.Context()).Snapshot()
		require.EqualValues(t, 30, snapshot.PhaseMS["downstream_write"])
		require.EqualValues(t, 70, snapshot.TotalMS)
		entries := logs.FilterMessage("gateway.request_timing").All()
		require.Len(t, entries, 1)
		fields := entries[0].ContextMap()
		require.EqualValues(t, 66, fields["account_id"])
		require.EqualValues(t, 413, fields["wire_status"])
		require.NotContains(t, fields, "authorization")
		require.NotContains(t, fields, "request_body")
		require.NotContains(t, fields, "upstream_url")
	})
}

func TestGatewayRequestTiming_RecordsEarlyRejection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	core, logs := observer.New(zap.InfoLevel)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	ctx := logger.IntoContext(context.Background(), zap.New(core))
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
	original := c.Writer
	(&GatewayHandler{}).Messages(c)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Same(t, original, c.Writer)
	require.Len(t, logs.FilterMessage("gateway.request_timing").All(), 1)
	timing := service.GatewayRequestTimingFromContext(c.Request.Context()).Snapshot()
	require.Zero(t, timing.UpstreamAttempts)
}

func TestGatewayRequestTiming_RecordsCancellationWithoutChangingIt(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	ctx, cancel := context.WithCancel(logger.IntoContext(context.Background(), zap.New(core)))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
	finish := beginGatewayRequestTiming(c)
	cancel()
	finish()
	require.ErrorIs(t, c.Request.Context().Err(), context.Canceled)
	entries := logs.FilterMessage("gateway.request_timing").All()
	require.Len(t, entries, 1)
	require.Equal(t, true, entries[0].ContextMap()["client_canceled"])
}
