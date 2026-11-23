//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func withOpsFilterSettings(t *testing.T, f func(*testing.T, *service.OpsService)) {
	t.Helper()
	for _, cfg := range []struct{ name, settings string }{
		{"default", "{}"}, {"log_cancel", `{"ignore_context_canceled":false}`}, {"ignore_limits", `{"ignore_no_available_accounts":true,"ignore_insufficient_balance_errors":true}`},
	} {
		t.Run(cfg.name, func(t *testing.T) {
			f(t, service.NewOpsService(nil, &opsAdvancedSettingsRepoStub{advanced: cfg.settings}, nil, nil, nil, nil, nil, nil, nil, nil, nil))
		})
	}
}

// Use the same detail-message precedence as channel_monitor_v2_repo.go.
func logMonitorRow(t *testing.T, e *service.OpsInsertErrorLogInput) (aggregate, detail string) {
	t.Helper()
	if e == nil {
		t.Log("MONITOR null")
		return "skip", "skip"
	}
	msg := e.ErrorMessage
	if e.UpstreamErrorMessage != nil && *e.UpstreamErrorMessage != "" {
		msg = *e.UpstreamErrorMessage
	}
	if msg == "" && e.UpstreamErrorDetail != nil {
		msg = *e.UpstreamErrorDetail
	}
	if msg == "" {
		msg = e.ErrorBody
	}
	if msg == "" {
		msg = e.ErrorType
	}
	status := 0
	if e.UpstreamStatusCode != nil {
		status = *e.UpstreamStatusCode
	}
	input := service.ChannelMonitorV2ErrorInput{
		ErrorType: e.ErrorType, ErrorOwner: e.ErrorOwner, ErrorSource: e.ErrorSource, StatusCode: e.StatusCode, UpstreamStatusCode: status, Message: msg,
	}
	category := service.ClassifyChannelMonitorV2Error(input)
	parts := []string{e.ErrorMessage, e.ErrorBody}
	if e.UpstreamErrorMessage != nil {
		parts = append(parts, *e.UpstreamErrorMessage)
	}
	if e.UpstreamErrorDetail != nil {
		parts = append(parts, *e.UpstreamErrorDetail)
	}
	input.Message = strings.Join(parts, " ")
	aggregate = service.ClassifyChannelMonitorV2Error(input)
	row, err := json.Marshal(map[string]any{"type": e.ErrorType, "phase": e.ErrorPhase, "owner": e.ErrorOwner, "source": e.ErrorSource, "severity": e.Severity, "limited": e.IsBusinessLimited, "status": e.StatusCode, "upStatus": status, "msg": e.ErrorMessage, "upMsg": e.UpstreamErrorMessage, "detail": e.UpstreamErrorDetail, "body": e.ErrorBody, "category": category})
	require.NoError(t, err)
	t.Logf("MONITOR %s", row)
	return aggregate, category
}

type cancellingAnthropicUpstream struct {
	service.HTTPUpstream
	cancel context.CancelFunc
	calls  int
}

