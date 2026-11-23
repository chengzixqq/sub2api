//go:build unit

package service

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

type passthroughReadCounter struct {
	fallbackPolicySettingRepo
	reads []string
}

func (r *passthroughReadCounter) GetValue(ctx context.Context, key string) (string, error) {
	r.reads = append(r.reads, key)
	return r.fallbackPolicySettingRepo.GetValue(ctx, key)
}
func TestPassthroughSettingsReadBudget(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		for _, shape := range []string{"plain", "field", "header"} {
			t.Run(path+"/"+shape, func(t *testing.T) {
				repo := &passthroughReadCounter{}
				svc := fallbackNativeGatewayForTest(t, &anthropicHTTPUpstreamRecorder{resp: nativeAnthropicBufferedResponse()}, nil)
				svc.settingService = NewSettingService(repo, svc.cfg)
				// Production primes the alignment snapshot at startup. Older versions have no snapshot.
				if prime, ok := any(svc.settingService).(interface {
					AnyAlignmentReject(context.Context, string) bool
				}); ok {
					prime.AnyAlignmentReject(t.Context(), "unknown_beta_header")
				}
				repo.reads = nil
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, path, nil)
				field := ""
				if shape == "field" {
					field = `"context_management":{},`
				}
				if shape == "header" {
					c.Request.Header.Set("anthropic-beta", "context-management-2025-06-27")
				}
				body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":15,` + field + `"messages":[{"role":"user","content":"hi"}]}`)
				parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
				require.NoError(t, err)
				account := fallbackNativeAccountForTest(true)
				if path == "/v1/messages" {
					_, err = svc.Forward(t.Context(), c, account, parsed)
				} else {
					err = svc.ForwardCountTokens(t.Context(), c, account, parsed)
				}
				require.NoError(t, err)
				require.Equal(t, []string{SettingKeyClaudeCustomization}, repo.reads)
			})
		}
	}
}
