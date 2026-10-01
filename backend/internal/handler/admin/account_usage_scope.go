package admin

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func requireAccountUsageScope(c *gin.Context) bool {
	scope, ok := service.ScopeFromContext(c.Request.Context())
	if !ok || (scope.IsVendor() && !scope.Perms.AccountManage &&
		(c.Request.Method != http.MethodGet || !scope.Perms.MonitorView)) {
		response.ErrorFrom(c, domain.ErrWorkspacePermissionDenied)
		return false
	}
	return true
}
