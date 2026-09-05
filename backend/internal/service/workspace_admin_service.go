package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// WorkspaceAdminService 承载站长侧的工作区管理，以及供应商在授权范围内的自助调价。
//
// 它是策略层：校验、上限判定、缓存失效都在这里，仓储层只负责持久化。
// WorkspaceMemberUserReader 是成员校验、角色提升与回显所需的最小用户能力。
//
// 刻意不用整个 UserRepository（40+ 方法）：这里只要按 ID 取一个用户、
// 必要时把角色提升为 vendor，收窄接口让测试替身写得起，
// 也说明这段代码不会顺手动别的用户数据。
type WorkspaceMemberUserReader interface {
	GetByID(ctx context.Context, id int64) (*User, error)
	// PromoteToVendor 把普通用户的角色改为 vendor。
	// 幂等：目标已是 vendor 时应直接成功。
	PromoteToVendor(ctx context.Context, id int64) error
}

type WorkspaceAdminService struct {
	repo      WorkspaceAdminRepository
	readRepo  WorkspaceRepository
	scopeSvc  *WorkspaceService
	groupRepo GroupRepository
	userRepo  WorkspaceMemberUserReader
}

func NewWorkspaceAdminService(
	repo WorkspaceAdminRepository,
	readRepo WorkspaceRepository,
	scopeSvc *WorkspaceService,
	groupRepo GroupRepository,
	userRepo WorkspaceMemberUserReader,
) *WorkspaceAdminService {
	return &WorkspaceAdminService{
		repo:      repo,
		readRepo:  readRepo,
		scopeSvc:  scopeSvc,
		groupRepo: groupRepo,
		userRepo:  userRepo,
	}
}

// CreateWorkspaceInput 描述站长新建工作区的入参。
type CreateWorkspaceInput struct {
	Name        string
	Description string
	Permissions WorkspacePermissions
	// SettlementRateMin/Max 为供应商自设账号倍率的可调区间，nil 表示该端
	// 不设限。两端都为 nil 时供应商完全不可自调，这是新建时的安全默认。
	SettlementRateMin *float64
	SettlementRateMax *float64
}

// List 返回全部工作区，供站长后台列表使用。
func (s *WorkspaceAdminService) List(ctx context.Context) ([]*Workspace, error) {
	return s.readRepo.List(ctx)
}

// Get 返回单个工作区。
func (s *WorkspaceAdminService) Get(ctx context.Context, id int64) (*Workspace, error) {
	return s.readRepo.GetByID(ctx, id)
}

// Create 新建工作区。
//
// 权限档由站长在创建时显式指定；未指定即全 false，
// 该工作区的成员登录后什么都做不了 —— 这是刻意的默认拒绝。
func (s *WorkspaceAdminService) Create(ctx context.Context, input CreateWorkspaceInput) (*Workspace, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, domain.ErrWorkspaceNameRequired
	}
	// 先校验再落库：区间非法时不能留下一个已建好的工作区，
	// 否则站长得先删掉半成品才能重试。
	min, max := normalizeRateBound(input.SettlementRateMin), normalizeRateBound(input.SettlementRateMax)
	if err := validateSettlementRange(min, max); err != nil {
		return nil, err
	}
	ws, err := s.repo.Create(ctx, name, strings.TrimSpace(input.Description), input.Permissions)
	if err != nil {
		return nil, err
	}
	// 区间走 Update 落库，而不是给 repo.Create 加两个参数：Create 的位置参数
	// 已经够长，且区间的三态归一与校验逻辑都在 Update 一侧，复用它可以保证
	// 新建与修改两条路径永远同一套语义。
	if min != nil || max != nil {
		updated, err := s.repo.Update(ctx, ws.ID, WorkspaceUpdateInput{
			SettlementRateMin: min,
			SettlementRateMax: max,
		})
		if err != nil {
			return nil, err
		}
		ws = updated
	}
	s.invalidate()
	return ws, nil
}

// Update 修改工作区。
//
// 改完必须失效缓存：作用域中间件按 TTL 缓存权限档与工作区状态，
// 不失效则站长收走的权限在缓存过期前仍然生效。
func (s *WorkspaceAdminService) Update(
	ctx context.Context,
	id int64,
	input WorkspaceUpdateInput,
) (*Workspace, error) {
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" {
			return nil, domain.ErrWorkspaceNameRequired
		}
		input.Name = &name
	}
	if input.Status != nil {
		if err := validateWorkspaceStatus(*input.Status); err != nil {
			return nil, err
		}
	}
	// 区间要合着现存值一起校验：只改一端时，另一端来自库里的旧值，
	// 单看入参那一端永远是「合法」的，会放过 min > max 这种矛盾区间。
	if input.SettlementRateMin != nil || input.SettlementRateMax != nil {
		current, err := s.readRepo.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		min, max := mergeSettlementRange(current, input)
		if err := validateSettlementRange(min, max); err != nil {
			return nil, err
		}
	}
	ws, err := s.repo.Update(ctx, id, input)
	if err != nil {
		return nil, err
	}
	s.invalidate()
	return ws, nil
}

