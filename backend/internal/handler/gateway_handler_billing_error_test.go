package handler

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayLocalErrorTypesFollowAnthropicStatus(t *testing.T) {
	for _, tc := range []struct {
		name, code, wantType string
		status               int
	}{
		{"advisor pricing", "advisor_pricing_unavailable", "api_error", 503},
		{"probe budget", "probe_unavailable", "api_error", 503},
		{"probe billing", "probe_billing_unavailable", "api_error", 503},
		{"billing", "billing_error", "permission_error", 403},
		{"rate limit", "rate_limit_exceeded", "rate_limit_error", 429},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"/v1/messages", "/v1/usage"} {
				r := gin.New()
				r.POST(path, func(c *gin.Context) { (&GatewayHandler{}).errorResponse(c, tc.status, tc.code, "Local failure") })
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
				wantType := tc.code
				if path == "/v1/messages" {
					wantType = tc.wantType
				}
				require.Equal(t, tc.status, w.Code)
				require.JSONEq(t, `{"type":"error","error":{"type":"`+wantType+`","message":"Local failure"}}`, w.Body.String())
			}
		})
	}
}

func TestGatewayLocalStreamingErrorTypesIncludeUpstreamFailures(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, local := range []bool{false, true} {
			t.Run(fmt.Sprintf("streaming=%t/local=%t", streaming, local), func(t *testing.T) {
				r := gin.New()
				r.POST("/v1/messages", func(c *gin.Context) {
					h := &GatewayHandler{}
					if local {
						h.handleLocalStreamingAwareError(c, 503, "probe_unavailable", "Probe attempt budget exhausted", streaming)
					} else {
						h.handleStreamingAwareError(c, 503, "upstream_error", "Upstream request failed", streaming)
					}
				})
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
				body := w.Body.String()
				if streaming {
					require.Equal(t, 200, w.Code)
					require.True(t, strings.HasPrefix(body, "event: error\ndata: "))
					body = strings.TrimSpace(strings.TrimPrefix(body, "event: error\ndata: "))
				} else {
					require.Equal(t, 503, w.Code)
				}
				if local {
					require.JSONEq(t, `{"type":"error","error":{"type":"api_error","message":"Probe attempt budget exhausted"}}`, body)
				} else {
					require.JSONEq(t, `{"type":"error","error":{"type":"api_error","message":"Upstream request failed"}}`, body)
				}
			})
		}
	}
}

func TestBillingErrorDetailsForRequestPreservesStatusAndRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		err                    error
		status                 int
		message, legacyMessage string
	}{
		{"quota", service.ErrAPIKeyQuotaExhausted, 403, "API key quota has been exhausted", "api key 额度已用完"},
		{"expired", service.ErrAPIKeyExpired, 403, "API key has expired", "api key 已过期"},
		{"unavailable", service.ErrBillingServiceUnavailable, 503, "Billing service temporarily unavailable. Please retry later.", "Billing service temporarily unavailable. Please retry later."},
		{"platform quota", service.ErrUserPlatformDailyQuotaExhausted.WithMetadata(map[string]string{"window_resets_at": time.Now().Add(time.Minute).UTC().Format(time.RFC3339)}), 429, "Daily usage quota exhausted for this platform.", "Daily usage quota exhausted for this platform."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"/v1/messages", "/v1/chat/completions"} {
				r := gin.New()
				r.POST(path, func(c *gin.Context) {
					status, code, message, retryAfter := billingErrorDetailsForRequest(c, fmt.Errorf("wrapped: %w", tc.err))
					require.Equal(t, tc.status, status)
					if tc.name == "platform quota" {
						require.GreaterOrEqual(t, retryAfter, 59)
						require.LessOrEqual(t, retryAfter, 60)
					} else {
						require.Zero(t, retryAfter)
					}
					if path == "/v1/messages" {
						require.Equal(t, tc.message, message)
						wantType := map[int]string{403: "billing_error", 429: "rate_limit_exceeded", 503: "billing_service_error"}[tc.status]
						require.Equal(t, wantType, code)
					} else {
						require.Equal(t, tc.legacyMessage, message)
						wantType := map[int]string{403: "billing_error", 429: "rate_limit_exceeded", 503: "billing_service_error"}[tc.status]
						require.Equal(t, wantType, code)
					}
				})
				r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, nil))
			}
		})
	}
}

