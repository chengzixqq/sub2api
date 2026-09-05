package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// memberWsRepo 是一个「工作区总能查到」的 WorkspaceRepository 替身。
//
// 与 grantRepo 分开写：后者的 GetByID 恒返回 NotFound，成员绑定测试
// 需要先过工作区存在校验才能触达角色判定。
type memberWsRepo struct{}

func (r *memberWsRepo) GetByID(_ context.Context, id int64) (*Workspace, error) {
	return &Workspace{ID: id, Name: "A 家", Status: domain.WorkspaceStatusActive}, nil
}
func (r *memberWsRepo) List(_ context.Context) ([]*Workspace, error) { return nil, nil }
func (r *memberWsRepo) GetByUserID(_ context.Context, _ int64) (*Workspace, error) {
	return nil, domain.ErrWorkspaceMemberNotFound
}
func (r *memberWsRepo) ListGrantsByWorkspace(_ context.Context, _ int64) ([]*WorkspaceGroupGrant, error) {
	return nil, nil
}
func (r *memberWsRepo) GetGrant(_ context.Context, _, _ int64) (*WorkspaceGroupGrant, error) {
	return nil, domain.ErrWorkspaceGrantNotFound
}
func (r *memberWsRepo) CountGrantsByGroup(_ context.Context, _ int64) (int, error) { return 1, nil }

// memberUserRepo 是 WorkspaceMemberUserReader 的替身。
//
// promoted 记录 PromoteToVendor 被调的次数：本文件的核心断言是
// 「该提升时提升、不该提升时一次都不碰」，只看返回值判不出后者。
type memberUserRepo struct {
	user        *User
	err         error
	promoteErr  error
	promoted    int
	promotedIDs []int64
}

func (r *memberUserRepo) GetByID(_ context.Context, _ int64) (*User, error) {
	return r.user, r.err
}

func (r *memberUserRepo) PromoteToVendor(_ context.Context, id int64) error {
	r.promoted++
	r.promotedIDs = append(r.promotedIDs, id)
	return r.promoteErr
}

func newMemberSvc(userRepo WorkspaceMemberUserReader) (*WorkspaceAdminService, *adminRepoStub) {
	write := &adminRepoStub{}
	read := &memberWsRepo{}
	return NewWorkspaceAdminService(write, read, NewWorkspaceService(read, nil), nil, userRepo), write
}

// TestAddMemberPromotesPlainUser 锁定普通用户绑定时自动提升为 vendor。
//
// 站长在工作区页选中某人的意图就是「让他当供应商」。若只绑定不改角色，
// 作用域中间件仍把他当普通用户，对方登录后毫无变化 —— 一次静默失效的配置。
// 因此绑定这个动作必须自己把角色补齐，而不是让站长再跑一趟用户管理页。
func TestAddMemberPromotesPlainUser(t *testing.T) {
	userRepo := &memberUserRepo{user: &User{ID: 9, Role: RoleUser}}
	svc, _ := newMemberSvc(userRepo)

	result, err := svc.AddMember(context.Background(), 7, 9)
	if err != nil {
		t.Fatalf("普通用户应可绑定并被提升，得到 err=%v", err)
	}
	if !result.RolePromoted {
		t.Error("RolePromoted 必须为 true —— 前端靠它提示站长角色已变更")
	}
	if userRepo.promoted != 1 {
		t.Fatalf("应恰好提升一次，实际 %d 次", userRepo.promoted)
	}
	if len(userRepo.promotedIDs) != 1 || userRepo.promotedIDs[0] != 9 {
		t.Errorf("提升的应是目标用户 9，实际 %v", userRepo.promotedIDs)
	}
}

// TestAddMemberRejectsAdmin 锁定站长不会被降级成供应商。
//
// 这是唯一必须拒绝的角色：admin → vendor 是不可逆的权限收缩，
// 若降掉最后一个站长，后台再没人能改回来。
func TestAddMemberRejectsAdmin(t *testing.T) {
	userRepo := &memberUserRepo{user: &User{ID: 1, Role: RoleAdmin}}
	svc, write := newMemberSvc(userRepo)

	_, err := svc.AddMember(context.Background(), 7, 1)
	if !errors.Is(err, domain.ErrWorkspaceMemberAdminNotAllowed) {
		t.Fatalf("admin 应被拒，得到 %v", err)
	}
	if userRepo.promoted != 0 {
		t.Error("被拒时不得改动角色")
	}
	if write.addedMembers != 0 {
		t.Error("被拒时不得写入绑定")
	}
}

