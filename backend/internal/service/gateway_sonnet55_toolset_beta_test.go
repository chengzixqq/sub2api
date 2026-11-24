package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSonnet55ToolsetDropsLegacyStreamingBeta(t *testing.T) {
	svc := &GatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{InjectBetaForAPIKey: true}}}
	for _, tc := range []struct {
		name     string
		model    string
		toolType string
		wantDrop bool
	}{
		{"computer toolset", "claude-sonnet-5-5", "computer_toolset_20260801", true},
		{"browser toolset", "claude-sonnet-5-5", "browser_toolset_20260801", true},
		{"legacy computer", "claude-sonnet-5-5", "computer_20251124", false},
		{"older model", "claude-sonnet-5", "computer_toolset_20260801", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"` + tc.model + `","tools":[{"type":"` + tc.toolType + `"}],"messages":[{"role":"user","content":"hi"}]}`)
			headers := http.Header{}
			headers.Set("anthropic-beta", claude.BetaFineGrainedToolStreaming+",context-1m-2025-08-07")
			for _, tokens := range []struct {
				name       string
				count      bool
				clientBeta bool
			}{
				{"explicit messages", false, true},
				{"injected messages", false, false},
				{"explicit count_tokens", true, true},
				{"injected count_tokens", true, false},
			} {
				t.Run(tokens.name, func(t *testing.T) {
					requestHeaders := http.Header{}
					if tokens.clientBeta {
						requestHeaders = headers
					}
					var got string
					var set bool
					if tokens.count {
						got, set = svc.computeFinalCountTokensAnthropicBeta("apikey", false, tc.model, requestHeaders, body, nil)
					} else {
						got, set = svc.computeFinalAnthropicBeta("apikey", false, tc.model, requestHeaders, body, nil)
					}
					require.True(t, set)
					require.Equal(t, !tc.wantDrop, containsBetaToken(got, claude.BetaFineGrainedToolStreaming))
					if tokens.clientBeta {
						require.True(t, containsBetaToken(got, claude.BetaContext1M))
					}
				})
			}
		})
	}
}

func TestSonnet55ToolsetBetaFilteredAfterAccountOverrideAndOnVertex(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-5-5","max_tokens":128,"tools":[{"type":"browser_toolset_20260801"}],"messages":[{"role":"user","content":"hi"}]}`)
	account := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			credKeyHeaderOverrideEnabled: true,
			credKeyHeaderOverrides:       map[string]any{"anthropic-beta": claude.BetaFineGrainedToolStreaming + ",context-1m-2025-08-07"},
		}}
	svc := &GatewayService{cfg: &config.Config{}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	for _, build := range []struct {
		name string
		fn   func() (*http.Request, error)
	}{
		{"normal", func() (*http.Request, error) {
			req, _, err := svc.buildUpstreamRequest(context.Background(), c, account, body, "key", "apikey", "claude-sonnet-5-5", false, false)
			return req, err
		}},
		{"passthrough", func() (*http.Request, error) {
			req, _, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(context.Background(), c, account, body, "key")
			return req, err
		}},
	} {
		t.Run(build.name, func(t *testing.T) {
			req, err := build.fn()
			require.NoError(t, err)
			header := getHeaderRaw(req.Header, "anthropic-beta")
			require.False(t, containsBetaToken(header, claude.BetaFineGrainedToolStreaming))
			require.True(t, containsBetaToken(header, claude.BetaContext1M))
		})
	}

	vertexContext := newVertexBetaTestContext(t, claude.BetaFineGrainedToolStreaming+","+claude.BetaContext1M)
	req, _, err := svc.buildUpstreamRequest(context.Background(), vertexContext, newVertexServiceAccount(9001), body,
		"vertex-token", "service_account", "claude-sonnet-5-5@20260928", false, false)
	require.NoError(t, err)
	vertexHeader := getHeaderRaw(req.Header, "anthropic-beta")
	require.False(t, containsBetaToken(vertexHeader, claude.BetaFineGrainedToolStreaming))
	require.True(t, containsBetaToken(vertexHeader, claude.BetaContext1M))

	nativeAccount := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			credKeyHeaderOverrideEnabled: true,
			credKeyHeaderOverrides:       map[string]any{"anthropic-beta": claude.BetaFineGrainedToolStreaming + ",context-1m-2025-08-07"},
		}}
	native := &OpenAIGatewayService{}
	nativeReq, _, err := native.buildNativeAnthropicUpstreamRequest(context.Background(), c, nativeAccount, body,
		"key", "https://api.anthropic.com/v1/messages")
	require.NoError(t, err)
	nativeHeader := getHeaderRaw(nativeReq.Header, "anthropic-beta")
	require.False(t, containsBetaToken(nativeHeader, claude.BetaFineGrainedToolStreaming))
	require.True(t, containsBetaToken(nativeHeader, claude.BetaContext1M))
}

func TestAnthropicHeaderTemplateRespectsFinalBetaPolicy(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-5-5","tools":[{"type":"browser_toolset_20260801"}],"messages":[{"role":"user","content":"hi"}]}`)
	account := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{
		credKeyHeaderOverrideEnabled: true,
		credKeyHeaderOverrides: map[string]any{
			"anthropic-beta": claude.BetaFineGrainedToolStreaming + "," + claude.BetaContext1M + ",user-{{user_id}}",
			"x-report-user":  "user-{{user_id}}",
		},
	}}
	svc := &GatewayService{cfg: &config.Config{}}
	for _, authenticated := range []bool{true, false} {
		ctx := t.Context()
		if authenticated {
			ctx = WithHeaderOverrideRequestScope(ctx, 7, 11, nil)
		}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/messages", nil)
		c.Request.Header.Set("anthropic-beta", "client-supplied")
		for _, build := range []struct {
			name string
			fn   func() (*http.Request, []byte, error)
		}{
			{"messages", func() (*http.Request, []byte, error) {
				return svc.buildUpstreamRequest(ctx, c, account, body, "key", "apikey", "claude-sonnet-5-5", false, false)
			}},
			{"count_tokens", func() (*http.Request, []byte, error) {
				return svc.buildCountTokensRequest(ctx, c, account, body, "key", "apikey", "claude-sonnet-5-5", false)
			}},
			{"passthrough", func() (*http.Request, []byte, error) {
				return svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(ctx, c, account, body, "key")
			}},
			{"native", func() (*http.Request, []byte, error) {
				return (&OpenAIGatewayService{}).buildNativeAnthropicUpstreamRequest(ctx, c, account, body, "key", "https://example.test/v1/messages")
			}},
		} {
			t.Run(build.name, func(t *testing.T) {
				req, _, err := build.fn()
				require.NoError(t, err)
				beta := getHeaderRaw(req.Header, "anthropic-beta")
				if authenticated {
					require.Equal(t, "user-7", getHeaderRaw(req.Header, "x-report-user"))
					require.True(t, containsBetaToken(beta, "user-7"), beta)
					require.True(t, containsBetaToken(beta, claude.BetaContext1M), beta)
					require.False(t, containsBetaToken(beta, claude.BetaFineGrainedToolStreaming), beta)
				} else {
					require.Empty(t, getHeaderRaw(req.Header, "x-report-user"))
					require.Empty(t, beta)
					require.Empty(t, headerValuesFold(req.Header, "anthropic-beta"))
				}
			})
		}
	}
}
