package admin

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAccountUsageHandlersRequireManagementScope(t *testing.T) {
	h := &AccountHandler{}
	for name, handle := range map[string]func(*gin.Context){
		"opencode read":    h.GetOpenCodeGoUsage,
		"opencode refresh": h.RefreshOpenCodeGoUsage,
		"opencode auto":    h.SetOpenCodeGoUsageAutoRefresh,
		"ollama read":      h.GetOllamaCloudUsage,
		"ollama session":   h.SaveOllamaCloudUsageSession,
		"ollama delete":    h.DeleteOllamaCloudUsageSession,
		"ollama auto":      h.SetOllamaCloudUsageAutoRefresh,
		"ollama refresh":   h.RefreshOllamaCloudUsage,
	} {
		t.Run(name, func(t *testing.T) {
			c, recorder := newOpenCodeGoUsageHandlerContext(http.MethodGet, "/accounts/7/usage", "", "7")
			c.Request = c.Request.WithContext(context.Background())
			handle(c)
			require.Equal(t, http.StatusForbidden, recorder.Code)
		})
	}
}

func TestAccountUsageSettingsWritesRequireStationOwner(t *testing.T) {
	h := &AccountHandler{}
	for name, handle := range map[string]func(*gin.Context){
		"opencode": h.UpdateOpenCodeGoUsageSettings,
		"ollama":   h.UpdateOllamaCloudUsageSettings,
	} {
		t.Run(name, func(t *testing.T) {
			c, recorder := newOpenCodeGoUsageHandlerContext(http.MethodPut, "/accounts/usage/settings", `{}`, "")
			c.Request = c.Request.WithContext(service.WithScope(context.Background(), service.VendorScope(11, service.WorkspacePermissions{AccountManage: true})))
			handle(c)
			require.Equal(t, http.StatusForbidden, recorder.Code)
		})
	}
}