// TestAddMemberKeepsExistingVendor 是正向锚点，且锁定不做多余写入。
//
// 目标已是 vendor 时重复写一次 role 列不会出错，但会污染
// 用户表的 updated_at，让审计上看起来「角色刚被改过」。
func TestAddMemberKeepsExistingVendor(t *testing.T) {
	userRepo := &memberUserRepo{user: &User{ID: 9, Role: RoleVendor}}
	svc, _ := newMemberSvc(userRepo)

	result, err := svc.AddMember(context.Background(), 7, 9)
	if err != nil {
		t.Fatalf("vendor 应可绑定，得到 err=%v", err)
	}
	if result.RolePromoted {
		t.Error("已是 vendor 不算提升，回报 true 会让站长以为角色被改过")
	}
	if userRepo.promoted != 0 {
		t.Error("已是 vendor 时不应写 role 列")
	}
	if result.Member.WorkspaceID != 7 || result.Member.UserID != 9 {
		t.Errorf("绑定关系写错：workspace=%d user=%d",
			result.Member.WorkspaceID, result.Member.UserID)
	}
}

// TestAddMemberSkipsBindingWhenPromoteFails 锁定提升失败不落绑定。
//
// 顺序是先提角色再写绑定：反过来会留下一条「已绑定但仍是普通用户」的
// 记录，正是自动提升要消灭的那种静默失效状态。
func TestAddMemberSkipsBindingWhenPromoteFails(t *testing.T) {
	boom := errors.New("用户表不可写")
	userRepo := &memberUserRepo{user: &User{ID: 9, Role: RoleUser}, promoteErr: boom}
	svc, write := newMemberSvc(userRepo)

	if _, err := svc.AddMember(context.Background(), 7, 9); !errors.Is(err, boom) {
		t.Fatalf("提升失败应向上传递，得到 %v", err)
	}
	if write.addedMembers != 0 {
		t.Error("角色没提上去就不该写绑定，否则留下静默失效的成员")
	}
}

// TestAddMemberRejectsMissingUser 锁定用户不存在时不落库。
//
// 站长手输 ID 是最容易出错的一步，写进去就成了一条查不到人的脏绑定。
func TestAddMemberRejectsMissingUser(t *testing.T) {
	userRepo := &memberUserRepo{err: ErrUserNotFound}
	svc, write := newMemberSvc(userRepo)

	if _, err := svc.AddMember(context.Background(), 7, 404); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("用户不存在应返回 ErrUserNotFound，得到 %v", err)
	}
	if write.addedMembers != 0 || userRepo.promoted != 0 {
		t.Error("查不到人时既不该提角色也不该写绑定")
	}
}

// TestAddMemberTreatsNilUserAsMissing 锁定 (nil, nil) 不会打成 panic。
//
// 接口未禁止这种返回，替身漏写就会让一次配置失误变成 500。
func TestAddMemberTreatsNilUserAsMissing(t *testing.T) {
	svc, _ := newMemberSvc(&memberUserRepo{})

	if _, err := svc.AddMember(context.Background(), 7, 9); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("nil 用户应视为查不到，得到 %v", err)
	}
}

// TestRemoveMemberSkipsRoleCheck 锁定解绑不校验角色、也不动角色。
//
// 历史绑定与降级后的用户都必须解得掉；解绑同时把 vendor 降回普通用户
// 则是另一回事 —— 一个人可能被移出 A 家再加入 B 家，中途降级会打断流程。
func TestRemoveMemberSkipsRoleCheck(t *testing.T) {
	userRepo := &memberUserRepo{user: &User{ID: 9, Role: RoleUser}}
	svc, _ := newMemberSvc(userRepo)

	if err := svc.RemoveMember(context.Background(), 7, 9); err != nil {
		t.Fatalf("解绑非 vendor 成员应成功，得到 %v", err)
	}
	if userRepo.promoted != 0 {
		t.Error("解绑不应触碰用户角色")
	}
}
