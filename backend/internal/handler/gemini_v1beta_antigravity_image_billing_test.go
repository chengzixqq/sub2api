//go:build unit

package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// antigravityV1BetaFailingUpstream 返回 200 的 SSE，随后读到 payload 就以读错误结束。
type antigravityV1BetaFailingUpstream struct {
	payload string
	calls   int
}

func (u *antigravityV1BetaFailingUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	u.calls++
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       &antigravityV1BetaErrBody{payload: []byte(u.payload)},
	}, nil
}

func (u *antigravityV1BetaFailingUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

type antigravityV1BetaErrBody struct {
	payload []byte
	sent    bool
}

func (b *antigravityV1BetaErrBody) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		if len(b.payload) > 0 {
			return copy(p, b.payload), nil
		}
	}
	return 0, io.ErrUnexpectedEOF
}

func (b *antigravityV1BetaErrBody) Close() error { return nil }

// /antigravity/v1beta 图片模型在上游读错误时不能按模型名猜一张图做失败结算：
// 图片既没生成完也没送达，失败结算只能落真实观察到的东西。
func TestGeminiV1BetaAntigravityImageReadErrorDoesNotBillGuessedImage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name          string
		payload       string
		wantUsageRows int
	}{
		{name: "no chunk before read error", payload: "", wantUsageRows: 0},
		{
			name:          "text-only chunk before read error",
			payload:       `data: {"response":{"candidates":[{"content":{"parts":[{"text":"drawing"}]}}]}}` + "\n\n",
			wantUsageRows: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Default.RateMultiplier = 1
			groupID := int64(9500)
			accountID := int64(9501)
			group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformGemini, Status: service.StatusActive, RateMultiplier: 1, AllowImageGeneration: true}
			account := &service.Account{
				ID: accountID, Name: "antigravity-v1beta-image", Platform: service.PlatformAntigravity, Type: service.AccountTypeOAuth,
				Status: service.StatusActive, Schedulable: true, Concurrency: 1, Priority: 1,
				Credentials: map[string]any{
					"access_token": "test-token",
					"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
					"project_id":   "test-project",
				},
				AccountGroups: []service.AccountGroup{{AccountID: accountID, GroupID: groupID}},
			}
			usageRepo := &fakeFailureSinkUsageLogRepo{}
			snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: []*service.Account{account}}, nil, nil, nil, nil)
			gw := service.NewGatewayService(
				nil, &fakeGroupRepo{group: group}, usageRepo, nil,
				&fakeFailureSinkUserRepo{}, &fakeFailureSinkSubRepo{},
				nil, nil, cfg, snapshot, nil,
				service.NewBillingService(cfg, nil), nil, &service.BillingCacheService{}, nil, nil,
				&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
				nil,
			)
			billingCache := service.NewBillingCacheService(newHandlerInflightCache(100), nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billingCache.Stop)
			upstream := &antigravityV1BetaFailingUpstream{payload: tt.payload}
			h := &GatewayHandler{
				gatewayService:           gw,
				billingCacheService:      billingCache,
				cfg:                      cfg,
				maxAccountSwitches:       1,
				maxAccountSwitchesGemini: 1,
				concurrencyHelper:        NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatNone, 0),
				antigravityGatewayService: service.NewAntigravityGatewayService(
					&geminiMessagesAntigravityAccountRepo{}, nil, nil, service.NewAntigravityTokenProvider(nil, nil, nil), nil, upstream,
					service.NewSettingService(&contentModerationHandlerSettingRepo{}, cfg), nil,
				),
			}

			const model = "gemini-3.1-flash-image"
			body := `{"contents":[{"role":"user","parts":[{"text":"draw a cat"}]}],"generationConfig":{"responseModalities":["TEXT","IMAGE"]}}`
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			req := httptest.NewRequest(http.MethodPost, "/antigravity/v1beta/models/"+model+":streamGenerateContent?alt=sse", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			reqCtx := context.WithValue(req.Context(), ctxkey.Group, group)
			reqCtx = context.WithValue(reqCtx, ctxkey.ForcePlatform, service.PlatformAntigravity)
			c.Request = req.WithContext(reqCtx)
			c.Params = gin.Params{{Key: "modelAction", Value: "/" + model + ":streamGenerateContent"}}
			c.Set(string(middleware.ContextKeyForcePlatform), service.PlatformAntigravity)
			apiKey := &service.APIKey{
				ID: 9502, UserID: 9503, GroupID: &groupID, Group: group, Status: service.StatusActive,
				User: &service.User{ID: 9503, Concurrency: 10, Balance: 100},
			}
			c.Set(string(middleware.ContextKeyAPIKey), apiKey)
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})

			h.GeminiV1BetaModels(c)

			require.Equal(t, 1, upstream.calls)
			calls, log := usageRepo.snapshot()
			require.Equal(t, tt.wantUsageRows, calls)
			if log != nil {
				require.Zero(t, log.ImageCount, "失败结算不能按模型名猜出的图片数按张扣费")
				require.NotEqual(t, string(service.BillingModeImage), derefString(log.BillingMode))
			}
		})
	}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
