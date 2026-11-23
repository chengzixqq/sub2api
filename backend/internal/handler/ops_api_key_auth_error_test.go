//go:build unit

package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type opsAuthErrorRepo struct {
	service.APIKeyRepository
	key       service.APIKey
	lookupErr error
}

func (r *opsAuthErrorRepo) GetByKeyForAuth(context.Context, string) (*service.APIKey, error) {
	return &r.key, r.lookupErr
}

func TestOpsInvalidAPIKeyClassificationAndFilteringAcrossProtocols(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, missing := range []bool{false, true} {
		for _, ignore := range []bool{false, true} {
			t.Run(fmt.Sprintf("missing=%t/ignore=%t", missing, ignore), func(t *testing.T) {
				setupOpsErrorLogTestQueue(t, 2)
				settings := &opsAdvancedSettingsRepoStub{advanced: fmt.Sprintf(`{"ignore_invalid_api_key_errors":%t}`, ignore)}
				ops := service.NewOpsService(nil, settings, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				// This legacy option is normalized to true even when stored as false.
				require.True(t, ops.OpsAdvancedSettingsSnapshot().IgnoreInvalidApiKeyErrors)
				cfg := &config.Config{RunMode: config.RunModeStandard}
				keys := service.NewAPIKeyService(&opsAuthErrorRepo{lookupErr: service.ErrAPIKeyNotFound}, nil, nil, nil, nil, nil, cfg)
				var classifications []service.OpsInsertErrorLogInput
				for _, path := range []string{"/v1/chat/completions", "/v1/messages"} {
					r := gin.New()
					w := httptest.NewRecorder()
					r.Use(OpsErrorLoggerMiddleware(ops))
					r.Use(func(c *gin.Context) {
						c.Next()
						parsed := parseOpsErrorResponse(w.Body.Bytes())
						authCode := c.GetString(string(middleware.ContextKeyOpsAPIKeyAuthErrorCode))
						wantCode, wantMessage := "INVALID_API_KEY", "invalid x-api-key"
						if missing {
							wantCode, wantMessage = "API_KEY_REQUIRED", "x-api-key header is required"
						}
						if path == "/v1/messages" {
							require.Equal(t, "authentication_error", parsed.ErrorType)
							require.Equal(t, wantMessage, parsed.Message)
							require.Equal(t, wantCode, authCode)
							// Same canonical inputs used by OpsErrorLoggerMiddleware.
							parsed.ErrorType, parsed.Code = "api_error", authCode
						} else {
							require.Equal(t, wantCode, parsed.Code)
						}
						require.True(t, shouldSkipOpsErrorLog(ops, parsed.Message, w.Body.String(), path, authCode))
						errType := normalizeOpsErrorType(parsed.ErrorType, parsed.Code)
						phase, limited, owner, source := classifyOpsErrorLog(c, errType, parsed.Message, parsed.Code, w.Code)
						classifications = append(classifications, service.OpsInsertErrorLogInput{
							ErrorType: errType, ErrorPhase: phase, IsBusinessLimited: limited,
							ErrorOwner: owner, ErrorSource: source, Severity: classifyOpsSeverity(errType, w.Code), StatusCode: w.Code,
						})
						reason, rejected := middleware.GetIngressRejectReason(c)
						require.True(t, rejected)
						if missing {
							require.Equal(t, middleware.IngressRejectAPIKeyRequired, reason)
						} else {
							require.Equal(t, middleware.IngressRejectInvalidAPIKey, reason)
						}
					})
					r.Use(gin.HandlerFunc(middleware.NewAPIKeyAuthMiddleware(keys, nil, cfg)))
					r.POST(path, func(c *gin.Context) { t.Fatal("rejected key reached handler") })
					req := httptest.NewRequest(http.MethodPost, path, nil)
					if !missing {
						req.Header.Set("x-api-key", "wrong")
					}
					r.ServeHTTP(w, req)
					require.Equal(t, http.StatusUnauthorized, w.Code)
					// Ingress rejections never enter detailed Ops logs, with either setting.
					require.Empty(t, opsErrorLogQueue)
				}
				require.Equal(t, "auth", classifications[1].ErrorPhase)
				require.True(t, classifications[1].IsBusinessLimited)
				require.Equal(t, classifications[0], classifications[1])
			})
		}
	}
}

func TestOpsAPIKeyAuthClassificationAcrossProtocols(t *testing.T) {
	for _, tc := range []struct {
		status   string
		wireType string
		phase    string
	}{
		{service.StatusAPIKeyExpired, "permission_error", "auth"},
		{service.StatusAPIKeyQuotaExhausted, "rate_limit_error", "request"},
		{service.StatusActive, "permission_error", "request"}, // zero balance
	} {
		t.Run(tc.status, func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 2)
			cfg := &config.Config{RunMode: config.RunModeStandard}
			repo := &opsAuthErrorRepo{key: service.APIKey{
				ID: 1, UserID: 2, Status: tc.status, User: &service.User{ID: 2, Status: service.StatusActive},
			}}
			svc := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
			ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			r := gin.New()
			r.Use(OpsErrorLoggerMiddleware(ops), gin.HandlerFunc(middleware.NewAPIKeyAuthMiddleware(svc, nil, cfg)))
			var entries []*service.OpsInsertErrorLogInput
			for _, path := range []string{"/v1/chat/completions", "/v1/messages"} {
				r.POST(path, func(c *gin.Context) { t.Fatal("rejected key reached handler") })
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, path, nil)
				req.Header.Set("x-api-key", "test-key")
				r.ServeHTTP(w, req)
				if path == "/v1/messages" {
					require.Equal(t, tc.wireType, parseOpsErrorResponse(w.Body.Bytes()).ErrorType)
				}
				require.Len(t, opsErrorLogQueue, 1)
				entries = append(entries, (<-opsErrorLogQueue).entry)
			}
			legacy, anthropic := entries[0], entries[1]
			require.Equal(t, tc.phase, anthropic.ErrorPhase)
			require.True(t, anthropic.IsBusinessLimited)
			require.Equal(t, legacy.ErrorType, anthropic.ErrorType)
			require.Equal(t, legacy.ErrorPhase, anthropic.ErrorPhase)
			require.Equal(t, legacy.ErrorSource, anthropic.ErrorSource)
			require.Equal(t, legacy.ErrorOwner, anthropic.ErrorOwner)
			require.Equal(t, legacy.Severity, anthropic.Severity)
			require.Equal(t, legacy.IsBusinessLimited, anthropic.IsBusinessLimited)
			require.Equal(t, legacy.StatusCode, anthropic.StatusCode)
			require.Equal(t, legacy.APIKeyID, anthropic.APIKeyID)
			logMonitorRow(t, anthropic)
			require.Equal(t, legacy.ErrorMessage, anthropic.ErrorMessage)
			require.Equal(t, legacy.ErrorBody, anthropic.ErrorBody)
		})
	}
}

