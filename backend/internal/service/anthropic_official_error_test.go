package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAnthropicStreamErrorEncodesInvalidRequestID(t *testing.T) {
	frame := buildAnthropicStreamErrorSSEWithID("api_error", "read failed", "req_\xff\x01")
	payload := strings.TrimSpace(strings.TrimPrefix(frame, "event: error\ndata: "))
	var envelope struct {
		RequestID string `json:"request_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(payload), &envelope))
	require.Equal(t, "req_\uFFFD\x01", envelope.RequestID)
}

func TestNewAnthropicRequestID_Format(t *testing.T) {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	seen := make(map[string]bool, 1000)
	for range 1000 {
		id := NewAnthropicRequestID()
		require.Len(t, id, 28)
		require.True(t, strings.HasPrefix(id, "req_011C"), "unexpected request ID: %q", id)
		for _, ch := range id[8:] {
			require.Contains(t, alphabet, string(ch))
		}
		require.False(t, seen[id], "duplicate request ID: %q", id)
		seen[id] = true
	}
}

func TestWriteLocalAnthropicInvalidRequest_Bytes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name        string
		body        string
		contentType string
	}{
		{name: "non-stream", body: `{"stream":false}`},
		{name: "stream before headers", body: `{"stream":true}`},
		{name: "unflushed SSE headers", body: `{"stream":true}`, contentType: "text/event-stream"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(tt.body))
			c.Header("request-id", "previous-request-id")
			if tt.contentType != "" {
				c.Header("Content-Type", tt.contentType)
			}

			err := WriteLocalAnthropicInvalidRequest(c, "context_management: Extra inputs are not permitted")

			require.ErrorIs(t, err, ErrLocalAnthropicRejected)
			require.Equal(t, ErrLocalAnthropicRejected, err)
			require.EqualError(t, err, "local anthropic official rejection")
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Equal(t, "application/json", rec.Result().Header.Get("Content-Type"))
			id := rec.Result().Header.Get("request-id")
			require.Regexp(t, `^req_011C[1-9A-HJ-NP-Za-km-z]{20}$`, id)
			want := `{"type":"error","error":{"type":"invalid_request_error","message":"context_management: Extra inputs are not permitted"},"request_id":"` + id + `"}`
			require.Equal(t, want, rec.Body.String())
			require.True(t, IsOpsRequestScopedError(c))
			require.True(t, IsResponseCommitted(c))
			errType, code := OpsLocalErrorType(c)
			require.Equal(t, "invalid_request_error", errType)
			require.Empty(t, code)
			require.Empty(t, GetOpsStreamErrors(c))
		})
	}
}

func TestWriteLocalAnthropicInvalidRequest_SSEStarted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, prefix := range []string{": ping\n\n", "data: {\"type\": \"ping\"}\n\n"} {
		t.Run(strings.TrimSpace(prefix), func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Header("Content-Type", "text/event-stream; charset=utf-8")
			_, err := c.Writer.WriteString(prefix)
			require.NoError(t, err)
			c.Writer.Flush()

			err = WriteLocalAnthropicInvalidRequest(c, "context_management: Extra inputs are not permitted")

			require.Equal(t, ErrLocalAnthropicRejected, err)
			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, "text/event-stream; charset=utf-8", rec.Result().Header.Get("Content-Type"))
			require.True(t, rec.Flushed)
			id := c.Writer.Header().Get("request-id")
			require.Regexp(t, `^req_011C[1-9A-HJ-NP-Za-km-z]{20}$`, id)
			want := prefix + "event: error\ndata: " + `{"type":"error","error":{"type":"invalid_request_error","message":"context_management: Extra inputs are not permitted"},"request_id":"` + id + "\"}\n\n"
			require.Equal(t, want, rec.Body.String())
			require.True(t, IsOpsRequestScopedError(c))
			require.True(t, IsResponseCommitted(c))
			streamErr, ok := GetOpsStreamError(c)
			require.True(t, ok)
			require.Equal(t, "invalid_request_error", streamErr.ErrType)
			require.Equal(t, "context_management: Extra inputs are not permitted", streamErr.Message)
			require.Equal(t, http.StatusBadRequest, streamErr.IntendedStatus)
			require.True(t, streamErr.RequestScoped)
			require.False(t, streamErr.CountTowardsSLA)
		})
	}
}

func TestWriteLocalAnthropicInvalidRequest_Escaping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const message = "<tag>&\"quoted\"\\path\nline\r\t\x00 雪"
	for _, started := range []bool{false, true} {
		name := "JSON"
		if started {
			name = "SSE"
		}
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			const prefix = ": ping\n\n"
			if started {
				c.Header("Content-Type", "text/event-stream")
				_, err := c.Writer.WriteString(prefix)
				require.NoError(t, err)
				c.Writer.Flush()
			}

			require.Equal(t, ErrLocalAnthropicRejected, WriteLocalAnthropicInvalidRequest(c, message))

			id := c.Writer.Header().Get("request-id")
			want := `{"type":"error","error":{"type":"invalid_request_error","message":"<tag>&\"quoted\"\\path\nline\r\t\u0000 雪"},"request_id":"` + id + `"}`
			payload := rec.Body.String()
			if started {
				require.Equal(t, prefix+"event: error\ndata: "+want+"\n\n", payload)
				payload = strings.TrimSuffix(strings.TrimPrefix(payload, prefix+"event: error\ndata: "), "\n\n")
			} else {
				require.Equal(t, want, payload)
			}
			var decoded struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal([]byte(payload), &decoded))
			require.Equal(t, message, decoded.Error.Message)
		})
	}
}
