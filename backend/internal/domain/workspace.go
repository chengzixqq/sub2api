package domain

import (
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Workspace 是一家供应商（或站长直管）的资源边界。
//
// 供应商登录管理端后，所有读写都被收窄到自己所属的工作区：
// 账号与代理靠 accounts/proxies.workspace_id 直接归属，分组则靠
// WorkspaceGroupGrant 授权（一个分组可同时开放给多家供应商）。
type Workspace struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`

	Permissions WorkspacePermissions `json:"permissions"`

	// SettlementRateMin/Max 限定该供应商可自设的账号倍率区间。
	//
	// 供应商在账号管理里改的就是 accounts.rate_multiplier —— 那一列本就是
	// 上游成本倍率，也正是与站长结算的口径，不另立字段。
	//
	// Max 为 nil 表示不允许自改（默认如此，需站长显式放开）；
	// Min 为 nil 表示不限下限 —— 供应商主动降价对站长无害。
	SettlementRateMin *float64 `json:"settlement_rate_min,omitempty"`
	SettlementRateMax *float64 `json:"settlement_rate_max,omitempty"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

// IsActive 报告工作区是否处于可用状态。
// 停用的工作区其成员登录后应当什么都做不了（读也不行）。
func (w *Workspace) IsActive() bool {
	return w != nil && w.Status == WorkspaceStatusActive
}

// SettlementRateAdjustable 报告该工作区的供应商是否可自设账号倍率。
//
// 以「上限是否设定」为开关而非另加 bool：少一个可能与区间矛盾的状态位
// （开关为真但区间为空、或区间已设而开关忘了开）。站长填上限即放开。
func (w *Workspace) SettlementRateAdjustable() bool {
	return w != nil && w.SettlementRateMax != nil
}

// ValidateSettlementRate 校验供应商自设的账号倍率是否落在约定区间内。
//
// 返回 nil 表示放行。三类拒绝分别给出不同错误码，供应商能直接看出是
// 「不许改」还是「超出区间」，不必逐个试探。
//
// 非正值单独拦：倍率乘进成本里，0 会把结算金额抹成 0，负数更荒谬。
// 这一条与区间无关，即使站长把 min 设成 0 也不放行。
func (w *Workspace) ValidateSettlementRate(rate float64) error {
	if !w.SettlementRateAdjustable() {
		return ErrWorkspaceSettlementRateNotAdjustable
	}
	if rate <= 0 {
		return ErrWorkspaceInvalidCostRate
	}
	if rate > *w.SettlementRateMax {
		return ErrWorkspaceSettlementRateOutOfRange
	}
	if w.SettlementRateMin != nil && rate < *w.SettlementRateMin {
		return ErrWorkspaceSettlementRateOutOfRange
	}
	return nil
}

// WorkspacePermissions 是工作区的五个权限档，默认全 false。
//
// 新建工作区在站长显式开权限之前什么都做不了 —— 默认拒绝，
// 避免漏配权限导致越权。
type WorkspacePermissions struct {
	// AccountManage 允许在已授权分组内增删改自己工作区的账号。
	AccountManage bool `json:"account_manage"`
	// GroupOps 允许调整已授权分组的运营参数（不含计费字段）。
	GroupOps bool `json:"group_ops"`
	// GroupBilling 允许调整已授权分组的计费字段（含成本倍率下调）。
	GroupBilling bool `json:"group_billing"`
	// ProxyManage 允许管理自己工作区的代理。
	ProxyManage bool `json:"proxy_manage"`
	// MonitorView 允许查看自己工作区范围内的用量与监控。
	MonitorView bool `json:"monitor_view"`
}

// WorkspaceMember 把一个用户绑定到一个工作区。
// 一个用户至多属于一个工作区（表上有 UNIQUE(user_id) 兜底）。
type WorkspaceMember struct {
	ID          int64     `json:"id"`
	UserID      int64     `json:"user_id"`
	WorkspaceID int64     `json:"workspace_id"`
	CreatedAt   time.Time `json:"created_at"`
}

// WorkspaceGroupGrant 是站长把某个分组开放给某家供应商的授权记录。
//
// BasePriority 由站长锁定，供应商不可自行突破：新增账号强制套用此调度
// 优先级，避免多家互相抬价抢流量。
//
// 结算倍率不在这里 —— 它就是 accounts.rate_multiplier，可调区间挂在
// Workspace 上（见 SettlementRateMin/Max）。倍率是每账号一个值，而一个账号
// 可同时落在多个已授权分组里，按分组各设上限会撞出互相冲突的区间。
type WorkspaceGroupGrant struct {
	ID          int64 `json:"id"`
	WorkspaceID int64 `json:"workspace_id"`
	GroupID     int64 `json:"group_id"`

	BasePriority int `json:"base_priority"`

	Enabled bool `json:"enabled"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IsEffective 报告该授权当前是否生效。
// 停用的授权等同于「未授权」：供应商看不到也改不动这个分组。
func (g *WorkspaceGroupGrant) IsEffective() bool {
	return g != nil && g.Enabled
}

var (
	// ErrWorkspaceNotFound 表示工作区不存在或已被软删除。
	ErrWorkspaceNotFound = infraerrors.NotFound("WORKSPACE_NOT_FOUND", "workspace not found")
	// ErrWorkspaceMemberNotFound 表示该用户未绑定任何工作区。
	// vendor 角色出现这个错误说明数据不一致，鉴权链路必须拒绝放行。
	ErrWorkspaceMemberNotFound = infraerrors.NotFound("WORKSPACE_MEMBER_NOT_FOUND", "user is not bound to any workspace")
	// ErrWorkspaceGrantNotFound 表示该工作区未被授权此分组。
	ErrWorkspaceGrantNotFound = infraerrors.NotFound("WORKSPACE_GRANT_NOT_FOUND", "workspace has no grant for this group")
	// ErrWorkspaceDisabled 表示工作区被停用，其成员不得访问管理端。
	ErrWorkspaceDisabled = infraerrors.Forbidden("WORKSPACE_DISABLED", "workspace is disabled")
	// ErrWorkspacePermissionDenied 表示工作区未开启对应权限档。
	ErrWorkspacePermissionDenied = infraerrors.Forbidden("WORKSPACE_PERMISSION_DENIED", "workspace lacks permission for this operation")
	// ErrWorkspaceScopeViolation 表示目标资源不属于调用方工作区。
	// 对外与「不存在」同等对待，避免供应商借错误信息探测他人资源。
	ErrWorkspaceScopeViolation = infraerrors.NotFound("RESOURCE_NOT_FOUND", "resource not found")
	// ErrWorkspaceSharedGroupBilling 表示分组被多家工作区共享，计费类字段不可由供应商修改。
	ErrWorkspaceSharedGroupBilling = infraerrors.Forbidden("WORKSPACE_SHARED_GROUP_BILLING", "billing fields are locked on groups shared by multiple workspaces")
	// ErrWorkspaceMemberConflict 表示该用户已绑定到另一个工作区。
	// 一个用户至多属于一个工作区，换绑必须先解绑，避免作用域出现二义性。
	ErrWorkspaceMemberConflict = infraerrors.Conflict("WORKSPACE_MEMBER_CONFLICT", "user is already bound to another workspace")
	// ErrWorkspaceMemberAdminNotAllowed 表示不能把站长绑定为工作区成员。
	//
	// 普通用户绑定时会被自动提升为 vendor，唯独 admin 不做这个提升：
	// 那是不可逆的权限收缩，还可能降掉最后一个 admin 而锁死后台。
	ErrWorkspaceMemberAdminNotAllowed = infraerrors.BadRequest("WORKSPACE_MEMBER_ADMIN_NOT_ALLOWED", "an administrator cannot be bound as a workspace member")
	// ErrWorkspaceSettlementRateOutOfRange 表示供应商自设的账号倍率越出约定区间。
	ErrWorkspaceSettlementRateOutOfRange = infraerrors.BadRequest("WORKSPACE_SETTLEMENT_RATE_OUT_OF_RANGE", "settlement rate multiplier is outside the agreed range")
	// ErrWorkspaceSettlementRateNotAdjustable 表示站长未设上限，供应商不得自设倍率。
	ErrWorkspaceSettlementRateNotAdjustable = infraerrors.Forbidden("WORKSPACE_SETTLEMENT_RATE_NOT_ADJUSTABLE", "settlement rate is not self-adjustable for this workspace")
	// ErrWorkspaceInvalidSettlementRange 表示站长设定的区间自相矛盾（min > max）。
	ErrWorkspaceInvalidSettlementRange = infraerrors.BadRequest("WORKSPACE_INVALID_SETTLEMENT_RANGE", "settlement rate min must not exceed max")
	// ErrWorkspaceNameRequired 表示工作区名称为空。
	ErrWorkspaceNameRequired = infraerrors.BadRequest("WORKSPACE_NAME_REQUIRED", "workspace name is required")
	// ErrWorkspaceInvalidStatus 表示状态取值非法（仅 active | disabled）。
	ErrWorkspaceInvalidStatus = infraerrors.BadRequest("WORKSPACE_INVALID_STATUS", "workspace status must be active or disabled")
	// ErrWorkspaceInvalidCostRate 表示倍率取值非正。
	ErrWorkspaceInvalidCostRate = infraerrors.BadRequest("WORKSPACE_INVALID_COST_RATE", "cost rate must be greater than zero")
)
