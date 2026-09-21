//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestChannelMonitorProbeHandler_RequiresOwnerRoleAndScope(t *testing.T) {
	for _, tc := range []struct {
		name, role string
		scope      *service.Scope
	}{
		{name: "missing role"},
		{name: "vendor", role: "vendor", scope: &service.Scope{WorkspaceID: 1}},
		{name: "admin missing scope", role: "admin"},
		{name: "admin restricted scope", role: "admin", scope: &service.Scope{WorkspaceID: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			ctx := context.Background()
			if tc.scope != nil {
				ctx = service.WithScope(ctx, *tc.scope)
			}
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/channel-monitor-v2/probe-budget", nil).WithContext(ctx)
			if tc.role != "" {
				c.Set(string(middleware.ContextKeyUserRole), tc.role)
			}
			NewChannelMonitorProbeHandler(nil).Budget(c)
			require.Equal(t, http.StatusForbidden, w.Code)
		})
	}
}
