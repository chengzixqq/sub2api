//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type monitorProbeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f monitorProbeRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestChannelMonitorProbeExecutor_AccountEndpointPolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		base   string
		policy config.URLAllowlistConfig
		allow  bool
	}{
		{"configured public HTTP", "http://8.8.8.8/anthropic", config.URLAllowlistConfig{AllowInsecureHTTP: true}, true},
		{"HTTP disabled", "http://8.8.8.8/anthropic", config.URLAllowlistConfig{}, false},
		{"public HTTPS", "https://8.8.8.8/anthropic", config.URLAllowlistConfig{}, true},
		{"listed HTTPS", "https://8.8.8.8/anthropic", config.URLAllowlistConfig{Enabled: true, UpstreamHosts: []string{"8.8.8.8"}}, true},
		{"unlisted HTTPS", "https://8.8.8.8/anthropic", config.URLAllowlistConfig{Enabled: true, UpstreamHosts: []string{"1.1.1.1"}}, false},
		{"allowlist requires HTTPS", "http://8.8.8.8/anthropic", config.URLAllowlistConfig{Enabled: true, AllowInsecureHTTP: true, UpstreamHosts: []string{"8.8.8.8"}}, false},
		{"private HTTP", "http://10.0.0.1/anthropic", config.URLAllowlistConfig{AllowInsecureHTTP: true, AllowPrivateHosts: true}, false},
		{"private HTTPS", "https://127.0.0.1/anthropic", config.URLAllowlistConfig{AllowPrivateHosts: true}, false},
		{"metadata HTTP", "http://169.254.169.254/anthropic", config.URLAllowlistConfig{AllowInsecureHTTP: true}, false},
		{"userinfo", "https://user:pass@8.8.8.8/anthropic", config.URLAllowlistConfig{}, false},
		{"query", "http://8.8.8.8/anthropic?mode=test", config.URLAllowlistConfig{AllowInsecureHTTP: true}, false},
		{"fragment", "http://8.8.8.8/anthropic#fragment", config.URLAllowlistConfig{AllowInsecureHTTP: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gateway := &GatewayService{cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: tc.policy}}}
			executor := NewChannelMonitorProbeExecutor(gateway, nil)
			executor.selector = probeAccountSelectorStub{&Account{
				ID: 1, Type: AccountTypeAPIKey, Platform: PlatformAnthropic,
				Credentials: map[string]any{"api_key": "fixture-key", "base_url": tc.base},
			}}
			executor.acquire = nil
			calls := 0
			executor.client.Transport = monitorProbeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, tc.base+"/v1/messages", req.URL.String())
				require.Equal(t, http.MethodPost, req.Method)
				require.Equal(t, "fixture-key", req.Header.Get("X-Api-Key"))
				return &http.Response{
					StatusCode: http.StatusOK, Header: make(http.Header),
					Body: io.NopCloser(strings.NewReader(`{"content":[{"type":"text","text":"OK"}]}`)),
				}, nil
			})
			run := &ChannelMonitorProbeRun{GroupID: 1, Model: "claude-test", Protocol: "anthropic"}
			executor.Execute(context.Background(), run)
			t.Logf("status=%s error_class=%s outbound_calls=%d", run.Status, run.ErrorClass, calls)
			if tc.allow {
				require.Equal(t, "healthy", run.Status)
				require.NotNil(t, run.Success)
				require.True(t, *run.Success)
				require.Equal(t, 1, calls)
			} else {
				require.Equal(t, "endpoint_rejected", run.ErrorClass)
				require.Nil(t, run.Success)
				require.Zero(t, calls)
			}
		})
	}
}