func (u *cancellingAnthropicUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	u.calls++
	u.cancel()
	return nil, context.Canceled
}
func TestAnthropicOpsPersistence(t *testing.T) {
	for _, cfg := range []struct{ name, settings string }{
		{"default", "{}"}, {"log_cancel", `{"ignore_context_canceled":false}`}, {"ignore_limits", `{"ignore_no_available_accounts":true,"ignore_insufficient_balance_errors":true}`},
	} {
		for _, tc := range []struct {
			name, original, wireType string
			status                   int
		}{
			{"concurrency_cancel", "api_error", "invalid_request_error", 499}, {"antigravity_cancel", "client_disconnected", "invalid_request_error", 499}, {"before_upstream", "", "", 499},
			{"body_timeout", "invalid_request_error", "invalid_request_error", 408}, {"body_limit", "invalid_request_error", "request_too_large", 413}, {"body_bad", "invalid_request_error", "invalid_request_error", 400},
			{"payment", "billing_error", "billing_error", 402}, {"balance", "insufficient_balance", "permission_error", 403}, {"queue", "rate_limit_error", "rate_limit_error", 429},
			{"missing", "not_found_error", "not_found_error", 404}, {"empty_pool", "overloaded_error", "api_error", 503},
		} {
			t.Run(cfg.name+"/"+tc.name, func(t *testing.T) {
				setupOpsErrorLogTestQueue(t, 2)
				ops := service.NewOpsService(nil, &opsAdvancedSettingsRepoStub{advanced: cfg.settings}, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				r := gin.New()
				r.Use(OpsErrorLoggerMiddleware(ops))
				h := &GatewayHandler{}
				r.POST("/v1/messages", func(c *gin.Context) {
					switch tc.name {
					case "before_upstream":
						ctx, cancel := context.WithCancel(c.Request.Context())
						cancel()
						c.Request = c.Request.WithContext(ctx)
						require.True(t, failoverClientGone(c))
					case "concurrency_cancel":
						h.handleConcurrencyError(c, context.Canceled, "user", false)
					case "antigravity_cancel":
						ctx, cancel := context.WithCancel(c.Request.Context())
						defer cancel()
						c.Request = c.Request.WithContext(ctx)
						up := &cancellingAnthropicUpstream{cancel: cancel}
						gateway := service.NewAntigravityGatewayService(nil, nil, nil, service.NewAntigravityTokenProvider(nil, nil, nil), nil, up, service.NewSettingService(&ingressRejectSettingRepo{}, &config.Config{}), nil)
						account := &service.Account{ID: 23, Platform: service.PlatformAntigravity, Type: service.AccountTypeOAuth, Concurrency: 1, Credentials: map[string]any{"project_id": "test-project", "access_token": "test-token", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339), "model_mapping": map[string]any{"claude-sonnet-4-5": "claude-sonnet-4-5"}}}
						_, err := gateway.Forward(ctx, c, account, []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}],"max_tokens":12}`), false)
						require.Error(t, err)
						require.Equal(t, 1, up.calls)
					case "body_limit":
						middleware.AbortWithRequestBodyReadFailure(c, "test", &http.MaxBytesError{Limit: 32})
					case "body_timeout":
						middleware.AbortWithRequestBodyReadFailure(c, "test", os.ErrDeadlineExceeded)
					case "body_bad":
						middleware.AbortWithRequestBodyReadFailure(c, "test", fmt.Errorf("failed read"))
					case "balance":
						h.errorResponse(c, tc.status, tc.original, "Insufficient account balance")
					case "empty_pool":
						h.errorResponse(c, tc.status, tc.original, "No available accounts")
					case "queue":
						h.handleConcurrencyError(c, &WaitQueueFullError{SlotType: "user"}, "user", false)
					default:
						h.errorResponse(c, tc.status, tc.original, "Unavailable")
					}
				})
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", nil))
				require.Equal(t, tc.status, w.Code)
				var row *service.OpsInsertErrorLogInput
				if len(opsErrorLogQueue) > 0 {
					row = (<-opsErrorLogQueue).entry
				}
				aggregate, detail := logMonitorRow(t, row)
				skipped := (tc.name == "concurrency_cancel" || tc.name == "before_upstream") && cfg.name != "log_cancel" ||
					(tc.name == "balance" || tc.name == "empty_pool") && cfg.name == "ignore_limits"
				if skipped {
					require.Nil(t, row)
				} else {
					require.NotNil(t, row)
					want := map[string]string{"concurrency_cancel": "client_cancelled", "antigravity_cancel": "client_cancelled", "before_upstream": "client_cancelled", "body_timeout": "invalid_request", "body_limit": "invalid_request", "body_bad": "invalid_request", "payment": "other", "balance": "upstream_forbidden", "queue": "rate_or_capacity", "missing": "not_found", "empty_pool": "account_pool_unavailable"}[tc.name]
					require.Equal(t, want, aggregate)
					require.Equal(t, want, detail)
				}
				if tc.name != "before_upstream" {
					require.Equal(t, tc.wireType, parseOpsErrorResponse(w.Body.Bytes()).ErrorType)
					if row != nil {
						require.Equal(t, tc.original, parseOpsErrorResponse([]byte(row.ErrorBody)).ErrorType)
					}
				}
			})
		}
	}
}
