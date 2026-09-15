package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type captureObservationRepo struct{}

func (captureObservationRepo) GetConfig(context.Context) (*service.ChannelMonitorObservationConfig, error) {
	cfg := service.DefaultChannelMonitorObservationConfig()
	return &cfg, nil
}
func (captureObservationRepo) UpdateConfig(context.Context, service.ChannelMonitorObservationConfig, int) (*service.ChannelMonitorObservationConfig, error) {
	cfg := service.DefaultChannelMonitorObservationConfig()
	return &cfg, nil
}
func (captureObservationRepo) StoreBatch(context.Context, []service.ChannelMonitorEvent) error {
	return nil
}
func (captureObservationRepo) RecordGaps(context.Context, string, []service.ChannelMonitorObservationGap) error {
	return nil
}
func (captureObservationRepo) Heartbeat(context.Context, service.ChannelMonitorObservationSession) error {
	return nil
}
func (captureObservationRepo) Maintain(context.Context, time.Time) error { return nil }
func (captureObservationRepo) Query(context.Context, service.ChannelMonitorV2Filter) (*service.ChannelMonitorObservationSnapshot, error) {
	return &service.ChannelMonitorObservationSnapshot{}, nil
}

func TestChannelMonitorCaptureWriterPreservesLargeResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	payload := strings.Repeat("x", channelMonitorCaptureLimit+4096)
	r.GET("/v1/messages", func(c *gin.Context) {
		c.Header("Content-Type", "application/json")
		writer := &channelMonitorCaptureWriter{ResponseWriter: c.Writer, protocol: "anthropic"}
		c.Writer = writer
		_, _ = writer.Write([]byte(payload))
	})
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	r.ServeHTTP(res, req)
	got, err := io.ReadAll(res.Result().Body)
	require.NoError(t, err)
	require.Equal(t, payload, string(got))
}

func TestChannelMonitorCaptureDoesNotReadOrTruncateRequestBody(t *testing.T) {
	collector := service.NewChannelMonitorCollector(captureObservationRepo{}, service.ChannelMonitorCollectorOptions{})
	collector.Start()
	defer func() { require.NoError(t, collector.Stop(context.Background())) }()
	r := gin.New()
	r.Use(ChannelMonitorCaptureMiddleware(collector))
	payload := strings.Repeat("x", channelMonitorCaptureLimit+4096)
	r.POST("/v1/messages", func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		c.Data(http.StatusOK, "application/json", []byte(`{"type":"message","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
		require.Equal(t, payload, string(body))
	})
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(payload))
	r.ServeHTTP(res, req)
	require.Equal(t, http.StatusOK, res.Code)
}

func TestChannelMonitorParserResultClassifiesEmptyStream(t *testing.T) {
	p := newChannelMonitorParser("anthropic", true)
	p.feed([]byte("data: {\"type\":\"message_start\"}\n\n"))
	result := p.result(http.StatusOK)
	require.Equal(t, "channel_error", result.Outcome)
	require.Equal(t, "empty_stream", result.ErrorCategory)
}

func TestChannelMonitorWriterResultClassifiesHeaderOnlySSEAsEmptyStream(t *testing.T) {
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	writer := &channelMonitorCaptureWriter{ResponseWriter: context.Writer, protocol: "anthropic"}
	writer.Header().Set("Content-Type", "text/event-stream")
	result, stream := channelMonitorWriterResult(writer, "anthropic", http.StatusOK)
	require.True(t, stream)
	require.Equal(t, "channel_error", result.Outcome)
	require.Equal(t, "empty_stream", result.ErrorCategory)
}

func TestChannelMonitorParserResultClassifiesClientHTTPError(t *testing.T) {
	p := newChannelMonitorParser("anthropic", false)
	result := p.result(http.StatusBadRequest)
	require.Equal(t, "client_error", result.Outcome)
	require.Equal(t, "client_http", result.ErrorCategory)
}

func TestChannelMonitorParserParsesFinalSSEEventWithoutBlankLine(t *testing.T) {
	p := newChannelMonitorParser("anthropic", true)
	p.feed([]byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"ok\"}}\n"))
	result := p.result(http.StatusOK)
	require.True(t, result.OutputSeen)
	require.False(t, p.malformed)
}

func TestChannelMonitorProtocolOnlyCapturesGenerationEndpoints(t *testing.T) {
	tests := []struct {
		method, path, want string
	}{
		{http.MethodPost, "/v1/messages", "anthropic"},
		{http.MethodPost, "/v1/messages/count_tokens", "unknown"},
		{http.MethodPost, "/v1/chat/completions", "openai_chat"},
		{http.MethodGet, "/v1/responses", "unknown"},
		{http.MethodPost, "/v1/responses", "openai_responses"},
		{http.MethodPost, "/v1beta/models/gemini:generateContent", "gemini"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			require.Equal(t, tt.want, channelMonitorProtocol(tt.method, tt.path))
		})
	}
}

func TestChannelMonitorTimingFactsUsesGatewayTimingSnapshot(t *testing.T) {
	ctx, timing := service.WithGatewayRequestTiming(context.Background())
	timing.Record(service.GatewayTimingAccountSelect, 12*time.Millisecond)
	phases, first := channelMonitorTimingFacts(ctx)
	require.Nil(t, first)
	require.EqualValues(t, 12, phases["account_select"])
	require.Contains(t, phases, "total")
}
