package repository

import (
	"context"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/workspace"
	"github.com/Wei-Shaw/sub2api/ent/workspacegroupgrant"
	"github.com/Wei-Shaw/sub2api/ent/workspacemember"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type workspaceRepository struct {
	client *dbent.Client
}

// NewWorkspaceRepository 构造工作区仓储。
//
// 软删除由 SoftDeleteMixin 自动拦截，所有查询默认已排除 deleted_at 非空的行。
func NewWorkspaceRepository(client *dbent.Client) service.WorkspaceRepository {
	return &workspaceRepository{client: client}
}

func (r *workspaceRepository) GetByID(ctx context.Context, id int64) (*domain.Workspace, error) {
	row, err := clientFromContext(ctx, r.client).Workspace.Get(ctx, id)
	if err != nil {
		return nil, translatePersistenceError(err, domain.ErrWorkspaceNotFound, nil)
	}
	return workspaceFromEnt(row), nil
}

func (r *workspaceRepository) List(ctx context.Context) ([]*domain.Workspace, error) {
	rows, err := clientFromContext(ctx, r.client).Workspace.Query().
		Order(dbent.Asc(workspace.FieldID)).
		All(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, nil, nil)
	}
	out := make([]*domain.Workspace, 0, len(rows))
	for i := range rows {
		out = append(out, workspaceFromEnt(rows[i]))
	}
	return out, nil
}

// GetByUserID 返回该用户所属的工作区。
// 用户未绑定任何工作区时返回 domain.ErrWorkspaceMemberNotFound。
func (r *workspaceRepository) GetByUserID(ctx context.Context, userID int64) (*domain.Workspace, error) {
	client := clientFromContext(ctx, r.client)
	member, err := client.WorkspaceMember.Query().
		Where(workspacemember.UserIDEQ(userID)).
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, domain.ErrWorkspaceMemberNotFound, nil)
	}
	return r.GetByID(ctx, member.WorkspaceID)
}

func workspaceFromEnt(row *dbent.Workspace) *domain.Workspace {
	if row == nil {
		return nil
	}
	w := &domain.Workspace{
		ID:          row.ID,
		Name:        row.Name,
		Description: row.Description,
		Status:      row.Status,
		Permissions: domain.WorkspacePermissions{
			AccountManage: row.PermAccountManage,
			GroupOps:      row.PermGroupOps,
			GroupBilling:  row.PermGroupBilling,
			ProxyManage:   row.PermProxyManage,
			MonitorView:   row.PermMonitorView,
		},
		SettlementRateMin: row.SettlementRateMin,
		SettlementRateMax: row.SettlementRateMax,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
	if row.DeletedAt != nil {
		w.DeletedAt = row.DeletedAt
	}
	return w
}

// ListGrantsByWorkspace 返回该工作区的全部分组授权（含已停用的）。
// 调用方按需用 IsEffective() 过滤 —— 管理端要展示停用项，鉴权链路则不能放行。
func (r *workspaceRepository) ListGrantsByWorkspace(ctx context.Context, workspaceID int64) ([]*domain.WorkspaceGroupGrant, error) {
	rows, err := clientFromContext(ctx, r.client).WorkspaceGroupGrant.Query().
		Where(workspacegroupgrant.WorkspaceIDEQ(workspaceID)).
		Order(dbent.Asc(workspacegroupgrant.FieldGroupID)).
		All(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, nil, nil)
	}
	out := make([]*domain.WorkspaceGroupGrant, 0, len(rows))
	for i := range rows {
		out = append(out, grantFromEnt(rows[i]))
	}
	return out, nil
}

// GetGrant 返回指定 (workspace, group) 的授权记录。
// 未授权时返回 domain.ErrWorkspaceGrantNotFound。
func (r *workspaceRepository) GetGrant(ctx context.Context, workspaceID, groupID int64) (*domain.WorkspaceGroupGrant, error) {
	row, err := clientFromContext(ctx, r.client).WorkspaceGroupGrant.Query().
		Where(
			workspacegroupgrant.WorkspaceIDEQ(workspaceID),
			workspacegroupgrant.GroupIDEQ(groupID),
		).
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, domain.ErrWorkspaceGrantNotFound, nil)
	}
	return grantFromEnt(row), nil
}

// CountGrantsByGroup 统计某个分组被多少个工作区授权。
//
// 计费类改动要用它做「共享分组」判定：一个分组同时开放给多家供应商时，
// 任何一家单方面改计费都会影响其他家，因此必须拒绝。
func (r *workspaceRepository) CountGrantsByGroup(ctx context.Context, groupID int64) (int, error) {
	count, err := clientFromContext(ctx, r.client).WorkspaceGroupGrant.Query().
		Where(
			workspacegroupgrant.GroupIDEQ(groupID),
			workspacegroupgrant.EnabledEQ(true),
		).
		Count(ctx)
	if err != nil {
		return 0, translatePersistenceError(err, nil, nil)
	}
	return count, nil
}

func grantFromEnt(row *dbent.WorkspaceGroupGrant) *domain.WorkspaceGroupGrant {
	if row == nil {
		return nil
	}
	return &domain.WorkspaceGroupGrant{
		ID:           row.ID,
		WorkspaceID:  row.WorkspaceID,
		GroupID:      row.GroupID,
		BasePriority: row.BasePriority,
		Enabled:      row.Enabled,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}