func TestBillingErrorDetails_MapsGroupRPMExceededToTooManyRequests(t *testing.T) {
	status, code, msg, retryAfter := billingErrorDetails(service.ErrGroupRPMExceeded)
	require.Equal(t, http.StatusTooManyRequests, status)
	require.Equal(t, "rate_limit_exceeded", code)
	require.NotEmpty(t, msg)
	require.Greater(t, retryAfter, 0, "RPM exceeded should return positive Retry-After")
	require.LessOrEqual(t, retryAfter, 60)
}

func TestBillingErrorDetails_MapsUserRPMExceededToTooManyRequests(t *testing.T) {
	status, code, msg, retryAfter := billingErrorDetails(service.ErrUserRPMExceeded)
	require.Equal(t, http.StatusTooManyRequests, status)
	require.Equal(t, "rate_limit_exceeded", code)
	require.NotEmpty(t, msg)
	require.Greater(t, retryAfter, 0, "RPM exceeded should return positive Retry-After")
	require.LessOrEqual(t, retryAfter, 60)
}

func TestBillingErrorDetails_APIKeyRateLimitStillMaps(t *testing.T) {
	// 回归保护：加 RPM 分支后不应影响已有 APIKey rate limit 的映射。
	for _, err := range []error{
		service.ErrAPIKeyRateLimit5hExceeded,
		service.ErrAPIKeyRateLimit1dExceeded,
		service.ErrAPIKeyRateLimit7dExceeded,
	} {
		status, code, _, _ := billingErrorDetails(err)
		require.Equal(t, http.StatusTooManyRequests, status, "status for %v", err)
		require.Equal(t, "rate_limit_exceeded", code)
	}
}

func TestBillingErrorDetails_BillingServiceUnavailableMapsTo503(t *testing.T) {
	status, code, _, retryAfter := billingErrorDetails(service.ErrBillingServiceUnavailable)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "billing_service_error", code)
	require.Equal(t, 0, retryAfter, "non-RPM errors should not set Retry-After")
}

func TestBillingErrorDetails_UnknownErrorFallsBackTo403(t *testing.T) {
	status, code, msg, _ := billingErrorDetails(service.ErrInsufficientBalance)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "billing_error", code)
	require.NotEmpty(t, msg)
}

func TestExtractQuotaResetSeconds_T19_HappyPath(t *testing.T) {
	err := service.ErrUserPlatformDailyQuotaExhausted.WithMetadata(map[string]string{
		"window_resets_at": time.Now().Add(10 * time.Second).UTC().Format(time.RFC3339),
	})
	got := extractQuotaResetSeconds(err)
	if got < 10 || got > 11 {
		t.Errorf("T19: got %d, want 10 or 11 (math.Ceil boundary)", got)
	}
}

func TestExtractQuotaResetSeconds_T20_NoMetadataFallback(t *testing.T) {
	if got := extractQuotaResetSeconds(errors.New("naked error")); got != 60 {
		t.Errorf("T20: got %d, want 60 fallback", got)
	}
}

func TestExtractQuotaResetSeconds_T21_BadFormatFallback(t *testing.T) {
	err := service.ErrUserPlatformDailyQuotaExhausted.WithMetadata(map[string]string{
		"window_resets_at": "not-a-time",
	})
	if got := extractQuotaResetSeconds(err); got != 60 {
		t.Errorf("T21: got %d, want 60 fallback", got)
	}
}

func TestExtractQuotaResetSeconds_T22_PastResetFallsBackToDefault(t *testing.T) {
	// 当 window_resets_at 已过去时返回 fallback (60s) 而非 1s：
	// 1 秒会导致客户端立即重试仍触发限额的退避循环；
	// 60s 让客户端按常规节奏退避，cache/DB 自愈期间不会反复打抖。
	err := service.ErrUserPlatformDailyQuotaExhausted.WithMetadata(map[string]string{
		"window_resets_at": time.Now().Add(-5 * time.Second).UTC().Format(time.RFC3339),
	})
	if got := extractQuotaResetSeconds(err); got != 60 {
		t.Errorf("T22: got %d, want 60 (fallback on past reset)", got)
	}
}

