//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type geminiMessagesAntigravityUpstream struct {
	requestURL   string
	requestBody  []byte
	statusCode   int
	responseBody string
	calls        int
}

func (u *geminiMessagesAntigravityUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.requestURL = req.URL.String()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.requestBody = body
	u.calls++
	if u.statusCode != 0 {
		return &http.Response{
			StatusCode: u.statusCode,
			Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"req-gemini-messages"}},
			Body:       io.NopCloser(strings.NewReader(u.responseBody)),
		}, nil
	}

	stream := `data: {"response":{"responseId":"resp-1","candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}}` + "\n\n"
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(stream)),
	}, nil
}

func (u *geminiMessagesAntigravityUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func TestGatewayHandlerMessages_GeminiGroupMixedAntigravityUsesClaudeForward(t *testing.T) {
	upstream := &geminiMessagesAntigravityUpstream{}
	h, c, recorder, account := newGeminiMessagesAntigravityFixture(t, upstream, nil, false)
	accountID := account.ID
	h.Messages(c)

	require.Equal(t, accountID, mustOpsAccountID(t, c))
	require.Contains(t, upstream.requestURL, "/v1internal:streamGenerateContent")
	var wrapped map[string]any
	require.NoError(t, json.Unmarshal(upstream.requestBody, &wrapped))
	require.Equal(t, "gemini-2.5-pro", wrapped["model"])
	request, ok := wrapped["request"].(map[string]any)
	require.True(t, ok)
	contents, ok := request["contents"].([]any)
	require.True(t, ok)
	require.NotContains(t, request, "messages")
	require.Len(t, contents, 1)
	content, ok := contents[0].(map[string]any)
	require.True(t, ok)
	parts, ok := content["parts"].([]any)
	require.True(t, ok)
	require.Equal(t, "hello", parts[0].(map[string]any)["text"])

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var response map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, "message", response["type"])
	require.Equal(t, "claude-sonnet-4-5", response["model"])
	require.Equal(t, "assistant", response["role"])
	require.NotContains(t, response, "candidates")
	responseContent, ok := response["content"].([]any)
	require.True(t, ok)
	require.Len(t, responseContent, 1)
	require.Equal(t, map[string]any{"type": "text", "text": "ok"}, responseContent[0])
}

func newGeminiMessagesAntigravityFixture(t *testing.T, upstream *geminiMessagesAntigravityUpstream, cache *geminiMessagesAntigravityCache, stream bool) (*GatewayHandler, *gin.Context, *httptest.ResponseRecorder, *service.Account) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	groupID := int64(5001)
	accountID := int64(6001)
	group := &service.Group{
		ID:       groupID,
		Hydrated: true,
		Platform: service.PlatformGemini,
		Status:   service.StatusActive,
	}
	account := &service.Account{
		ID:          accountID,
		Name:        "mixed-antigravity",
		Platform:    service.PlatformAntigravity,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    1,
		Credentials: map[string]any{
			"access_token": "test-token",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"project_id":   "test-project",
			"model_mapping": map[string]any{
				"claude-sonnet-4-5": "gemini-2.5-pro",
			},
		},
		Extra: map[string]any{
			"mixed_scheduling": true,
		},
		AccountGroups: []service.AccountGroup{{AccountID: accountID, GroupID: groupID}},
	}

	h, cleanup := newTestGatewayHandler(t, group, []*service.Account{account})
	t.Cleanup(cleanup)
	var gatewayCache service.GatewayCache
	if cache != nil {
		gatewayCache = cache
		// Two available accounts keep 503 on the normal switch path, not the
		// single-account retry-in-place path.
		second := *account
		second.ID++
		second.AccountGroups = []service.AccountGroup{{AccountID: second.ID, GroupID: groupID}}
		snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: []*service.Account{account, &second}}, nil, nil, nil, nil)
		h.gatewayService = service.NewGatewayService(
			nil, &fakeGroupRepo{group: group}, nil, nil, nil, nil, nil,
			gatewayCache, nil, snapshot,
			nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
			nil, nil, nil, nil, nil,
		)
	}

	tokenProvider := service.NewAntigravityTokenProvider(nil, nil, nil)
	settingService := service.NewSettingService(&contentModerationHandlerSettingRepo{}, nil)
	h.antigravityGatewayService = service.NewAntigravityGatewayService(&geminiMessagesAntigravityAccountRepo{}, gatewayCache, nil, tokenProvider, nil, upstream, settingService, nil)

	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`)
	if stream {
		body = []byte(`{"model":"claude-sonnet-4-5","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.Group, group))
	c.Request = req
	apiKey := &service.APIKey{
		ID:      7001,
		UserID:  8001,
		GroupID: &groupID,
		Status:  service.StatusActive,
		User:    &service.User{ID: 8001, Concurrency: 10, Balance: 100},
		Group:   group,
	}
	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})

	return h, c, recorder, account
}

type geminiMessagesAntigravityAccountRepo struct {
	service.AccountRepository
}