// Delete 软删除工作区。
func (s *WorkspaceAdminService) Delete(ctx context.Context, id int64) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	s.invalidate()
	return nil
}

func validateWorkspaceStatus(status string) error {
	switch status {
	case domain.WorkspaceStatusActive, domain.WorkspaceStatusDisabled:
		return nil
	default:
		return fmt.Errorf("%w: %q", domain.ErrWorkspaceInvalidStatus, status)
	}
}

// ListMembers 返回工作区成员绑定。
func (s *WorkspaceAdminService) ListMembers(ctx context.Context, workspaceID int64) ([]*WorkspaceMember, error) {
	if _, err := s.readRepo.GetByID(ctx, workspaceID); err != nil {
		return nil, err
	}
	return s.repo.ListMembers(ctx, workspaceID)
}

// WorkspaceMemberDetail 是带用户信息的成员条目。
//
// 绑定表只存 user_id，站长看一列数字认不出人；这里补上邮箱与用户名，
// 同时带上 role 以便前端标出「角色已不是 vendor」的历史绑定。
type WorkspaceMemberDetail struct {
	*WorkspaceMember
	Email    string `json:"email"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// ListMemberDetails 返回带用户信息的成员列表。
//
// 单个用户查询失败不丢弃该行：绑定关系本身是事实，用户查不到时
// 仍列出 user_id，让站长能看到并解绑这条脏数据。
func (s *WorkspaceAdminService) ListMemberDetails(
	ctx context.Context,
	workspaceID int64,
) ([]*WorkspaceMemberDetail, error) {
	members, err := s.ListMembers(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]*WorkspaceMemberDetail, 0, len(members))
	for i := range members {
		detail := &WorkspaceMemberDetail{WorkspaceMember: members[i]}
		if s.userRepo != nil {
			if user, err := s.userRepo.GetByID(ctx, members[i].UserID); err == nil && user != nil {
				detail.Email = user.Email
				detail.Username = user.Username
				detail.Role = user.Role
			}
		}
		out = append(out, detail)
	}
	return out, nil
}

// AddMemberResult 是绑定成员的结果，附带本次是否顺带提升了角色。
//
// 单独建类型而非多返回值：提升是站长需要被告知的副作用
// （一个工作区页的动作改了用户的全局角色），让它在类型上留痕，
// 避免调用方顺手丢弃。
type AddMemberResult struct {
	Member *WorkspaceMember
	// RolePromoted 为 true 表示该用户原为普通用户，本次已被提升为 vendor。
	RolePromoted bool
}

// AddMember 把用户绑定到工作区，必要时先将其提升为 vendor。
//
// 绑定后该用户的作用域立即改变，必须失效缓存。
func (s *WorkspaceAdminService) AddMember(
	ctx context.Context,
	workspaceID, userID int64,
) (*AddMemberResult, error) {
	if _, err := s.readRepo.GetByID(ctx, workspaceID); err != nil {
		return nil, err
	}
	promoted, err := s.ensureVendorRole(ctx, userID)
	if err != nil {
		return nil, err
	}
	member, err := s.repo.AddMember(ctx, workspaceID, userID)
	if err != nil {
		return nil, err
	}
	s.invalidate()
	return &AddMemberResult{Member: member, RolePromoted: promoted}, nil
}

// RemoveMember 解除用户与工作区的绑定。
func (s *WorkspaceAdminService) RemoveMember(ctx context.Context, workspaceID, userID int64) error {
	if err := s.repo.RemoveMember(ctx, workspaceID, userID); err != nil {
		return err
	}
	s.invalidate()
	return nil
}

// ListGrants 返回工作区的全部分组授权，含已停用项。
func (s *WorkspaceAdminService) ListGrants(
	ctx context.Context,
	workspaceID int64,
) ([]*WorkspaceGroupGrant, error) {
	if _, err := s.readRepo.GetByID(ctx, workspaceID); err != nil {
		return nil, err
	}
	return s.readRepo.ListGrantsByWorkspace(ctx, workspaceID)
}

// UpsertGrant 由站长新增或修改分组授权。
//
// 授权只管「哪个分组可见」与「新号套哪个优先级」；结算倍率区间挂在
// 工作区上（UpdateWorkspace），不在此处。
func (s *WorkspaceAdminService) UpsertGrant(
	ctx context.Context,
	input WorkspaceGrantInput,
) (*WorkspaceGroupGrant, error) {
	if _, err := s.readRepo.GetByID(ctx, input.WorkspaceID); err != nil {
		return nil, err
	}
	if err := s.ensureGroupExists(ctx, input.GroupID); err != nil {
		return nil, err
	}
	grant, err := s.repo.UpsertGrant(ctx, input)
	if err != nil {
		return nil, err
	}
	s.invalidate()
	return grant, nil
}

// DeleteGrant 收回分组授权。
func (s *WorkspaceAdminService) DeleteGrant(ctx context.Context, workspaceID, groupID int64) error {
	if err := s.repo.DeleteGrant(ctx, workspaceID, groupID); err != nil {
		return err
	}
	s.invalidate()
	return nil
}

// ensureVendorRole 确认待绑定用户存在，并在必要时把普通用户提升为 vendor。
// 返回值表示本次是否发生了角色提升，供上层回报给站长。
//
// 为什么提升而不是拒绝：站长在成员弹窗里选中某人，意图已经是
// 「让这个人当供应商」。要求先去用户管理改角色再回来重选，
// 是把两张表的实现细节转嫁成操作负担。
//
// admin 例外，必须拒绝：降级站长是不可逆的权限收缩，
// 且可能降掉最后一个 admin 而锁死后台，代价远高于少点一次。
//
// 只在新增绑定时处理，不回溯已在册的成员：历史绑定即便角色不符也
// 无法访问管理端（作用域中间件只认 vendor），把它们改判为错误只会
// 让站长在解绑时也吃到 400。
func (s *WorkspaceAdminService) ensureVendorRole(ctx context.Context, userID int64) (bool, error) {
	// 不为 nil 留后门：userRepo 缺失意味着存在性校验整体失效，
	// 填错 ID 会静默建出悬空绑定。宁可装配期就炸，不可静默放过。
	if s.userRepo == nil {
		return false, fmt.Errorf("workspace admin service: user repository not configured")
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return false, err
	}
	// 接口未禁止 (nil, nil)，真实仓储不会这样返回但测试替身容易漏写。
	// 当作「查不到」而非解引用 —— 空指针会把一次配置失误变成 500。
	if user == nil {
		return false, ErrUserNotFound
	}
	switch user.Role {
	case RoleVendor:
		return false, nil
	case RoleAdmin:
		return false, domain.ErrWorkspaceMemberAdminNotAllowed
	default:
		if err := s.userRepo.PromoteToVendor(ctx, userID); err != nil {
			return false, err
		}
		return true, nil
	}
}

// ensureGroupExists 确认分组存在，避免把授权挂在不存在的分组上。
func (s *WorkspaceAdminService) ensureGroupExists(ctx context.Context, groupID int64) error {
	if s.groupRepo == nil {
		return nil
	}
	if _, err := s.groupRepo.GetByID(ctx, groupID); err != nil {
		return err
	}
	return nil
}

// mergeSettlementRange 把本次入参与库中现值合并成待校验的区间。
//
// 入参那一端用新值，另一端沿用现值；指向 0 视为「清除该端」，
// 合并后即为 nil（不设限），因此不会被 validateSettlementRange 的正数
// 检查误判成非法。
func mergeSettlementRange(current *Workspace, input WorkspaceUpdateInput) (*float64, *float64) {
	min, max := current.SettlementRateMin, current.SettlementRateMax
	if input.SettlementRateMin != nil {
		min = normalizeRateBound(input.SettlementRateMin)
	}
	if input.SettlementRateMax != nil {
		max = normalizeRateBound(input.SettlementRateMax)
	}
	return min, max
}

// normalizeRateBound 把「指向 0」折叠成 nil，即清除该端限制。
func normalizeRateBound(v *float64) *float64 {
	if v == nil || *v == 0 {
		return nil
	}
	return v
}

// validateSettlementRange 校验站长设定的结算倍率区间。
//
// 两端都必须为正：0 或负数会让结算口径失去意义（倍率乘进成本，0 会把
// 金额抹平）。min > max 属于站长自己配置矛盾，同样拒绝 —— 那样的区间
// 没有任何取值能通过，供应商会撞上一个永远填不对的输入框。
func validateSettlementRange(min, max *float64) error {
	if min != nil && *min <= 0 {
		return domain.ErrWorkspaceInvalidCostRate
	}
	if max != nil && *max <= 0 {
		return domain.ErrWorkspaceInvalidCostRate
	}
	if min != nil && max != nil && *min > *max {
		return domain.ErrWorkspaceInvalidSettlementRange
	}
	return nil
}

// invalidate 清空作用域缓存。
//
// 任何工作区、成员或授权的变更都必须走这里：中间件与计费都读缓存，
// 漏掉一次就意味着旧权限或旧结算价在 TTL 内继续生效。
func (s *WorkspaceAdminService) invalidate() {
	if s.scopeSvc != nil {
		s.scopeSvc.InvalidateCache()
	}
}