func TestBillingErrorDetails_T10_QuotaExhaustedReturns429WithRetryAfter(t *testing.T) {
	// quota 超限映射 429 + Retry-After（RFC 6585 / 与 RPM 一致），
	// 让 SDK（OpenAI 兼容客户端等）能按 Retry-After 自动退避。
	// 旧实现用 403 导致客户端不退避直接报错。
	// 三个窗口共用同一映射分支，循环覆盖避免漏测某个窗口的 status/code。
	cases := []struct {
		name string
		err  error
	}{
		{"daily", service.ErrUserPlatformDailyQuotaExhausted.WithMetadata(map[string]string{
			"window_resets_at": time.Now().Add(60 * time.Minute).UTC().Format(time.RFC3339),
		})},
		{"weekly", service.ErrUserPlatformWeeklyQuotaExhausted.WithMetadata(map[string]string{
			"window_resets_at": time.Now().Add(60 * time.Minute).UTC().Format(time.RFC3339),
		})},
		{"monthly", service.ErrUserPlatformMonthlyQuotaExhausted.WithMetadata(map[string]string{
			"window_resets_at": time.Now().Add(60 * time.Minute).UTC().Format(time.RFC3339),
		})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code, _, retryAfter := billingErrorDetails(tc.err)
			if status != http.StatusTooManyRequests {
				t.Errorf("status = %d, want 429", status)
			}
			if code != "rate_limit_exceeded" {
				t.Errorf("code = %q, want rate_limit_exceeded", code)
			}
			if retryAfter < 3599 || retryAfter > 3601 {
				t.Errorf("retryAfter = %d, want ~3600", retryAfter)
			}
		})
	}
}

func TestAnthropicErrorTypePreservesOpsClassification(t *testing.T) {
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	for _, tc := range []struct {
		status                  int
		original, wire, message string
	}{
		{502, "upstream_error", "api_error", "Upstream request failed"},
		{401, "upstream_error", "authentication_error", "Upstream request failed"},
		{503, "overloaded_error", "api_error", "Upstream service overloaded"},
		{404, "model_not_found", "not_found_error", "Model is not available"},
		{503, "api_error", "api_error", "No available accounts"},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%s/stream=%t", tc.status, tc.original, stream), func(t *testing.T) {
				setupOpsErrorLogTestQueue(t, 2)
				var entries []*service.OpsInsertErrorLogInput
				for _, path := range []string{"/v1/chat/completions", "/v1/messages"} {
					r := gin.New()
					r.Use(OpsErrorLoggerMiddleware(ops))
					r.POST(path, func(c *gin.Context) {
						if stream {
							c.Header("Content-Type", "text/event-stream")
							_, err := c.Writer.WriteString(": ping\n\n")
							require.NoError(t, err)
							c.Writer.Flush()
						}
						if path == "/v1/messages" {
							(&OpenAIGatewayHandler{}).anthropicStreamingAwareError(c, tc.status, tc.original, tc.message, stream)
						} else {
							(&OpenAIGatewayHandler{}).handleStreamingAwareError(c, tc.status, tc.original, tc.message, stream)
						}
					})
					rec := httptest.NewRecorder()
					r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
					typ := tc.original
					if path == "/v1/messages" {
						typ = tc.wire
					}
					require.Contains(t, rec.Body.String(), `"type":"`+typ+`"`)
					if stream {
						require.Equal(t, 200, rec.Code)
						require.Contains(t, rec.Body.String(), "event: error")
					} else {
						require.Equal(t, tc.status, rec.Code)
					}
					require.Len(t, opsErrorLogQueue, 1)
					entries = append(entries, (<-opsErrorLogQueue).entry)
				}
				old, current := entries[0], entries[1]
				require.Equal(t, tc.original, current.ErrorType)
				require.Equal(t, old.ErrorPhase, current.ErrorPhase)
				require.Equal(t, old.ErrorOwner, current.ErrorOwner)
				require.Equal(t, old.ErrorSource, current.ErrorSource)
				require.Equal(t, old.Severity, current.Severity)
				require.Equal(t, old.StatusCode, current.StatusCode)
				require.Equal(t, old.IsBusinessLimited, current.IsBusinessLimited)
			})
		}
	}
}
