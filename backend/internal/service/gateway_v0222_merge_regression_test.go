//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayStreamingResponse_AdvisorMarkersSurviveFastPath(t *testing.T) {
	for _, withIterations := range []bool{false, true} {
		t.Run(fmt.Sprintf("iterations=%v", withIterations), func(t *testing.T) {
			frames := []string{
				"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fast\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4\",\"content\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\n",
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"advisor_tool_result\",\"tool_use_id\":\"tool_advisor\",\"content\":[]}}\n\n",
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
			}
			if withIterations {
				frames = append(frames, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":2,\"output_tokens\":3,\"iterations\":[{\"type\":\"message\",\"input_tokens\":2,\"output_tokens\":3},{\"type\":\"advisor_message\",\"model\":\"advisor-test\",\"input_tokens\":10,\"output_tokens\":20}]}}\n\n")
			}
			frames = append(frames, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(strings.Join(frames, "")))}
			defer resp.Body.Close()
			c := partialUsageTestContext("/v1/messages")
			result, err := newMinimalGatewayService().handleStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "claude-sonnet-4", "claude-sonnet-4", false)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotNil(t, result.usage.IterationUsage, "advisor markers without usage must remain observable")
			require.True(t, result.usage.IterationUsage.AdvisorSeen)
			require.True(t, result.usage.IterationUsage.Terminal, "message_stop finalizes iteration billing even on the JSON fast path")
			require.Equal(t, withIterations, result.usage.HasAdvisorUsage())
			if withIterations {
				require.True(t, claudeExecutorIterationsComplete(*result.usage))
			}
		})
	}
}

func TestGatewayUpstreamErrorAfterKeepalive_PreservesURLRedaction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, bedrock := range []bool{false, true} {
		t.Run(fmt.Sprintf("bedrock=%v", bedrock), func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Set(redactUpstreamURLContextKey, true)
			c.Header("Content-Type", "text/event-stream")
			const ping = ": ping\n\n"
			_, err := c.Writer.WriteString(ping)
			require.NoError(t, err)
			c.Writer.Flush()
			status := http.StatusBadRequest
			body := `{"error":{"type":"invalid_request_error","message":"Invalid endpoint https://private.example/v1/messages?key=secret"}}`
			account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
			if bedrock {
				status = http.StatusBadGateway
				account.Type = AccountTypeBedrock
				body = `{"message":"blocked by content filtering policy: https://private.example/v1/messages?key=secret"}`
			}
			resp := &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
			_, err = (&GatewayService{}).handleErrorResponse(context.Background(), resp, c, account)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "private.example")
			require.NotContains(t, rec.Body.String(), "private.example")
			require.NotContains(t, rec.Body.String(), "secret")
			payload := requireGatewayKeepaliveErrorEvent(t, rec.Body.String(), ping)
			require.Equal(t, "invalid_request_error", payload.Error.Type)
			require.Contains(t, payload.Error.Message, "https://***.***/v1/messages")
			streamError, ok := GetOpsStreamError(c)
			require.True(t, ok)
			require.Equal(t, http.StatusBadRequest, streamError.IntendedStatus)
		})
	}
}
