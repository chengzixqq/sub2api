//go:build unit

package service

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestBedrockCCEarlyForwardReads(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		status       int
	}{
		{"validation", `"thinking":{"type":"between_tools"},"output_config":{"effort":"xhigh"}`, 400},
		{"advisor", `"tools":[{"type":"advisor_20260301","name":"advisor","model":"unpriced"}]`, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &betaMatrixRepo{}
			svc := fallbackNativeGatewayForTest(t, nil, nil)
			svc.settingService = NewSettingService(repo, svc.cfg)
			if prime, ok := any(svc.settingService).(interface {
				AnyAlignmentReject(context.Context, string) bool
			}); ok {
				prime.AnyAlignmentReject(t.Context(), "unknown_beta_header")
			}
			repo.reads = 0
			svc.channelService = &ChannelService{}
			svc.channelService.cache.Store(&channelCache{loadedAt: time.Now(), channelByGroupID: map[int64]*Channel{1: {Status: StatusActive, FeaturesConfig: map[string]any{featureKeyBedrockCCCompat: true}}}})
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
			c.Request.Header.Set("Anthropic-Beta", "claude-code-20250219,context-management-2025-06-27")
			c.Request.Header.Set("User-Agent", "claude-cli/2.1.328 (external, cli)")
			account := fallbackNativeAccountForTest(false)
			gid := int64(1)
			body := []byte(`{"model":"claude-sonnet-5-5","max_tokens":1024,"messages":[{"role":"user","content":"hello"}],"context_management":{"edits":[]},` + tc.fields + `}`)
			body = svc.ApplyBedrockCCCompat(c, body, "claude-sonnet-5-5", account, &gid)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
			require.NoError(t, err)
			_, err = svc.Forward(t.Context(), c, account, parsed)
			require.Error(t, err)
			t.Logf("EARLY %s status=%d body=%q reads=%d", tc.name, rec.Code, rec.Body.String(), repo.reads)
			require.Equal(t, tc.status, rec.Code)
			require.Zero(t, repo.reads)
		})
	}
}

func TestTypeSafeCountTokensRedaction(t *testing.T) {
	repo := &betaMatrixRepo{}
	svc := fallbackNativeGatewayForTest(t, &anthropicHTTPUpstreamRecorder{resp: nativeAnthropicBufferedResponse()}, nil)
	svc.settingService = NewSettingService(repo, svc.cfg)
	require.NoError(t, repo.Set(t.Context(), SettingKeyClaudeCustomization, `{"preset":"custom","url_redaction_enabled":false}`))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages/count_tokens", nil)
	account := fallbackNativeAccountForTest(false)
	account.Platform = PlatformTypeSafe
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)), PlatformAnthropic)
	require.NoError(t, err)
	require.NoError(t, svc.ForwardCountTokens(t.Context(), c, account, parsed))
	value, ok := c.Get(redactUpstreamURLContextKey)
	t.Logf("REDACTION flag=%v reads=%d", value, repo.reads)
	require.True(t, ok)
	require.Equal(t, false, value)
	require.LessOrEqual(t, repo.reads, 3)
}
