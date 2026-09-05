package middleware

import (
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// ContextKeyVendorScope 工作区作用域上下文键。
const ContextKeyVendorScope ContextKey = "vendor_scope"

// VendorScope 描述当前请求的数据作用域与权限档。
//
// admin 为 Unrestricted，行为与引入工作区之前完全一致；
// vendor 携带 WorkspaceID 与权限集，管理端所有读写需按 WorkspaceID 收窄。
type VendorScope struct {
	// Unrestricted 为 true 时不做任何作用域限制（admin）。
	Unrestricted bool
	// WorkspaceID 仅在 Unrestricted 为 false 时有效。
	WorkspaceID int64
	// Perms 为该工作区已开启的权限档，默认全 false（默认拒绝）。
	Perms service.WorkspacePermissions
	// SettlementRateMin/Max 为该工作区可自设的账号结算倍率区间。
	//
	// Max 为 nil 表示站长未开放自助调价。零值 VendorScope 因此天然不可调，
	// 与 Perms 全 false 的默认拒绝取向一致。
	SettlementRateMin *float64
	SettlementRateMax *float64
}

// Allows 判断作用域是否具备指定权限档。Unrestricted 恒为 true。
//
// pick 从权限结构体中取出目标档位，例如：
//
//	scope.Allows(func(p service.WorkspacePermissions) bool { return p.AccountManage })
func (s VendorScope) Allows(pick func(service.WorkspacePermissions) bool) bool {
	if s.Unrestricted {
		return true
	}
	return pick(s.Perms)
}

// AdminScope 返回不受限作用域。
func AdminScope() VendorScope {
	return VendorScope{Unrestricted: true}
}

// GetVendorScopeFromContext 读取当前请求的作用域。
//
// 缺失时返回 false —— 调用方必须视为拒绝，不可回落为不受限，
// 避免中间件未挂载时静默放开全量数据。
func GetVendorScopeFromContext(c *gin.Context) (VendorScope, bool) {
	value, exists := c.Get(string(ContextKeyVendorScope))
	if !exists {
		return VendorScope{}, false
	}
	scope, ok := value.(VendorScope)
	return scope, ok
}

// NewVendorScopeMiddleware 构造作用域解析中间件，须挂在管理端鉴权之后。
//
// admin → 不受限；vendor → 按成员关系解析工作区与权限，并校验路由白名单。
// 角色未知或工作区不可用时一律拒绝。
func NewVendorScopeMiddleware(workspaceService *service.WorkspaceService) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, ok := GetUserRoleFromContext(c)
		if !ok {
			AbortWithError(c, 401, "UNAUTHORIZED", "User not found in context")
			return
		}

		if role == service.RoleAdmin {
			applyScope(c, AdminScope())
			c.Next()
			return
		}

		if role != service.RoleVendor {
			AbortWithError(c, 403, "FORBIDDEN", "Admin access required")
			return
		}

		subject, ok := GetAuthSubjectFromContext(c)
		if !ok {
			AbortWithError(c, 401, "UNAUTHORIZED", "User not found in context")
			return
		}

		scope, err := resolveVendorScope(c, workspaceService, subject.UserID)
		if err != nil {
			return
		}

		if !vendorRouteAllowed(c, scope) {
			AbortWithError(c, 403, "FORBIDDEN", "This operation is not available for your workspace")
			return
		}

		applyScope(c, scope)

		// 用量与看板查询按账号白名单收窄。只在这些路由解析，
		// 避免每个管理端请求都多一次账号 ID 查询。
		if usageScopeRequired(c) && !injectUsageAccountScope(c, workspaceService, scope.WorkspaceID) {
			return
		}

		c.Next()
	}
}

// applyScope 同时把作用域写入 gin context 与请求 context。
//
// gin context 供 handler 做展示层判断（例如按权限裁剪响应字段），
// 请求 context 供 service 层收窄数据读写 —— service 不依赖 gin，
// 只能从 context.Context 取。两处必须同源，否则会出现展示与
// 数据作用域不一致的越权面。
func applyScope(c *gin.Context, scope VendorScope) {
	c.Set(string(ContextKeyVendorScope), scope)
	c.Request = c.Request.WithContext(
		service.WithScope(c.Request.Context(), scope.toServiceScope()),
	)
}

