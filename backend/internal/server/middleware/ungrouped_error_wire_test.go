//go:build unit

package middleware

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUngroupedKeyErrorWire(t *testing.T) {
	settings := service.NewSettingService(fakeSettingRepo{values: map[string]string{service.SettingKeyAllowUngroupedKeyScheduling: "false"}}, &config.Config{})
	const msg = "API Key is not assigned to any group and cannot be used. Please contact the administrator to assign it to a group."
	for _, tc := range []struct {
		method, path   string
		claude, google bool
	}{
		{"POST", "/v1/chat/completions", false, false}, {"POST", "/v1/responses", false, false}, {"POST", "/v1/embeddings", false, false},
		{"POST", "/v1/images/generations", false, false}, {"POST", "/v1/videos", false, false}, {"GET", "/v1/models", false, false},
		{"POST", "/responses", false, false}, {"POST", "/backend-api/codex/responses", false, false}, {"GET", "/backend-api/codex/models", false, false},
		{"POST", "/api/v3/contents/generations/tasks", false, false}, {"POST", "/v1/systemone", false, false}, {"GET", "/v1beta/models", false, true},
		{"GET", "/antigravity/models", false, false}, {"GET", "/antigravity/models", true, false}, {"GET", "/antigravity/v1/models", true, false},
		{"GET", "/v1/models?client_version=1", false, false}, {"GET", "/models?client_version=1", false, false}, {"POST", "/v1/messages", true, false}, {"POST", "/v1/messages/count_tokens", true, false},
		{"GET", "/v1/models", true, false}, {"GET", "/v1/models/:model", true, false}, {"GET", "/models/:model", true, false}, {"POST", "/antigravity/v1/messages", true, false},
	} {
		for _, fill := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/claude=%t/fill=%t", tc.path, tc.claude, fill), func(t *testing.T) {
				const id = "req_011C123456789ABCDEFGHJKL"
				r := gin.New()
				w := GatewayErrorWriter(AnthropicErrorWriter)
				if tc.google {
					w = GoogleErrorWriter
				}
				route, _, codex := strings.Cut(tc.path, "?")
				r.Handle(tc.method, route, func(c *gin.Context) {
					c.Set(string(ContextKeyAPIKey), &service.APIKey{ID: 1})
					if fill {
						c.Set("anthropic_filled_request_id", id)
					}
				}, RequireGroupAssignment(settings, w))
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(tc.method, tc.path, nil)
				if codex {
					req.Header.Set("x-api-key", "test")
				}
				if tc.claude {
					req.Header.Set("Anthropic-Version", "2023-06-01")
				} else if !codex {
					req.Header.Set("Authorization", "Bearer test")
				}
				r.ServeHTTP(rec, req)
				typ := "application/json; charset=utf-8"
				want := `{"error":{"message":"` + msg + `","type":"permission_error"},"type":"error"}`
				switch {
				case tc.google:
					want = `{"error":{"code":403,"message":"` + msg + `","status":"PERMISSION_DENIED"}}`
				case tc.claude && route != "/antigravity/models" && route != "/antigravity/v1/models" && !strings.HasSuffix(route, "/:model"):
					typ = "application/json"
					want = `{"type":"error","error":{"type":"permission_error","message":"` + msg + `"}`
					if fill {
						want += `,"request_id":"` + id + `"`
					}
					want += "}"
				}
				t.Logf("WIRE %s %d %q %q", t.Name(), rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
				require.Equal(t, 403, rec.Code)
				require.Equal(t, typ, rec.Header().Get("Content-Type"))
				require.Equal(t, want, rec.Body.String())
			})
		}
	}
}

// Detail routes use the production error protocol even when a Claude client
// sends either discovery header, and even when request-id fill is available.
func TestModelDetailUngroupedHeaderCombinations(t *testing.T) {
	settings := service.NewSettingService(fakeSettingRepo{values: map[string]string{service.SettingKeyAllowUngroupedKeyScheduling: "false"}}, &config.Config{})
	const message = "API Key is not assigned to any group and cannot be used. Please contact the administrator to assign it to a group."
	for _, path := range []string{"/v1/models/:model", "/models/:model"} {
		for mask := 0; mask < 8; mask++ {
			for _, fill := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/headers=%d/fill=%t", path, mask, fill), func(t *testing.T) {
					router := gin.New()
					router.GET(path, func(c *gin.Context) {
						c.Set(string(ContextKeyAPIKey), &service.APIKey{ID: 1})
						if fill {
							c.Set("anthropic_filled_request_id", "req_011C123456789ABCDEFGHJKL")
						}
					}, RequireGroupAssignment(settings, AnthropicErrorWriter))
					req := httptest.NewRequest("GET", strings.ReplaceAll(path, ":model", "claude-test"), nil)
					if mask&1 != 0 {
						req.Header.Set("Authorization", "Bearer test")
					}
					if mask&2 != 0 {
						req.Header.Set("x-api-key", "test")
					}
					if mask&4 != 0 {
						req.Header.Set("anthropic-version", "2023-06-01")
					}
					rec := httptest.NewRecorder()
					router.ServeHTTP(rec, req)
					require.Equal(t, 403, rec.Code)
					require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
					require.Equal(t, `{"error":{"message":"`+message+`","type":"permission_error"},"type":"error"}`, rec.Body.String())
				})
			}
		}
	}
}

func TestAnthropicErrorWriterUnauthorizedOpsLiteral(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	AnthropicErrorWriter(c, 401, "Invalid API key")
	require.Equal(t, 401, rec.Code)
	require.Equal(t, `{"type":"error","error":{"type":"authentication_error","message":"Invalid API key"}}`, rec.Body.String())
	require.Equal(t, "api_error", c.GetString(service.OpsAnthropicErrorTypeKey))
	body, ok := service.OpsOriginalResponseBody(c)
	require.True(t, ok)
	require.Equal(t, `{"error":{"message":"Invalid API key","type":"api_error"},"type":"error"}`, string(body))
}
