package service

import "context"

// workspaceMemberUserAdapter 把完整的 UserRepository 收窄成
// WorkspaceMemberUserReader 所需的两个动作。
//
// 为什么用适配器而不给 UserRepository 加 PromoteToVendor：
// 那会在一个 40+ 方法的公共接口上再开一个「改角色」的口子，
// 任何持有该仓储的代码都能顺手调用。收窄在这里，改角色的能力
// 就只存在于工作区成员绑定这一条路径上。
type workspaceMemberUserAdapter struct {
	repo UserRepository
}

// NewWorkspaceMemberUserReader 构造工作区成员绑定所需的用户能力。
func NewWorkspaceMemberUserReader(repo UserRepository) WorkspaceMemberUserReader {
	return &workspaceMemberUserAdapter{repo: repo}
}

func (a *workspaceMemberUserAdapter) GetByID(ctx context.Context, id int64) (*User, error) {
	return a.repo.GetByID(ctx, id)
}

// PromoteToVendor 只写 role 一列，把用户角色置为 vendor。
//
// 走 UserUpdateFields{Role: true} 而非整行写回：并发的余额扣款、
// 最后活跃时间更新都在同一行上，整行写回会用旧快照抹掉它们。
//
// 幂等：调用方已确认目标不是 admin，重复置为 vendor 无副作用。
func (a *workspaceMemberUserAdapter) PromoteToVendor(ctx context.Context, id int64) error {
	user, err := a.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if user == nil {
		return ErrUserNotFound
	}
	user.Role = RoleVendor
	return a.repo.Update(ctx, user, UserUpdateFields{Role: true})
}
