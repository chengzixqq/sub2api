package service

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestResolveUpstreamResponseReadLimit(t *testing.T) {
	t.Run("use default when config missing", func(t *testing.T) {
		require.Equal(t, defaultUpstreamResponseReadMaxBytes, resolveUpstreamResponseReadLimit(nil))
	})

	t.Run("use configured value", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Gateway.UpstreamResponseReadMaxBytes = 1234
		require.Equal(t, int64(1234), resolveUpstreamResponseReadLimit(cfg))
	})
}

func TestReadUpstreamResponseBodyLimited(t *testing.T) {
	t.Run("within limit", func(t *testing.T) {
		body, err := readUpstreamResponseBodyLimited(bytes.NewReader([]byte("ok")), 2)
		require.NoError(t, err)
		require.Equal(t, []byte("ok"), body)
	})

	t.Run("exceeds limit", func(t *testing.T) {
		body, err := readUpstreamResponseBodyLimited(bytes.NewReader([]byte("toolong")), 3)
		require.Nil(t, body)
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrUpstreamResponseBodyTooLarge))
	})
}

func TestReadUpstreamResponseBody(t *testing.T) {
	t.Run("within limit", func(t *testing.T) {
		body, err := ReadUpstreamResponseBody(bytes.NewReader([]byte("ok")), nil, nil, nil)
		require.NoError(t, err)
		require.Equal(t, []byte("ok"), body)
	})

	t.Run("exceeds limit calls onTooLarge", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Gateway.UpstreamResponseReadMaxBytes = 3

		called := false
		onTooLarge := func(_ *gin.Context) { called = true }

		body, err := ReadUpstreamResponseBody(bytes.NewReader([]byte("toolong")), cfg, nil, onTooLarge)
		require.Nil(t, body)
		require.True(t, errors.Is(err, ErrUpstreamResponseBodyTooLarge))
		require.True(t, called)
	})

	t.Run("nil onTooLarge does not panic", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Gateway.UpstreamResponseReadMaxBytes = 3

		body, err := ReadUpstreamResponseBody(bytes.NewReader([]byte("toolong")), cfg, nil, nil)
		require.Nil(t, body)
		require.True(t, errors.Is(err, ErrUpstreamResponseBodyTooLarge))
	})

	t.Run("io error does not call onTooLarge", func(t *testing.T) {
		called := false
		onTooLarge := func(_ *gin.Context) { called = true }

		body, err := ReadUpstreamResponseBody(iotest.ErrReader(errors.New("disk failure")), nil, nil, onTooLarge)
		require.Nil(t, body)
		require.Error(t, err)
		require.False(t, errors.Is(err, ErrUpstreamResponseBodyTooLarge))
		require.False(t, called)
	})
}

func TestChatCompletionsBridgeResponseLimitErrorType(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.UpstreamResponseReadMaxBytes = 1
	s := &OpenAIGatewayService{cfg: cfg}
	for _, tc := range []struct{ path, expected string }{
		{"/v1/messages", `{"type":"error","error":{"type":"api_error","message":"Upstream response too large"}}`},
		{"/v1/chat/completions", `{"error":{"type":"upstream_error","message":"Upstream response too large"}}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, nil)
			resp := &http.Response{Body: io.NopCloser(strings.NewReader("{}"))}
			_, _, err := s.readCCUpstreamJSONResponse(c, resp, nil)
			require.ErrorIs(t, err, ErrUpstreamResponseBodyTooLarge)
			require.Equal(t, 502, rec.Code)
			require.JSONEq(t, tc.expected, rec.Body.String())
		})
	}
}

func TestOversizeResponsesBillsOnlyCompleteSearchFrames(t *testing.T) {
	const data = "data: " + `{"type":"response.output_item.done","item":{"id":"ws1","type":"web_search_call","status":"completed"}}`
	for _, tc := range []struct {
		name, ending string
		cut, want    int
	}{
		{"complete CRLF", "\r\n\r\n", 0, 1},
		{"partial CRLF", "\r\n\r\n", 1, 0},
		{"LF followed by CR", "\n\r", 0, 1},
		{"missing blank line", "\n\r", 1, 0},
		{"blank line with whitespace", "\n \t\n", 0, 1},
		{"pure CR with trailing comment", "\r\r", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := data + tc.ending
			full := frame + ":" + strings.Repeat("x", 256)
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			svc.cfg.Gateway.UpstreamResponseReadMaxBytes = int64(len(frame) - tc.cut)
			price := 10.0
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Set("api_key", &APIKey{Group: &Group{SearchPricePer1k: &price}})
			resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(full))}
			result, err := svc.handleNonStreamingResponse(t.Context(), resp, c, &Account{Platform: PlatformOpenAI}, "m", "m")
			require.ErrorIs(t, err, ErrUpstreamResponseBodyTooLarge)
			require.Equal(t, http.StatusBadGateway, c.Writer.Status())
			if tc.want == 0 {
				require.Nil(t, result)
			} else {
				require.NotNil(t, result)
				require.Equal(t, tc.want, result.searchCount)
			}
		})
	}
}

func TestBufferedSearchPreservesMultilineFrameAcrossComment(t *testing.T) {
	const frame = "data: {\"type\":\"response.output_item.done\",\n\r: comment\n" +
		"data: \"item\":{\"id\":\"ws1\",\"type\":\"web_search_call\",\"status\":\"completed\"}}\n\n"
	require.Equal(t, 1, countOpenAINativeSearchSSE(frame))
	require.Equal(t, 1, countBufferedOpenAISearch(frame+": truncated comment", true))
}
