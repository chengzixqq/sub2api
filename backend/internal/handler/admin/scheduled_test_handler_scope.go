package admin

import (
	"database/sql"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// 定时测试计划的归属校验。
//
// 计划表只有 account_id，没有 workspace_id，计划服务也不读作用域，
// 所以归属一律经账号判定：GetAccount 走 GetByIDScoped，别家工作区的账号
// 在那里就是 not found。站长不受限，不做额外查询，行为与引入校验前一致。
//
// 路由层目前只经 /admin/accounts 的 GET 前缀对 vendor 放行
// GET /accounts/:id/scheduled-test-plans；/scheduled-test-plans 下的写与结果
// 列表不在白名单里，这里的校验是日后放开时的兜底。

// scheduledTestScope 读取作用域并校验权限档：读需要账号管理或监控档，写需要账号管理档。
// 返回 vendor=false 表示站长，调用方无需再查归属。
func scheduledTestScope(c *gin.Context, write bool) (vendor bool, ok bool) {
	scope, found := service.ScopeFromContext(c.Request.Context())
	if !found {
		response.ErrorFrom(c, domain.ErrWorkspacePermissionDenied)
		return false, false
	}
	if !scope.IsVendor() {
		return false, true
	}
	permitted := scope.Perms.AccountManage || (!write && scope.Perms.MonitorView)
	if !permitted {
		response.ErrorFrom(c, domain.ErrWorkspacePermissionDenied)
		return true, false
	}
	return true, true
}

// authorizeAccount 校验当前作用域可以读（或写）该账号的定时测试计划。
// 别家工作区的账号返回 404 ACCOUNT_NOT_FOUND，与账号详情接口一致。
func (h *ScheduledTestHandler) authorizeAccount(c *gin.Context, accountID int64, write bool) bool {
	vendor, ok := scheduledTestScope(c, write)
	if !ok || !vendor {
		return ok
	}
	if _, err := h.adminService.GetAccount(c.Request.Context(), accountID); err != nil {
		response.ErrorFrom(c, err)
		return false
	}
	return true
}

// authorizePlan 经计划所属账号校验归属。
// 计划不存在与计划属于别家工作区都返回 404 RESOURCE_NOT_FOUND，
// 避免 vendor 借错误差异探测他人计划 ID 是否存在。
func (h *ScheduledTestHandler) authorizePlan(c *gin.Context, planID int64, write bool) bool {
	vendor, ok := scheduledTestScope(c, write)
	if !ok || !vendor {
		return ok
	}
	ctx := c.Request.Context()
	plan, err := h.scheduledTestSvc.GetPlan(ctx, planID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			response.ErrorFrom(c, domain.ErrWorkspaceScopeViolation)
			return false
		}
		response.ErrorFrom(c, err)
		return false
	}
	if _, err := h.adminService.GetAccount(ctx, plan.AccountID); err != nil {
		if infraerrors.IsNotFound(err) {
			response.ErrorFrom(c, domain.ErrWorkspaceScopeViolation)
			return false
		}
		response.ErrorFrom(c, err)
		return false
	}
	return true
}
