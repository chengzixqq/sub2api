package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// scopeContextKey 是作用域在 context 中的私有键类型，避免与其他包的键冲突。
type scopeContextKey struct{}

// Scope 描述一次管理端请求的数据作用域。
//
// 它由中间件在鉴权后写入 context，service 层各入口据此收窄读写范围。
// 之所以走 context 而不是给每个方法加参数：管理端账号/代理/分组共有 30+
// 个读写入口，逐个改签名会牵动全部 handler 与存量测试，收益不抵回归风险。
type Scope struct {
	// Unrestricted 为 true 时不做任何限制（站长）。
	Unrestricted bool
	// WorkspaceID 仅在 Unrestricted 为 false 时有效。
	WorkspaceID int64
	// Perms 为该工作区已开启的权限档，默认全 false。
	Perms WorkspacePermissions
	// SettlementRateMin/Max 为该工作区可自设的账号结算倍率区间。
	//
	// 随作用域携带而非在写入口回表：中间件解析作用域时本就已取到工作区行，
	// 再查一次只是同一行数据的重复读。
	//
	// Max 为 nil 表示站长未开放自助调价，供应商一律不得设置倍率 ——
	// 零值 Scope（中间件未挂载、或 context 缺失后的兜底）因此天然是
	// 「不可调」，与全局的默认拒绝取向一致。
	SettlementRateMin *float64
	SettlementRateMax *float64
}

// AdminScope 返回站长的不受限作用域。
func AdminScope() Scope {
	return Scope{Unrestricted: true}
}

// VendorScope 构造某工作区的受限作用域。
func VendorScope(workspaceID int64, perms WorkspacePermissions) Scope {
	return Scope{WorkspaceID: workspaceID, Perms: perms}
}

// VendorScopeWithSettlementRange 在受限作用域上附带结算倍率区间。
//
// 与 VendorScope 分开而非加参数：现有调用方（含 20+ 处测试）多数不关心
// 结算区间，加参数会把「未开放调价」和「忘了传」混成同一个零值。
func VendorScopeWithSettlementRange(workspaceID int64, perms WorkspacePermissions, min, max *float64) Scope {
	return Scope{
		WorkspaceID:       workspaceID,
		Perms:             perms,
		SettlementRateMin: min,
		SettlementRateMax: max,
	}
}

// SettlementRateAdjustable 报告本作用域是否可自设账号结算倍率。
//
// 站长恒可（Unrestricted）；供应商取决于站长是否设了上限。
func (s Scope) SettlementRateAdjustable() bool {
	return s.Unrestricted || s.SettlementRateMax != nil
}

// ValidateSettlementRate 校验供应商自设的账号倍率是否落在约定区间内。
//
// 站长不受约束 —— 工作区机制的硬约束是站长行为逐字不变。
// 下限缺省视为 0：站长只关心「不得高于」时不必被迫填一个下限。
func (s Scope) ValidateSettlementRate(rate float64) error {
	if s.Unrestricted {
		return nil
	}
	if s.SettlementRateMax == nil {
		return domain.ErrWorkspaceSettlementRateNotAdjustable
	}
	if rate < 0 || rate > *s.SettlementRateMax {
		return domain.ErrWorkspaceSettlementRateOutOfRange
	}
	if s.SettlementRateMin != nil && rate < *s.SettlementRateMin {
		return domain.ErrWorkspaceSettlementRateOutOfRange
	}
	return nil
}

// IsVendor 报告该作用域是否为受限的供应商视角。
func (s Scope) IsVendor() bool {
	return !s.Unrestricted
}

// WorkspaceFilter 返回列表查询应施加的工作区过滤值。
//
// 返回 nil 表示不过滤（站长）。仓储层据此决定是否追加
// workspace_id 谓词 —— 过滤必须下沉到 SQL，否则 Count 与分页会错乱。
func (s Scope) WorkspaceFilter() *int64 {
	if s.Unrestricted {
		return nil
	}
	id := s.WorkspaceID
	return &id
}

