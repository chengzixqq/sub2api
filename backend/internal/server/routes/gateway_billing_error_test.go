//go:build unit

package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type gatewayLimitedKeyRepo struct {
	keyBillingRouteAPIKeyRepo
	usage service.APIKeyRateLimitData
}

func (r *gatewayLimitedKeyRepo) GetRateLimitData(context.Context, int64) (*service.APIKeyRateLimitData, error) {
	return &r.usage, nil
}

func (*gatewayLimitedKeyRepo) UpdateLastUsed(context.Context, int64, time.Time) error {
	return nil
}

func TestRegisterGatewayRoutesAPIKeyWindowErrorsAcrossProtocols(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, platform := range []string{service.PlatformAnthropic, service.PlatformOpenAI} {
		for _, window := range []struct {
			name, message, legacy string
		}{
			{"5h", "API key 5-hour rate limit exceeded", "api key 5小时限额已用完"},
			{"1d", "API key daily rate limit exceeded", "api key 日限额已用完"},
			{"7d", "API key 7-day rate limit exceeded", "api key 7天限额已用完"},
		} {
			t.Run(platform+"/"+window.name, func(t *testing.T) {
				// Simple mode's explicit window enforcement uses the real billing
				// service with DB-authoritative usage, without Redis or a database.
				cfg := &config.Config{RunMode: config.RunModeSimple, SimpleModeKeyRateLimitEnabled: true, Gateway: config.GatewayConfig{MaxBodySize: 1 << 20, TextMaxBodySize: 1 << 20}}
				group := &service.Group{ID: 1, Platform: platform, Hydrated: true, Status: service.StatusActive, AllowMessagesDispatch: true}
				key := &service.APIKey{ID: 1, Key: "limited-key", UserID: 2, Status: service.StatusActive,
					GroupID: &group.ID, Group: group, User: &service.User{ID: 2, Status: service.StatusActive}}
				now := time.Now()
				repo := &gatewayLimitedKeyRepo{keyBillingRouteAPIKeyRepo: keyBillingRouteAPIKeyRepo{apiKey: key},
					usage: service.APIKeyRateLimitData{Usage5h: 1, Usage1d: 1, Usage7d: 1, Window5hStart: &now, Window1dStart: &now, Window7dStart: &now}}
				switch window.name {
				case "5h":
					key.RateLimit5h = 1
				case "1d":
					key.RateLimit1d = 1
				case "7d":
					key.RateLimit7d = 1
				}
				keys := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
				billing := service.NewBillingCacheService(nil, nil, nil, repo, nil, nil, cfg, nil)
				t.Cleanup(billing.Stop)
				gateway := service.NewGatewayService(nil, nil, nil, nil, nil, nil, nil, nil, cfg,
					nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				openai := service.NewOpenAIGatewayService(nil, nil, nil, nil, nil, nil, nil, cfg,
					nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				concurrency := service.NewConcurrencyService(nil)
				handlers := &handler.Handlers{
					Gateway:       handler.NewGatewayHandler(gateway, openai, nil, nil, nil, concurrency, billing, nil, keys, nil, nil, nil, nil, cfg, nil),
					OpenAIGateway: handler.NewOpenAIGatewayHandler(openai, concurrency, billing, keys, nil, nil, nil, nil, cfg),
				}
				r := gin.New()
				RegisterGatewayRoutes(r, handlers, middleware.NewAPIKeyAuthMiddleware(keys, nil, cfg), keys, nil, nil, nil, nil, cfg)
				for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens", "/messages/count_tokens", "/antigravity/v1/messages", "/antigravity/v1/messages/count_tokens", "/v1/chat/completions"} {
					t.Run(path, func(t *testing.T) {
						req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"claude-sonnet-4","max_tokens":100,"messages":[{"role":"user","content":"Hello"}]}`))
						req.Header.Set("x-api-key", key.Key)
						w := httptest.NewRecorder()
						r.ServeHTTP(w, req)
						require.Equal(t, http.StatusTooManyRequests, w.Code, w.Body.String())
						if path == "/v1/chat/completions" {
							require.JSONEq(t, `{"error":{"type":"rate_limit_exceeded","message":"`+window.legacy+`"}}`, w.Body.String())
						} else {
							require.JSONEq(t, `{"type":"error","error":{"type":"rate_limit_error","message":"`+window.message+`"}}`, w.Body.String())
							require.Equal(t, -1, strings.IndexFunc(w.Body.String(), func(r rune) bool { return unicode.Is(unicode.Han, r) }))
						}
					})
				}
			})
		}
	}
}
