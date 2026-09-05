package repository

import (
	"context"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/workspacegroupgrant"
	"github.com/Wei-Shaw/sub2api/ent/workspacemember"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type workspaceAdminRepository struct {
	client *dbent.Client
}

// NewWorkspaceAdminRepository 构造站长侧的工作区写仓储。
//
// 与只读的 NewWorkspaceRepository 分开：鉴权热路径只依赖读接口。
func NewWorkspaceAdminRepository(client *dbent.Client) service.WorkspaceAdminRepository {
	return &workspaceAdminRepository{client: client}
}

func (r *workspaceAdminRepository) Create(
	ctx context.Context,
	name, description string,
	perms service.WorkspacePermissions,
) (*domain.Workspace, error) {
	row, err := clientFromContext(ctx, r.client).Workspace.Create().
		SetName(name).
		SetDescription(description).
		SetStatus(domain.WorkspaceStatusActive).
		SetPermAccountManage(perms.AccountManage).
		SetPermGroupOps(perms.GroupOps).
		SetPermGroupBilling(perms.GroupBilling).
		SetPermProxyManage(perms.ProxyManage).
		SetPermMonitorView(perms.MonitorView).
		Save(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, nil, nil)
	}
	return workspaceFromEnt(row), nil
}

// Update 只写调用方显式提供的列。
//
// 权限档整档替换：站长每次提交的是完整的五个开关状态，
// 逐档 diff 反而容易在前端漏传时产生「静默保留旧权限」的歧义。
func (r *workspaceAdminRepository) Update(
	ctx context.Context,
	id int64,
	input service.WorkspaceUpdateInput,
) (*domain.Workspace, error) {
	upd := clientFromContext(ctx, r.client).Workspace.UpdateOneID(id).
		SetNillableName(input.Name).
		SetNillableDescription(input.Description).
		SetNillableStatus(input.Status)

	if p := input.Permissions; p != nil {
		upd = upd.
			SetPermAccountManage(p.AccountManage).
			SetPermGroupOps(p.GroupOps).
			SetPermGroupBilling(p.GroupBilling).
			SetPermProxyManage(p.ProxyManage).
			SetPermMonitorView(p.MonitorView)
	}

	// 结算区间是三态：nil 跳过、0 清列、其余写值。
	// 不能直接用 SetNillable* —— 那会把「清除该端」写成 0，
	// 而 0 在校验里是非法倍率，工作区就此进入改不动的状态。
	if v := input.SettlementRateMin; v != nil {
		if *v == 0 {
			upd = upd.ClearSettlementRateMin()
		} else {
			upd = upd.SetSettlementRateMin(*v)
		}
	}
	if v := input.SettlementRateMax; v != nil {
		if *v == 0 {
			upd = upd.ClearSettlementRateMax()
		} else {
			upd = upd.SetSettlementRateMax(*v)
		}
	}

	row, err := upd.Save(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, domain.ErrWorkspaceNotFound, nil)
	}
	return workspaceFromEnt(row), nil
}

// Delete 软删除工作区。
//
// 成员绑定与分组授权行保留：它们通过 workspace_id 指向已软删除的行，
// 而读路径的 GetByID 会因软删除拦截返回 not found，作用域随之失效。
func (r *workspaceAdminRepository) Delete(ctx context.Context, id int64) error {
	err := clientFromContext(ctx, r.client).Workspace.DeleteOneID(id).Exec(ctx)
	if err != nil {
		return translatePersistenceError(err, domain.ErrWorkspaceNotFound, nil)
	}
	return nil
}

func (r *workspaceAdminRepository) ListMembers(
	ctx context.Context,
	workspaceID int64,
) ([]*domain.WorkspaceMember, error) {
	rows, err := clientFromContext(ctx, r.client).WorkspaceMember.Query().
		Where(workspacemember.WorkspaceIDEQ(workspaceID)).
		Order(dbent.Asc(workspacemember.FieldID)).
		All(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, nil, nil)
	}
	out := make([]*domain.WorkspaceMember, 0, len(rows))
	for i := range rows {
		out = append(out, memberFromEnt(rows[i]))
	}
	return out, nil
}

