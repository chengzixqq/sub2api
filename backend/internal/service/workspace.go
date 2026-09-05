package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// 工作区领域类型在 service 包的重导出，遵循仓库既有惯例。
type (
	Workspace            = domain.Workspace
	WorkspacePermissions = domain.WorkspacePermissions
	WorkspaceMember      = domain.WorkspaceMember
	WorkspaceGroupGrant  = domain.WorkspaceGroupGrant
)

// WorkspaceRepository 提供工作区、成员绑定与分组授权的读取能力。
//
// 只读接口：工作区的增删改由站长通过独立的管理端 service 完成，
// 而鉴权链路（每请求都要走）只需要读。拆开可以让作用域校验的依赖面最小。
type WorkspaceRepository interface {
	GetByID(ctx context.Context, id int64) (*Workspace, error)
	List(ctx context.Context) ([]*Workspace, error)

	// GetByUserID 返回用户所属工作区；未绑定时返回 domain.ErrWorkspaceMemberNotFound。
	GetByUserID(ctx context.Context, userID int64) (*Workspace, error)

	// ListGrantsByWorkspace 返回该工作区的全部授权，含已停用项。
	ListGrantsByWorkspace(ctx context.Context, workspaceID int64) ([]*WorkspaceGroupGrant, error)
	// GetGrant 返回指定授权；未授权时返回 domain.ErrWorkspaceGrantNotFound。
	GetGrant(ctx context.Context, workspaceID, groupID int64) (*WorkspaceGroupGrant, error)
	// CountGrantsByGroup 统计某分组的生效授权数，用于共享分组的计费锁定判定。
	CountGrantsByGroup(ctx context.Context, groupID int64) (int, error)
}

// WorkspaceAdminRepository 提供站长侧的工作区写能力。
//
// 与只读的 WorkspaceRepository 分开是刻意的：鉴权链路每个请求都要走读接口，
// 让它不依赖任何写方法，可以把作用域校验的依赖面压到最小。
type WorkspaceAdminRepository interface {
	Create(ctx context.Context, name, description string, perms WorkspacePermissions) (*Workspace, error)
	// Update 只写 name/description/status/permissions 与结算倍率区间，
	// 不触碰成员与授权。
	Update(ctx context.Context, id int64, input WorkspaceUpdateInput) (*Workspace, error)
	// Delete 软删除工作区。成员绑定与分组授权一并失效。
	Delete(ctx context.Context, id int64) error

	// ListMembers 返回该工作区的成员绑定。
	ListMembers(ctx context.Context, workspaceID int64) ([]*WorkspaceMember, error)
	// AddMember 绑定用户到工作区。用户已属于其他工作区时返回
	// domain.ErrWorkspaceMemberConflict。
	AddMember(ctx context.Context, workspaceID, userID int64) (*WorkspaceMember, error)
	// RemoveMember 解除绑定；未绑定时返回 domain.ErrWorkspaceMemberNotFound。
	RemoveMember(ctx context.Context, workspaceID, userID int64) error

	// UpsertGrant 新增或更新分组授权。
	UpsertGrant(ctx context.Context, input WorkspaceGrantInput) (*WorkspaceGroupGrant, error)
	// DeleteGrant 移除授权；不存在时返回 domain.ErrWorkspaceGrantNotFound。
	DeleteGrant(ctx context.Context, workspaceID, groupID int64) error
}

// WorkspaceUpdateInput 描述一次工作区更新。
//
// 全部字段用指针：nil 表示本次不改该列，避免站长只改名字时
// 把权限档连带回退成零值（默认全 false 等于收走全部权限）。
type WorkspaceUpdateInput struct {
	Name        *string
	Description *string
	Status      *string
	Permissions *WorkspacePermissions

	// SettlementRateMin/Max 为结算倍率可调区间。
	//
	// 指针语义在这里有三态，不能简化：nil 表示本次不改，
	// 非 nil 且指向 0 表示清除该端（那一侧不再设限）。
	// 供应商在账号管理里改 rate_multiplier 时按此区间校验。
	SettlementRateMin *float64
	SettlementRateMax *float64
}

// WorkspaceGrantInput 描述一次分组授权的写入。
//
// 不含倍率字段：结算倍率就是账号自身的 rate_multiplier，
// 可调区间挂在工作区上（一个供应商一份约定价），与分组无关。
type WorkspaceGrantInput struct {
	WorkspaceID int64
	GroupID     int64

	BasePriority int

	Enabled bool
}
