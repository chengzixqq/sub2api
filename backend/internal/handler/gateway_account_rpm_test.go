package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type accountRPMHandlerCache struct {
	service.RPMCache
	mu  sync.Mutex
	ids []int64
	err error
}

func (c *accountRPMHandlerCache) IncrementRPM(_ context.Context, id int64) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ids = append(c.ids, id)
	return len(c.ids), c.err
}

func (c *accountRPMHandlerCache) calls() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int64(nil), c.ids...)
}

func (c *accountRPMHandlerCache) GetRPM(context.Context, int64) (int, error) {
	return 0, nil
}

func (c *accountRPMHandlerCache) GetRPMBatch(_ context.Context, ids []int64) (map[int64]int, error) {
	counts := make(map[int64]int, len(ids))
	for _, id := range ids {
		counts[id] = 0
	}
	return counts, nil
}

func TestAccountRPM_ProviderInjectsCounterAndFailuresStayBestEffort(t *testing.T) {
	cache := &accountRPMHandlerCache{err: errors.New("redis unavailable")}
	h := ProvideOpenAIGatewayHandler(&service.OpenAIGatewayService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, cache)
	account := &service.Account{ID: 42, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey}
	h.recordAccountRPM(context.Background(), account, &service.OpenAIForwardResult{}, nil)
	require.Equal(t, []int64{42}, cache.calls())
}

func TestAccountRPM_OnlySuccessfulForwardOrCompletedWebSocketTurnCounts(t *testing.T) {
	cache := &accountRPMHandlerCache{}
	svc := &service.OpenAIGatewayService{}
	svc.SetRPMCache(cache)
	h := &OpenAIGatewayHandler{gatewayService: svc}
	account := &service.Account{ID: 42}
	for _, terminal := range []string{"response.completed", "response.done", "response.failed", "response.incomplete", "error", ""} {
		h.recordAccountRPM(context.Background(), account, &service.OpenAIForwardResult{OpenAIWSMode: true, UpstreamTerminalEvent: terminal}, nil)
	}
	h.recordAccountRPM(context.Background(), account, &service.OpenAIForwardResult{}, errors.New("retry attempt failed"))
	h.recordAccountRPM(context.Background(), account, nil, nil)
	require.Equal(t, []int64{42, 42}, cache.calls())
}

func TestAccountRPM_WebSocketCountsEachSuccessfulTurn(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "two successful turns", true: "second turn failed"}[failed], func(t *testing.T) {
			cache := &accountRPMHandlerCache{}
			runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{
				firstPayload:     `{"type":"response.create","model":"gpt-5.4","stream":false,"input":"one"}`,
				secondPayload:    `{"type":"response.create","model":"gpt-5.4","stream":false,"input":"two"}`,
				secondTurnFailed: failed,
				rpmCache:         cache,
			})
			want := []int64{9901, 9901}
			if failed {
				want = want[:1]
			}
			require.Equal(t, want, cache.calls())
		})
	}
}

type accountRPMHTTPUpstream struct {
	service.HTTPUpstream
	status int
	body   string
	calls  int
}

func (u *accountRPMHTTPUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	return &http.Response{StatusCode: u.status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(u.body))}, nil
}

func TestAccountRPM_EmbeddingsAndAlphaSearchSuccessfulRequestsOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"embeddings", "alpha/search"} {
		for _, success := range []bool{true, false} {
			t.Run(endpoint+map[bool]string{true: " success", false: " rejected"}[success], func(t *testing.T) {
				groupID := int64(500)
				account := service.Account{ID: 501, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"api_key": "fixture", "base_url": "https://api.example.test"}}
				upstream := &accountRPMHTTPUpstream{status: http.StatusOK, body: `{"object":"list","model":"text-embedding-3-small","data":[{"object":"embedding","embedding":[0.1],"index":0}],"usage":{"prompt_tokens":1,"total_tokens":1}}`}
				if !success {
					upstream.status = http.StatusBadRequest
					upstream.body = `{"error":{"message":"invalid input","type":"invalid_request_error"}}`
				}
				cfg := &config.Config{RunMode: config.RunModeSimple}
				cfg.Security.URLAllowlist.Enabled = false
				billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
				t.Cleanup(billing.Stop)
				svc := service.NewOpenAIGatewayService(&openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{account}}, nil, nil, nil, nil, nil, nil, cfg, nil, nil, service.NewBillingService(cfg, nil), nil, billing, upstream, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil)
				cache := &accountRPMHandlerCache{}
				svc.SetRPMCache(cache)
				h := NewOpenAIGatewayHandler(svc, service.NewConcurrencyService(nil), billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				body := `{"model":"text-embedding-3-small","input":"hello"}`
				if endpoint == "alpha/search" {
					body = `{"model":"gpt-5.4","query":"hello"}`
				}
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+endpoint, strings.NewReader(body))
				c.Request.Header.Set("Content-Type", "application/json")
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 502, GroupID: &groupID, User: &service.User{ID: 503, Status: service.StatusActive}, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive}})
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 503})
				if endpoint == "embeddings" {
					h.Embeddings(c)
				} else {
					h.AlphaSearch(c)
				}
				require.Equal(t, upstream.status, rec.Code, rec.Body.String())
				require.Equal(t, 1, upstream.calls)
				if success {
					require.Equal(t, []int64{501}, cache.calls())
				} else {
					require.Empty(t, cache.calls())
				}
			})
		}
	}
}
