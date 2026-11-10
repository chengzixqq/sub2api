//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const (
	geminiRetryTest503Body       = `{"error":{"code":503,"message":"The model is overloaded.","status":"UNAVAILABLE"}}`
	geminiRetryTestSignatureBody = `{"error":{"code":400,"message":"Function call is missing a thought_signature in functionCall parts.","status":"INVALID_ARGUMENT"}}`
	geminiRetryTestOKBody        = `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"totalTokenCount":4}}`
)

func geminiSameAccountRetryTestAccount() *Account {
	return &Account{
		ID:          840,
		Platform:    PlatformGemini,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "test-key"},
	}
}

func TestGeminiForward_SameAccountRetriesFollowConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, retries := range []int{0, 1} {
		for _, fw := range geminiSingleCheckForwarders {
			t.Run(fmt.Sprintf("%s/retries=%d", fw.name, retries), func(t *testing.T) {
				svc, upstream, _ := newGeminiSingleCheckService(geminiSequenceUpstreamResponse{status: http.StatusServiceUnavailable, body: geminiRetryTest503Body})
				svc.cfg.Gateway.MaxSameAccountRetriesGemini = retries

				err, _ := fw.forward(t, svc, geminiSameAccountRetryTestAccount())

				var failoverErr *UpstreamFailoverError
				require.True(t, errors.As(err, &failoverErr), "同号重试耗尽应换号，got %T: %v", err, err)
				require.Equal(t, http.StatusServiceUnavailable, failoverErr.StatusCode)
				require.Equal(t, retries+1, upstream.calls)
			})
		}
	}
}

func TestGeminiClaudeCompat_SignatureDowngradeDoesNotConsumeSameAccountRetries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	forward := func(svc *GeminiMessagesCompatService) (*ForwardResult, error) {
		svc.settingService = &SettingService{}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":[{"type":"thinking","thinking":"history","signature":"bad"},{"type":"tool_use","id":"toolu_1","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"found"}]}]}`)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
		account := geminiSameAccountRetryTestAccount()
		account.Credentials["model_mapping"] = map[string]any{"claude-sonnet-4-5": "gemini-2.5-flash"}
		policy := DefaultClaudeCustomizationSettings()
		policy.Preset = ClaudePresetCustom
		policy.ThinkingSignatureRetryEnabled, policy.ThinkingToolDowngradeRetryEnabled = true, true
		c.Set(claudeCustomizationRequestContextKey, claudeCustomizationRequestPolicy{accountID: account.ID, policy: policy})
		return svc.Forward(context.Background(), c, account, body)
	}

	t.Run("effective downgrade runs without same-account retries", func(t *testing.T) {
		svc, upstream, _ := newGeminiSingleCheckService(
			geminiSequenceUpstreamResponse{status: http.StatusBadRequest, body: geminiRetryTestSignatureBody},
			geminiSequenceUpstreamResponse{status: http.StatusOK, body: geminiRetryTestOKBody},
		)
		svc.cfg.Gateway.MaxSameAccountRetriesGemini = 0

		result, err := forward(svc)

		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, 2, upstream.calls)
	})

	t.Run("same-account retries are counted apart from downgrades", func(t *testing.T) {
		svc, upstream, _ := newGeminiSingleCheckService(
			geminiSequenceUpstreamResponse{status: http.StatusServiceUnavailable, body: geminiRetryTest503Body},
			geminiSequenceUpstreamResponse{status: http.StatusBadRequest, body: geminiRetryTestSignatureBody},
			geminiSequenceUpstreamResponse{status: http.StatusServiceUnavailable, body: geminiRetryTest503Body},
		)
		svc.cfg.Gateway.MaxSameAccountRetriesGemini = 1

		_, err := forward(svc)

		var failoverErr *UpstreamFailoverError
		require.True(t, errors.As(err, &failoverErr), "同号重试耗尽应换号，got %T: %v", err, err)
		require.Equal(t, http.StatusServiceUnavailable, failoverErr.StatusCode)
		require.Equal(t, 3, upstream.calls)
	})
}