// toServiceScope 把中间件作用域转换为 service 层作用域。
func (s VendorScope) toServiceScope() service.Scope {
	if s.Unrestricted {
		return service.AdminScope()
	}
	return service.VendorScopeWithSettlementRange(
		s.WorkspaceID, s.Perms, s.SettlementRateMin, s.SettlementRateMax,
	)
}

// resolveVendorScope 解析 vendor 的工作区作用域。
//
// 失败时已写入响应并中断请求，调用方只需直接 return。
func resolveVendorScope(c *gin.Context, workspaceService *service.WorkspaceService, userID int64) (VendorScope, error) {
	if workspaceService == nil {
		// 依赖未装配时拒绝而非放行：宁可 vendor 用不了，也不能让它看到全量数据。
		AbortWithError(c, 403, "FORBIDDEN", "Workspace support is not available")
		return VendorScope{}, errVendorScopeUnavailable
	}

	workspace, err := workspaceService.ResolveByUserID(c.Request.Context(), userID)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrWorkspaceMemberNotFound):
			// vendor 未绑定工作区属于数据不一致，拒绝并留痕。
			logger.LegacyPrintf("middleware.vendor_scope",
				"[VendorScope] vendor user %d is not bound to any workspace", userID)
			AbortWithError(c, 403, "FORBIDDEN", "No workspace is bound to this account")
		case errors.Is(err, domain.ErrWorkspaceDisabled):
			AbortWithError(c, 403, "WORKSPACE_DISABLED", "Your workspace is disabled")
		default:
			logger.LegacyPrintf("middleware.vendor_scope",
				"[VendorScope] failed to resolve workspace for user %d: %v", userID, err)
			AbortWithError(c, 500, "INTERNAL_ERROR", "Failed to resolve workspace")
		}
		return VendorScope{}, err
	}

	return VendorScope{
		WorkspaceID:       workspace.ID,
		Perms:             workspace.Permissions,
		SettlementRateMin: workspace.SettlementRateMin,
		SettlementRateMax: workspace.SettlementRateMax,
	}, nil
}

// usageScopeRequired 报告当前路由是否查询用量日志，需要注入账号白名单。
//
// 判定复用 vendorUsageScopedRoutes —— 与放行白名单同源，避免两份清单漂移：
// 新增一条用量类 vendor 路由时，若忘了在此登记，请求会在无白名单的情况下
// 落到用量查询上。为此 vendorUsageScopedRoutes 直接引用放行表中的同一批前缀。
func usageScopeRequired(c *gin.Context) bool {
	fullPath := c.FullPath()
	if fullPath == "" {
		return false
	}
	for _, prefix := range vendorUsageScopedRoutes {
		if matchesPrefix(fullPath, prefix) {
			return true
		}
	}
	return false
}

// injectUsageAccountScope 解析账号白名单并写入请求 context。
//
// 返回 false 表示已写入错误响应并中断，调用方须直接 return。
// 解析失败时拒绝而非放行 —— 拿不到白名单就无法收窄，
// 放行等于让 vendor 看到全站用量。
func injectUsageAccountScope(c *gin.Context, workspaceService *service.WorkspaceService, workspaceID int64) bool {
	accountIDs, err := workspaceService.AccountIDsForWorkspace(c.Request.Context(), workspaceID)
	if err != nil {
		logger.LegacyPrintf("middleware.vendor_scope",
			"[VendorScope] failed to resolve account scope for workspace %d: %v", workspaceID, err)
		AbortWithError(c, 500, "INTERNAL_ERROR", "Failed to resolve usage scope")
		return false
	}

	c.Request = c.Request.WithContext(
		service.WithUsageAccountScope(c.Request.Context(), accountIDs),
	)
	return true
}

// errVendorScopeUnavailable 内部哨兵：工作区服务未装配。
var errVendorScopeUnavailable = errors.New("vendor scope unavailable")
