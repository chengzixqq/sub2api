//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type probeAccountSelectorStub struct{ account *Account }

func (s probeAccountSelectorStub) SelectAccountForModel(context.Context, *int64, string, string) (*Account, error) {
	return s.account, nil
}

func TestChannelMonitorProbeExecutor_OneGenerationAndBoundedBody(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, float64(64), body["max_tokens"])
		require.False(t, body["stream"].(bool))
		require.NotContains(t, body, "tools")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":4,"completion_tokens":1}}`)
	}))
	defer server.Close()
	a := &Account{ID: 8, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{"api_key": "not-a-real-key", "base_url": server.URL}}
	executor := &ChannelMonitorProbeExecutor{selector: probeAccountSelectorStub{a}, client: server.Client()}
	run := &ChannelMonitorProbeRun{GroupID: 1, Model: "gpt-test", Protocol: "openai_chat", StartedAt: time.Now()}
	executor.Execute(context.Background(), run)
	require.Equal(t, 1, calls)
	require.Equal(t, "healthy", run.Status)
	require.Equal(t, int64(4), run.InputTokens)
}

func TestChannelMonitorProbeExecutor_UnsupportedAccountNeverCalls(t *testing.T) {
	executor := &ChannelMonitorProbeExecutor{selector: probeAccountSelectorStub{&Account{ID: 3, Type: AccountTypeOAuth, Platform: PlatformOpenAI}}}
	run := &ChannelMonitorProbeRun{GroupID: 1, Model: "gpt-test", Protocol: "openai_responses", StartedAt: time.Now()}
	executor.Execute(context.Background(), run)
	require.Equal(t, "unsupported_account", run.ErrorClass)
	require.Equal(t, "error", run.Status)
	require.Nil(t, run.Success)
}

func TestChannelMonitorProbeExecutor_DoesNotFollowRedirectOrRetry(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Location", "/again")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	executor := NewChannelMonitorProbeExecutor(nil, nil)
	executor.validateURL = nil
	executor.selector = probeAccountSelectorStub{&Account{ID: 1, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{"api_key": "test", "base_url": server.URL}}}
	executor.client.Transport = server.Client().Transport
	run := &ChannelMonitorProbeRun{GroupID: 1, Model: "gpt-test", Protocol: "openai_chat", StartedAt: time.Now()}
	executor.Execute(context.Background(), run)
	require.Equal(t, 1, calls)
	require.Equal(t, "upstream_http", run.ErrorClass)
	require.NotNil(t, run.Success)
	require.False(t, *run.Success)
}

func TestChannelMonitorProbeAdapter_ProviderRouting(t *testing.T) {
	a := &Account{Type: AccountTypeAPIKey, Platform: PlatformGrok}
	_, base, ok := monitorProbeAdapter(a, "openai_chat", "grok-test")
	require.True(t, ok)
	require.NotContains(t, base, "openai.com")
	a = &Account{Type: AccountTypeAPIKey, Platform: PlatformOpenCodeGo, Credentials: map[string]any{"api_protocol": "responses"}}
	_, _, ok = monitorProbeAdapter(a, "openai_chat", "gpt-test")
	require.False(t, ok)
	_, _, ok = monitorProbeAdapter(a, "openai_responses", "gpt-test")
	require.True(t, ok)
}

func TestChannelMonitorProbeExecutor_GeminiThinkingDisabledAndUnsupportedNotHealthEvidence(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		cfg := body["generationConfig"].(map[string]any)
		require.Equal(t, float64(64), cfg["maxOutputTokens"])
		require.Equal(t, float64(0), cfg["thinkingConfig"].(map[string]any)["thinkingBudget"])
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"model cannot disable thinking"}}`)
	}))
	defer server.Close()
	a := &Account{ID: 8, Type: AccountTypeAPIKey, Platform: PlatformGemini, Credentials: map[string]any{"api_key": "test", "base_url": server.URL}}
	executor := &ChannelMonitorProbeExecutor{selector: probeAccountSelectorStub{a}, client: server.Client()}
	run := &ChannelMonitorProbeRun{GroupID: 1, Model: "gemini-2.5-pro", Protocol: "gemini", StartedAt: time.Now()}
	executor.Execute(context.Background(), run)
	require.Equal(t, 1, calls)
	require.Equal(t, "unsupported_request", run.ErrorClass)
	require.Nil(t, run.Success)
}

func TestChannelMonitorProbeExecutor_AuditsActualUsageOnSuccessAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		healthy bool
		usage   bool
	}{
		{"success", 200, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":8,"completion_tokens":3}}`, true, true},
		{"empty_output", 200, `{"choices":[],"usage":{"prompt_tokens":8,"completion_tokens":3}}`, false, true},
		{"error_envelope", 200, `{"error":{"message":"failed"},"usage":{"prompt_tokens":8,"completion_tokens":3}}`, false, true},
		{"http_error", 503, `{"error":{"message":"failed"},"usage":{"prompt_tokens":8,"completion_tokens":3}}`, false, true},
		{"missing_usage", 503, `{"error":{"message":"failed"}}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			a := &Account{ID: 8, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{"api_key": "test", "base_url": server.URL}}
			executor := &ChannelMonitorProbeExecutor{selector: probeAccountSelectorStub{a}, client: server.Client(), billing: NewBillingService(&config.Config{}, nil)}
			run := &ChannelMonitorProbeRun{GroupID: 1, Model: "gpt-5.4", Protocol: "openai_chat", StartedAt: time.Now()}
			executor.Execute(context.Background(), run)
			require.Equal(t, 1, calls)
			require.Equal(t, tc.healthy, *run.Success)
			if tc.usage {
				require.Equal(t, int64(8), run.InputTokens)
				require.Equal(t, int64(3), run.OutputTokens)
				require.NotNil(t, run.UpstreamCostUSD)
				require.Positive(t, *run.UpstreamCostUSD)
			} else {
				require.Nil(t, run.UpstreamCostUSD)
			}
		})
	}
}

func TestChannelMonitorProbeExecutor_ResponsesNullErrorIsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"completed","error":null,"output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":8,"output_tokens":3}}`)
	}))
	defer server.Close()
	a := &Account{ID: 8, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{"api_key": "test", "base_url": server.URL}}
	executor := &ChannelMonitorProbeExecutor{selector: probeAccountSelectorStub{a}, client: server.Client(), billing: NewBillingService(&config.Config{}, nil)}
	run := &ChannelMonitorProbeRun{GroupID: 1, Model: "gpt-5.4", Protocol: "openai_responses", StartedAt: time.Now()}
	executor.Execute(context.Background(), run)
	require.Equal(t, "healthy", run.Status)
	require.NotNil(t, run.UpstreamCostUSD)
	require.Positive(t, *run.UpstreamCostUSD)
	failed := &ChannelMonitorProbeRun{Protocol: "openai_responses"}
	executor.auditUsage(failed, "gpt-5.4", []byte(`{"response":{"status":"failed","usage":{"input_tokens":8,"output_tokens":3}}}`))
	require.Equal(t, int64(8), failed.InputTokens)
	require.Equal(t, int64(3), failed.OutputTokens)
	require.Equal(t, *run.UpstreamCostUSD, *failed.UpstreamCostUSD)
}
