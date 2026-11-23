//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type contextBetaBlockRepo struct {
	service.SettingRepository
	rules string
}

func (r contextBetaBlockRepo) GetValue(_ context.Context, key string) (string, error) {
	if key == service.SettingKeyBetaPolicySettings {
		return r.rules, nil
	}
	return "", service.ErrSettingNotFound
}

func TestMessagesContextBetaPolicyBlockResponse(t *testing.T) {
	for _, token := range []string{"context-management-2025-06-27", "compact-2026-01-12"} {
		rules, err := json.Marshal(service.BetaPolicySettings{Rules: []service.BetaPolicyRule{{Scope: service.BetaPolicyScopeAll, BetaToken: token, Action: service.BetaPolicyActionBlock}}})
		require.NoError(t, err)
		cfg := &config.Config{RunMode: config.RunModeSimple}
		settings := service.NewSettingService(contextBetaBlockRepo{rules: string(rules)}, cfg)
		for _, typ := range []string{service.AccountTypeAPIKey, service.AccountTypeOAuth} {
			groupID := int64(5101)
			group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive}
			account := &service.Account{ID: 6101, Platform: service.PlatformAnthropic, Type: typ, Status: service.StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"api_key": "test", "access_token": "test"}, AccountGroups: []service.AccountGroup{{AccountID: 6101, GroupID: groupID}}}
			snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: []*service.Account{account}}, nil, nil, nil, nil)
			h, cleanup := newTestGatewayHandler(t, group, []*service.Account{account})
			h.gatewayService = service.NewGatewayService(nil, &fakeGroupRepo{group: group}, nil, nil, nil, nil, nil, nil, cfg, snapshot, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, settings, nil, nil, nil, nil, nil, nil, nil)
			h.apiKeyService = service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"context_management":{}}`))
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, group))
			c.Request.Header.Set("Anthropic-Beta", token)
			c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 7101, UserID: 8101, GroupID: &groupID, Group: group, User: &service.User{ID: 8101, Balance: 100}})
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 8101})
			h.Messages(c)
			cleanup()
			t.Logf("BLOCK %s %s status=%d body=%q", typ, token, rec.Code, rec.Body.String())
			require.Equal(t, 400, rec.Code, rec.Body.String())
			require.Equal(t, `{"error":{"message":"beta feature `+token+` is not allowed","type":"invalid_request_error"},"type":"error"}`, rec.Body.String())
		}
	}
}
