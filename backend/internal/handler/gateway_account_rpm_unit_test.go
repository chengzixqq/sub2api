//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type accountRPMSuccessUpstream struct{ service.HTTPUpstream }

func (u *accountRPMSuccessUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

func (*accountRPMSuccessUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"msg_rpm","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)),
	}, nil
}

func TestAccountRPM_AnthropicAPIKeyAndExistingOAuthCountOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, accountType := range []string{service.AccountTypeAPIKey, service.AccountTypeOAuth} {
		t.Run(accountType, func(t *testing.T) {
			groupID := int64(5101)
			group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive}
			account := &service.Account{
				ID: 6101, Platform: service.PlatformAnthropic, Type: accountType,
				Status: service.StatusActive, Schedulable: true, Concurrency: 1,
				Credentials:   map[string]any{"api_key": "fixture", "access_token": "fixture"},
				Extra:         map[string]any{"base_rpm": 60},
				AccountGroups: []service.AccountGroup{{AccountID: 6101, GroupID: groupID}},
			}
			rpm := &accountRPMHandlerCache{}
			snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: []*service.Account{account}}, nil, nil, nil, nil)
			cfg := &config.Config{RunMode: config.RunModeSimple}
			h, cleanup := newTestGatewayHandler(t, group, []*service.Account{account})
			t.Cleanup(cleanup)
			h.gatewayService = service.NewGatewayService(
				nil, &fakeGroupRepo{group: group}, nil, nil, nil, nil, nil, nil, cfg,
				snapshot, nil, nil, nil, nil, nil, &accountRPMSuccessUpstream{}, nil, nil, nil, rpm,
				nil, nil, nil, nil, nil, nil, nil, nil, nil,
			)
			h.apiKeyService = service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`))
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, group))
			c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 7101, UserID: 8101, GroupID: &groupID, Group: group, User: &service.User{ID: 8101, Balance: 100}})
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 8101})
			h.Messages(c)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, []int64{6101}, rpm.calls())
		})
	}
}