// OwnsWorkspace 报告目标资源的 workspace_id 是否属于当前作用域。
//
// resourceWorkspaceID 为 nil 表示资源不归属任何工作区（如站长本人的审计记录），
// 供应商一律无权触碰。
//
// 非正值与 OwnsWorkspaceID 同口径拒绝：零值 Scope 的 WorkspaceID 是 0，
// 若不挡住，它会与取值为 0 的迁移前残留行相等而放行。
func (s Scope) OwnsWorkspace(resourceWorkspaceID *int64) bool {
	if s.Unrestricted {
		return true
	}
	if resourceWorkspaceID == nil {
		return false
	}
	return s.OwnsWorkspaceID(*resourceWorkspaceID)
}

// RequireOwnership 校验资源归属，不符时返回 ErrWorkspaceScopeViolation。
//
// 该错误对外呈现为 404 而非 403：若回 403，供应商可借错误码差异
// 逐个 ID 探测出他人资源的存在性。
func (s Scope) RequireOwnership(resourceWorkspaceID *int64) error {
	if s.OwnsWorkspace(resourceWorkspaceID) {
		return nil
	}
	return domain.ErrWorkspaceScopeViolation
}

// OwnsWorkspaceID 是 OwnsWorkspace 的值语义版本，用于 workspace_id 为
// 非指针 int64 的领域模型（如 Account、Proxy）。
//
// 非正值一律不匹配。站长直管资源的归属是 DefaultWorkspaceID（1）而非 0，
// 所以 0 只会来自两种情形：迁移前的残留行，或零值 Scope。两者都必须拒绝 ——
// 零值 Scope 的 WorkspaceID 也是 0，若把 0 当作可匹配，它会与残留行相等而放行。
func (s Scope) OwnsWorkspaceID(resourceWorkspaceID int64) bool {
	if s.Unrestricted {
		return true
	}
	if resourceWorkspaceID <= 0 || s.WorkspaceID <= 0 {
		return false
	}
	return resourceWorkspaceID == s.WorkspaceID
}

// RequireAccountManage 校验账号管理权限。
func (s Scope) RequireAccountManage() error {
	return s.requirePerm(s.Perms.AccountManage)
}

// RequireGroupOps 校验分组运维权限。
func (s Scope) RequireGroupOps() error {
	return s.requirePerm(s.Perms.GroupOps)
}

// RequireGroupBilling 校验分组计费权限。
func (s Scope) RequireGroupBilling() error {
	return s.requirePerm(s.Perms.GroupBilling)
}

// RequireProxyManage 校验代理管理权限。
func (s Scope) RequireProxyManage() error {
	return s.requirePerm(s.Perms.ProxyManage)
}

// RequireMonitorView 校验监控查看权限。
func (s Scope) RequireMonitorView() error {
	return s.requirePerm(s.Perms.MonitorView)
}

func (s Scope) requirePerm(granted bool) error {
	if s.Unrestricted || granted {
		return nil
	}
	return domain.ErrWorkspacePermissionDenied
}

// WithScope 把作用域写入 context，供下游 service 读取。
func WithScope(ctx context.Context, scope Scope) context.Context {
	return context.WithValue(ctx, scopeContextKey{}, scope)
}

// ScopeFromContext 读取当前请求的作用域。
//
// 缺失时返回 false。调用方必须视为拒绝而非回落为不受限：
// 若中间件漏挂，静默放开全量数据的后果远重于功能不可用。
func ScopeFromContext(ctx context.Context) (Scope, bool) {
	if ctx == nil {
		return Scope{}, false
	}
	scope, ok := ctx.Value(scopeContextKey{}).(Scope)
	return scope, ok
}

// ScopeFromContextOrDeny 读取作用域，缺失时返回一个零权限的受限作用域。
//
// 零值 Scope 的 Unrestricted 为 false、WorkspaceID 为 0、权限全 false，
// 因此任何归属校验与权限校验都会失败 —— 这正是漏挂中间件时期望的行为。
func ScopeFromContextOrDeny(ctx context.Context) Scope {
	scope, ok := ScopeFromContext(ctx)
	if !ok {
		return Scope{}
	}
	return scope
}