// AddMember 绑定用户到工作区。
//
// 先查已有绑定再插入：表上的 UNIQUE(user_id) 是最终兜底，
// 但先查能给出「已属于哪个工作区」这一可操作的错误，而非裸唯一键冲突。
func (r *workspaceAdminRepository) AddMember(
	ctx context.Context,
	workspaceID, userID int64,
) (*domain.WorkspaceMember, error) {
	client := clientFromContext(ctx, r.client)

	existing, err := client.WorkspaceMember.Query().
		Where(workspacemember.UserIDEQ(userID)).
		Only(ctx)
	if err == nil {
		if existing.WorkspaceID == workspaceID {
			return memberFromEnt(existing), nil
		}
		return nil, domain.ErrWorkspaceMemberConflict
	}
	if !dbent.IsNotFound(err) {
		return nil, translatePersistenceError(err, nil, nil)
	}

	row, err := client.WorkspaceMember.Create().
		SetWorkspaceID(workspaceID).
		SetUserID(userID).
		Save(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, nil, domain.ErrWorkspaceMemberConflict)
	}
	return memberFromEnt(row), nil
}

func (r *workspaceAdminRepository) RemoveMember(ctx context.Context, workspaceID, userID int64) error {
	affected, err := clientFromContext(ctx, r.client).WorkspaceMember.Delete().
		Where(
			workspacemember.WorkspaceIDEQ(workspaceID),
			workspacemember.UserIDEQ(userID),
		).
		Exec(ctx)
	if err != nil {
		return translatePersistenceError(err, nil, nil)
	}
	if affected == 0 {
		return domain.ErrWorkspaceMemberNotFound
	}
	return nil
}

// UpsertGrant 新增或更新分组授权。
//
// 倍率与上限用 SetNillable*：nil 语义是「清除」，
// 由调用方 service 决定是否允许清除，仓储层不做策略判断。
func (r *workspaceAdminRepository) UpsertGrant(
	ctx context.Context,
	input service.WorkspaceGrantInput,
) (*domain.WorkspaceGroupGrant, error) {
	client := clientFromContext(ctx, r.client)

	existing, err := client.WorkspaceGroupGrant.Query().
		Where(
			workspacegroupgrant.WorkspaceIDEQ(input.WorkspaceID),
			workspacegroupgrant.GroupIDEQ(input.GroupID),
		).
		Only(ctx)

	switch {
	case err == nil:
		row, saveErr := client.WorkspaceGroupGrant.UpdateOneID(existing.ID).
			SetBasePriority(input.BasePriority).
			SetEnabled(input.Enabled).
			Save(ctx)
		if saveErr != nil {
			return nil, translatePersistenceError(saveErr, domain.ErrWorkspaceGrantNotFound, nil)
		}
		return grantFromEnt(row), nil

	case dbent.IsNotFound(err):
		row, createErr := client.WorkspaceGroupGrant.Create().
			SetWorkspaceID(input.WorkspaceID).
			SetGroupID(input.GroupID).
			SetBasePriority(input.BasePriority).
			SetEnabled(input.Enabled).
			Save(ctx)
		if createErr != nil {
			return nil, translatePersistenceError(createErr, nil, nil)
		}
		return grantFromEnt(row), nil

	default:
		return nil, translatePersistenceError(err, nil, nil)
	}
}

func (r *workspaceAdminRepository) DeleteGrant(ctx context.Context, workspaceID, groupID int64) error {
	affected, err := clientFromContext(ctx, r.client).WorkspaceGroupGrant.Delete().
		Where(
			workspacegroupgrant.WorkspaceIDEQ(workspaceID),
			workspacegroupgrant.GroupIDEQ(groupID),
		).
		Exec(ctx)
	if err != nil {
		return translatePersistenceError(err, nil, nil)
	}
	if affected == 0 {
		return domain.ErrWorkspaceGrantNotFound
	}
	return nil
}

func memberFromEnt(row *dbent.WorkspaceMember) *domain.WorkspaceMember {
	if row == nil {
		return nil
	}
	return &domain.WorkspaceMember{
		ID:          row.ID,
		UserID:      row.UserID,
		WorkspaceID: row.WorkspaceID,
		CreatedAt:   row.CreatedAt,
	}
}