func TestOpsPrehandlerErrorTypeAcrossEnvelopes(t *testing.T) {
	for _, tc := range []struct {
		status                  int
		legacyType, wantOpsType string
	}{
		{413, "invalid_request_error", "invalid_request_error"},
		{503, "api_error", "api_error"},    // body memory budget
		{500, "server_error", "api_error"}, // composite resolver
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		middleware.AnthropicErrorWriter(c, tc.status, "test failure")
		wire := parseOpsErrorResponse(w.Body.Bytes())
		require.Equal(t, tc.wantOpsType, normalizeOpsErrorType(tc.legacyType, ""))
		require.Equal(t, tc.wantOpsType, normalizeOpsErrorType(wire.ErrorType, wire.Code))
	}
}

func TestOpsAPIKeyBillingClassificationAcrossProtocols(t *testing.T) {
	for _, billingErr := range []error{
		service.ErrAPIKeyRateLimit5hExceeded,
		service.ErrAPIKeyRateLimit1dExceeded,
		service.ErrAPIKeyRateLimit7dExceeded,
		service.ErrAPIKeyQuotaExhausted,
	} {
		t.Run(billingErr.Error(), func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 2)
			ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			r := gin.New()
			r.Use(OpsErrorLoggerMiddleware(ops))
			var entries []*service.OpsInsertErrorLogInput
			for _, path := range []string{"/v1/chat/completions", "/v1/messages"} {
				r.POST(path, func(c *gin.Context) {
					status, code, message, _ := billingErrorDetailsForRequest(c, billingErr)
					(&GatewayHandler{}).errorResponse(c, status, code, message)
				})
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
				require.Len(t, opsErrorLogQueue, 1)
				entries = append(entries, (<-opsErrorLogQueue).entry)
			}
			legacy, anthropic := entries[0], entries[1]
			logMonitorRow(t, anthropic)
			require.Equal(t, legacy.ErrorMessage, anthropic.ErrorMessage)
			require.Equal(t, legacy.ErrorBody, anthropic.ErrorBody)
			require.Equal(t, "request", anthropic.ErrorPhase)
			require.True(t, anthropic.IsBusinessLimited)
			require.Equal(t, legacy.ErrorPhase, anthropic.ErrorPhase)
			require.Equal(t, legacy.ErrorSource, anthropic.ErrorSource)
			require.Equal(t, legacy.ErrorOwner, anthropic.ErrorOwner)
			require.Equal(t, legacy.Severity, anthropic.Severity)
			require.Equal(t, legacy.IsBusinessLimited, anthropic.IsBusinessLimited)
		})
	}
}
