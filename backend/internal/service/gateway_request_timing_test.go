package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayRequestTiming_TracksPhasesWithoutDoubleCounting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, timing := WithGatewayRequestTiming(context.Background())
		ctx2, same := WithGatewayRequestTiming(ctx)
		require.Same(t, timing, same)
		require.Equal(t, ctx, ctx2)
		admission := MeasureGatewayTiming(ctx, GatewayTimingUserAdmission)
		time.Sleep(10 * time.Millisecond)
		wait := MeasureGatewayTiming(ctx, GatewayTimingUserWait)
		time.Sleep(30 * time.Millisecond)
		wait()
		wait()
		admission()
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
		snapshot, ok := PublishGatewayTiming(c)
		require.True(t, ok)
		require.EqualValues(t, 40, snapshot.TotalMS)
		require.EqualValues(t, 30, snapshot.PhaseMS["user_wait"])
		require.EqualValues(t, 1, snapshot.PhaseCalls["user_wait"])
		require.EqualValues(t, 40, c.GetInt64(OpsRoutingLatencyMsKey))
		_, observed := c.Get(OpsUpstreamLatencyMsKey)
		require.False(t, observed, "unobserved upstream work must not be recorded as zero")
	})
}

func TestGatewayRequestTiming_UpstreamAttemptsHeadersAndBody(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, timing := WithGatewayRequestTiming(context.Background())
		req := httptest.NewRequest(http.MethodPost, "https://private.example/v1?api_key=hidden", strings.NewReader("abc")).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer secret")
		traced, finish := TraceGatewayUpstream(req)
		trace := httptrace.ContextClientTrace(traced.Context())
		trace.GetConn("private.example")
		time.Sleep(5 * time.Millisecond)
		trace.GotConn(httptrace.GotConnInfo{})
		time.Sleep(15 * time.Millisecond)
		trace.GotFirstResponseByte()
		response := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("response"))}
		finish(response, nil)
		finish(response, nil)
		require.EqualValues(t, 1, timing.Snapshot().UpstreamBodiesOpen)
		_, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		time.Sleep(80 * time.Millisecond)
		require.NoError(t, response.Body.Close())
		require.NoError(t, response.Body.Close())
		_, fail := TraceGatewayUpstream(req)
		time.Sleep(10 * time.Millisecond)
		fail(nil, errors.New("transport failed"))
		snapshot := timing.Snapshot()
		require.EqualValues(t, 2, snapshot.UpstreamAttempts)
		require.Zero(t, snapshot.UpstreamBodiesOpen)
		require.NotNil(t, snapshot.MaxDeclaredRequestBytes)
		require.EqualValues(t, 3, *snapshot.MaxDeclaredRequestBytes)
		require.EqualValues(t, 8, snapshot.UpstreamResponseBytes)
		require.EqualValues(t, 30, snapshot.PhaseMS["upstream_headers"])
		require.EqualValues(t, 80, snapshot.PhaseMS["upstream_body"])
		require.EqualValues(t, 5, snapshot.PhaseMS["http_connection_acquire"])
		require.EqualValues(t, 20, *snapshot.FirstUpstreamByteMS)
		encoded, err := json.Marshal(snapshot)
		require.NoError(t, err)
		for _, secret := range []string{"private.example", "api_key", "secret", "response" + `"`} {
			require.NotContains(t, string(encoded), secret)
		}
	})
}

func TestGatewayRequestTiming_IsolatedConcurrentAndDetached(t *testing.T) {
	ctx, timing := WithGatewayRequestTiming(context.Background())
	otherCtx, other := WithGatewayRequestTiming(context.Background())
	require.NotSame(t, timing, other)
	require.Same(t, timing, GatewayRequestTimingFromContext(context.WithoutCancel(ctx)))
	var workers sync.WaitGroup
	for range 50 {
		workers.Go(func() {
			MeasureGatewayTiming(ctx, GatewayTimingAccountSelect)()
			timing.Snapshot()
		})
	}
	workers.Wait()
	require.EqualValues(t, 50, timing.Snapshot().PhaseCalls["account_select"])
	require.Empty(t, GatewayRequestTimingFromContext(otherCtx).Snapshot().PhaseCalls)
	MeasureGatewayTiming(context.Background(), GatewayTimingAccountSelect)()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	unchanged, finish := TraceGatewayUpstream(req)
	require.Same(t, req, unchanged)
	finish(nil, nil)
}
