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
	requestURL  string
	requestBody []byte
}

func (u *geminiMessagesAntigravityUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.requestURL = req.URL.String()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.requestBody = body

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
	defer cleanup()

	upstream := &geminiMessagesAntigravityUpstream{}
	tokenProvider := service.NewAntigravityTokenProvider(nil, nil, nil)
	h.antigravityGatewayService = service.NewAntigravityGatewayService(nil, nil, nil, tokenProvider, nil, upstream, nil, nil)

	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`)
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

	h.Messages(c)

	require.Equal(t, accountID, mustOpsAccountID(t, c))
	require.Contains(t, upstream.requestURL, "/v1internal:streamGenerateContent")
	var wrapped map[string]any
	require.NoError(t, json.Unmarshal(upstream.requestBody, &wrapped))
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
}

func mustOpsAccountID(t *testing.T, c *gin.Context) int64 {
	t.Helper()
	value, ok := c.Get(opsAccountIDKey)
	require.True(t, ok)
	accountID, ok := value.(int64)
	require.True(t, ok)
	return accountID
}