func (*geminiMessagesAntigravityAccountRepo) SetModelRateLimit(context.Context, int64, string, time.Time, ...string) error {
	return nil
}

type geminiMessagesAntigravitySession struct {
	groupID int64
	hash    string
}

type geminiMessagesAntigravityCache struct {
	service.GatewayCache
	mu             sync.Mutex
	session        geminiMessagesAntigravitySession
	boundAccountID int64
	boundReads     int
	deletes        []geminiMessagesAntigravitySession
}

func (s *geminiMessagesAntigravityCache) GetSessionAccountID(_ context.Context, groupID int64, hash string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := geminiMessagesAntigravitySession{groupID: groupID, hash: hash}
	if s.session.hash == "" {
		s.session = key
	}
	if s.session != key || s.boundAccountID == 0 {
		return 0, service.ErrStickySessionNotFound
	}
	s.boundReads++
	return s.boundAccountID, nil
}

func (s *geminiMessagesAntigravityCache) SetSessionAccountID(_ context.Context, groupID int64, hash string, accountID int64, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session = geminiMessagesAntigravitySession{groupID: groupID, hash: hash}
	s.boundAccountID = accountID
	return nil
}

func (*geminiMessagesAntigravityCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}

func (s *geminiMessagesAntigravityCache) DeleteSessionAccountID(_ context.Context, groupID int64, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := geminiMessagesAntigravitySession{groupID: groupID, hash: hash}
	s.deletes = append(s.deletes, key)
	if s.session == key {
		s.boundAccountID = 0
	}
	return nil
}

func TestGatewayHandlerMessages_GeminiGroupMixedAntigravityPreservesErrorAndStickySemantics(t *testing.T) {
	const rateLimitBody = `{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","metadata":{"model":"gemini-2.5-pro"},"reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"15s"}]}}`
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantDelete bool
	}{
		{"prompt too long", http.StatusBadRequest, `{"error":{"message":"prompt is too long","status":"INVALID_ARGUMENT"}}`, http.StatusBadRequest, false},
		{"ordinary bad request", http.StatusBadRequest, `{"error":{"message":"invalid request field","status":"INVALID_ARGUMENT"}}`, http.StatusBadRequest, false},
		{"429 model rate limit", http.StatusTooManyRequests, rateLimitBody, http.StatusBadGateway, true},
		{"503 model rate limit", http.StatusServiceUnavailable, rateLimitBody, http.StatusBadGateway, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &geminiMessagesAntigravityUpstream{statusCode: tc.status, responseBody: tc.body}
			cache := &geminiMessagesAntigravityCache{boundAccountID: 6001}
			h, c, recorder, account := newGeminiMessagesAntigravityFixture(t, upstream, cache, false)
			h.maxAccountSwitchesGemini = 0
			h.Messages(c)
			require.Equal(t, tc.wantStatus, recorder.Code, recorder.Body.String())
			require.Equal(t, 1, upstream.calls)
			require.Positive(t, cache.boundReads, "the handler must observe an existing binding")
			require.Equal(t, int64(5001), cache.session.groupID)
			require.True(t, strings.HasPrefix(cache.session.hash, "gemini:"), cache.session.hash)
			if tc.wantDelete {
				require.Equal(t, []geminiMessagesAntigravitySession{cache.session}, cache.deletes)
				require.Zero(t, cache.boundAccountID)
			} else {
				require.Empty(t, cache.deletes)
				require.Equal(t, account.ID, cache.boundAccountID)
				var response struct {
					Type  string `json:"type"`
					Error struct {
						Type string `json:"type"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
				require.Equal(t, "error", response.Type)
				require.Equal(t, "invalid_request_error", response.Error.Type)
			}
		})
	}
}

func TestGatewayHandlerMessages_GeminiGroupMixedAntigravityStreamsAnthropicAndKeepsBinding(t *testing.T) {
	upstream := &geminiMessagesAntigravityUpstream{}
	cache := &geminiMessagesAntigravityCache{boundAccountID: 6001}
	h, c, recorder, account := newGeminiMessagesAntigravityFixture(t, upstream, cache, true)
	h.Messages(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
	require.Contains(t, recorder.Body.String(), "event: message_start")
	require.Contains(t, recorder.Body.String(), "event: content_block_delta")
	require.Contains(t, recorder.Body.String(), "event: message_stop")
	require.Contains(t, recorder.Body.String(), `"text":"ok"`)
	require.NotContains(t, recorder.Body.String(), `"candidates"`)
	require.Positive(t, cache.boundReads)
	require.Empty(t, cache.deletes)
	require.Equal(t, account.ID, cache.boundAccountID)
}

func mustOpsAccountID(t *testing.T, c *gin.Context) int64 {
	t.Helper()
	value, ok := c.Get(opsAccountIDKey)
	require.True(t, ok)
	accountID, ok := value.(int64)
	require.True(t, ok)
	return accountID
}
