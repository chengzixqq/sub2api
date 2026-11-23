//go:build unit

package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMissingResponsesUsageSucceedsForOpenAIAndGrok(t *testing.T) {
	const body = `{"object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}]}`
	for _, platform := range []string{PlatformOpenAI, PlatformGrok} {
		t.Run(platform, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
			account := rawChatCompletionsTestAccount()
			account.Platform = platform
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
			result, err := svc.handleNonStreamingResponse(t.Context(), resp, c, account, "m", "m")
			require.NoError(t, err)
			require.True(t, result.delivered)
			require.NotContains(t, rec.Body.String(), `"usage"`)
		})
	}
}

func TestMissingResponsesUsageBindsDeliveredResponseOwner(t *testing.T) {
	for _, id := range []string{"resp_delivered", ""} {
		t.Run(id, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.4","input":"hello","stream":false}`)
			resultBody := `{"object":"response","id":"` + id + `","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}]}`
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(resultBody))}}
			cache := &responseBindContextProbeCache{}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream, cache: cache}
			c := newOpenAIPartialUsageContext(body)
			groupID := int64(42)
			c.Set("api_key", &APIKey{ID: 501, GroupID: &groupID})
			SetOpenAIHTTPResponseOwner(c, 601, 501)
			result, err := svc.Forward(t.Context(), c, rawChatCompletionsTestAccount(), body)
			require.NoError(t, err)
			require.True(t, result.ClientContentDelivered)
			if id == "" {
				require.Empty(t, cache.sessionBindings)
				return
			}
			require.Len(t, cache.sessionBindings, 3)
			owned, err := svc.ValidateOpenAIHTTPResponseOwner(t.Context(), groupID, id, 601, 501)
			require.NoError(t, err)
			require.True(t, owned)
			owned, err = svc.ValidateOpenAIHTTPResponseOwner(t.Context(), groupID, id, 602, 501)
			require.NoError(t, err)
			require.False(t, owned)
		})
	}
}

func TestMissingUsageRejectsErrorEnvelopeWithOutput(t *testing.T) {
	for _, body := range []string{
		`{"type":"error","status":"completed","output":[]}`,
		`{"type":"response.failed","object":"response","output":[]}`,
		`{"success":false,"object":"response","output":[]}`,
	} {
		require.False(t, completedResponsesWithoutUsage([]byte(body)), body)
	}
}

func TestMissingUsageRejectsUnfinishedResponses(t *testing.T) {
	for _, field := range []string{
		`"type":"response.in_progress"`, `"type":"response.queued"`, `"type":"response.created"`,
		`"type":"response.incomplete"`, `"type":"response.failed"`, `"status":null`,
		`"incomplete_details":{"reason":"max_output_tokens"}`,
	} {
		t.Run(field, func(t *testing.T) {
			require.False(t, completedResponsesWithoutUsage([]byte(`{"object":"response","output":[],`+field+"}")))
		})
	}
	require.True(t, completedResponsesWithoutUsage([]byte(`{"object":"response","output":[],"incomplete_details":null}`)))
}
