package service

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"

	"golang.org/x/sync/singleflight"
)

// workspaceCacheTTL 是工作区与授权的进程内缓存时长。
//
// 取 30s 而非更长：站长改权限档或停用工作区后，最迟 30s 生效，
// 对运营足够及时；同时把每请求一次的 DB 读压到可忽略。
// 走进程内而非 Redis：数据量只有几十条，且鉴权链路要求极低延迟。
const workspaceCacheTTL = 30 * time.Second

type cachedWorkspace struct {
	workspace *Workspace
	expiresAt time.Time
}

type cachedGrant struct {
	grant     *WorkspaceGroupGrant
	found     bool
	expiresAt time.Time
}

// WorkspaceService 提供鉴权链路所需的工作区解析与作用域校验。
//
// 它是 vendor 权限判定的唯一入口：中间件解析作用域、service 层校验资源归属，
// 都经由这里，以免作用域规则散落各处出现口径不一致。
type WorkspaceService struct {
	repo        WorkspaceRepository
	accountRepo AccountRepository

	mu          sync.RWMutex
	byUser      map[int64]cachedWorkspace
	grants      map[string]cachedGrant
	grantCounts map[int64]cachedGrantCount
	accountIDs  map[int64]cachedAccountIDs
	groupIDs    map[int64]cachedGroupIDs
	loadGroup   singleflight.Group
}

type cachedAccountIDs struct {
	ids       []int64
	expiresAt time.Time
}

// lookupAccountIDs 读取账号白名单缓存。
//
// 返回的切片是副本 —— 调用方可能把它塞进 context 并透传到 SQL 参数，
// 共享底层数组会让并发请求相互污染。
func (s *WorkspaceService) lookupAccountIDs(workspaceID int64) ([]int64, bool) {
	s.mu.RLock()
	cached, ok := s.accountIDs[workspaceID]
	s.mu.RUnlock()
	if !ok || time.Now().After(cached.expiresAt) {
		return nil, false
	}
	out := make([]int64, len(cached.ids))
	copy(out, cached.ids)
	return out, true
}

func (s *WorkspaceService) storeAccountIDs(workspaceID int64, ids []int64) {
	stored := make([]int64, len(ids))
	copy(stored, ids)
	s.mu.Lock()
	s.accountIDs[workspaceID] = cachedAccountIDs{
		ids:       stored,
		expiresAt: time.Now().Add(workspaceCacheTTL),
	}
	s.mu.Unlock()
}

type cachedGrantCount struct {
	count     int
	expiresAt time.Time
}

type cachedGroupIDs struct {
	ids       []int64
	expiresAt time.Time
}

// lookupGrantedGroupIDs 读取已授权分组 ID 缓存。
//
// 与 lookupAccountIDs 同理返回副本：调用方会把它当作 SQL IN 参数透传，
// 共享底层数组会让并发请求相互污染。
func (s *WorkspaceService) lookupGrantedGroupIDs(workspaceID int64) ([]int64, bool) {
	s.mu.RLock()
	cached, ok := s.groupIDs[workspaceID]
	s.mu.RUnlock()
	if !ok || time.Now().After(cached.expiresAt) {
		return nil, false
	}
	out := make([]int64, len(cached.ids))
	copy(out, cached.ids)
	return out, true
}

func (s *WorkspaceService) storeGrantedGroupIDs(workspaceID int64, ids []int64) {
	stored := make([]int64, len(ids))
	copy(stored, ids)
	s.mu.Lock()
	s.groupIDs[workspaceID] = cachedGroupIDs{
		ids:       stored,
		expiresAt: time.Now().Add(workspaceCacheTTL),
	}
	s.mu.Unlock()
}

// NewWorkspaceService 构造工作区服务。
// NewWorkspaceService 构造工作区服务。
//
// accountRepo 仅用于解析用量统计的账号白名单，可为 nil ——
// 此时 AccountIDsForWorkspace 返回错误，调用方须拒绝而非放行。
func NewWorkspaceService(repo WorkspaceRepository, accountRepo AccountRepository) *WorkspaceService {
	return &WorkspaceService{
		repo:        repo,
		accountRepo: accountRepo,
		byUser:      make(map[int64]cachedWorkspace),
		grants:      make(map[string]cachedGrant),
		grantCounts: make(map[int64]cachedGrantCount),
		accountIDs:  make(map[int64]cachedAccountIDs),
		groupIDs:    make(map[int64]cachedGroupIDs),
	}
}

// AccountIDsForWorkspace 返回工作区名下账号 ID，带 TTL 缓存与并发合并。
//
// 空切片是合法结果，表示该工作区尚无账号 —— 调用方须让用量查询返回
// 零结果。取不到时返回错误，绝不返回 nil 让调用方误判为「不受限」。
func (s *WorkspaceService) AccountIDsForWorkspace(ctx context.Context, workspaceID int64) ([]int64, error) {
	if s == nil || s.accountRepo == nil {
		return nil, errWorkspaceAccountRepoMissing
	}
	if workspaceID <= 0 {
		return nil, domain.ErrWorkspaceNotFound
	}

	if cached, ok := s.lookupAccountIDs(workspaceID); ok {
		return cached, nil
	}

	key := "accounts:" + strconv.FormatInt(workspaceID, 10)
	result, err, _ := s.loadGroup.Do(key, func() (any, error) {
		ids, err := s.accountRepo.ListIDsByWorkspace(ctx, workspaceID)
		if err != nil {
			return nil, err
		}
		if ids == nil {
			ids = []int64{}
		}
		s.storeAccountIDs(workspaceID, ids)
		return ids, nil
	})
	if err != nil {
		return nil, err
	}

	ids, _ := result.([]int64)
	return ids, nil
}

// errWorkspaceAccountRepoMissing 内部哨兵：账号仓储未装配。
var errWorkspaceAccountRepoMissing = errors.New("workspace service: account repository not configured")

// ResolveByUserID 返回用户所属工作区，带 TTL 缓存与并发合并。
//
// 未绑定工作区返回 domain.ErrWorkspaceMemberNotFound；
// 工作区被停用返回 domain.ErrWorkspaceDisabled —— 调用方须拒绝放行。
func (s *WorkspaceService) ResolveByUserID(ctx context.Context, userID int64) (*Workspace, error) {
	if cached, ok := s.lookupUser(userID); ok {
		return cached, nil
	}

	key := "user:" + strconv.FormatInt(userID, 10)
	result, err, _ := s.loadGroup.Do(key, func() (any, error) {
		workspace, err := s.repo.GetByUserID(ctx, userID)
		if err != nil {
			return nil, err
		}
		s.storeUser(userID, workspace)
		return workspace, nil
	})
	if err != nil {
		return nil, err
	}

	workspace, _ := result.(*Workspace)
	if !workspace.IsActive() {
		return nil, domain.ErrWorkspaceDisabled
	}
	return workspace, nil
}

func (s *WorkspaceService) lookupUser(userID int64) (*Workspace, bool) {
	s.mu.RLock()
	entry, ok := s.byUser[userID]
	s.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.workspace, true
}

func (s *WorkspaceService) storeUser(userID int64, workspace *Workspace) {
	s.mu.Lock()
	s.byUser[userID] = cachedWorkspace{workspace: workspace, expiresAt: time.Now().Add(workspaceCacheTTL)}
	s.mu.Unlock()
}

// GetEffectiveGrant 返回工作区在指定分组的生效授权。
//
// 未授权或授权已停用，一律返回 domain.ErrWorkspaceScopeViolation
// （对外表现为 404）——避免供应商借错误码区分「无此分组」与「有但没授权我」，
// 从而探测他人资源的存在性。
func (s *WorkspaceService) GetEffectiveGrant(ctx context.Context, workspaceID, groupID int64) (*WorkspaceGroupGrant, error) {
	key := strconv.FormatInt(workspaceID, 10) + ":" + strconv.FormatInt(groupID, 10)

	if grant, found, ok := s.lookupGrant(key); ok {
		if !found || !grant.IsEffective() {
			return nil, domain.ErrWorkspaceScopeViolation
		}
		return grant, nil
	}

	result, err, _ := s.loadGroup.Do("grant:"+key, func() (any, error) {
		grant, err := s.repo.GetGrant(ctx, workspaceID, groupID)
		if errors.Is(err, domain.ErrWorkspaceGrantNotFound) {
			s.storeGrant(key, nil, false)
			return nil, domain.ErrWorkspaceScopeViolation
		}
		if err != nil {
			return nil, err
		}
		s.storeGrant(key, grant, true)
		return grant, nil
	})
	if err != nil {
		return nil, err
	}

	grant, _ := result.(*WorkspaceGroupGrant)
	if !grant.IsEffective() {
		return nil, domain.ErrWorkspaceScopeViolation
	}
	return grant, nil
}

// 计费不经过本服务：结算倍率就是 accounts.rate_multiplier，热路径直接读账号，
// 见 resolveAccountCostRate。工作区只在写入时校验可调区间。

func (s *WorkspaceService) lookupGrant(key string) (*WorkspaceGroupGrant, bool, bool) {
	s.mu.RLock()
	entry, ok := s.grants[key]
	s.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false, false
	}
	return entry.grant, entry.found, true
}

func (s *WorkspaceService) storeGrant(key string, grant *WorkspaceGroupGrant, found bool) {
	s.mu.Lock()
	s.grants[key] = cachedGrant{grant: grant, found: found, expiresAt: time.Now().Add(workspaceCacheTTL)}
	s.mu.Unlock()
}

// ListGrantedGroupIDs 返回该工作区已生效授权的分组 ID 列表。
// 用于列表类查询把结果收窄到已授权分组。
func (s *WorkspaceService) ListGrantedGroupIDs(ctx context.Context, workspaceID int64) ([]int64, error) {
	if ids, ok := s.lookupGrantedGroupIDs(workspaceID); ok {
		return ids, nil
	}
	grants, err := s.repo.ListGrantsByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(grants))
	for _, grant := range grants {
		if grant.IsEffective() {
			ids = append(ids, grant.GroupID)
		}
	}
	s.storeGrantedGroupIDs(workspaceID, ids)
	return ids, nil
}

// IsGroupShared 报告分组是否被多家工作区共享。
// 共享分组的计费字段对供应商锁定，避免一家改价影响其他家结算。
func (s *WorkspaceService) IsGroupShared(ctx context.Context, groupID int64) (bool, error) {
	s.mu.RLock()
	entry, ok := s.grantCounts[groupID]
	s.mu.RUnlock()
	if ok && time.Now().Before(entry.expiresAt) {
		return entry.count > 1, nil
	}

	count, err := s.repo.CountGrantsByGroup(ctx, groupID)
	if err != nil {
		return false, err
	}

	s.mu.Lock()
	s.grantCounts[groupID] = cachedGrantCount{count: count, expiresAt: time.Now().Add(workspaceCacheTTL)}
	s.mu.Unlock()

	return count > 1, nil
}

// InvalidateCache 清空全部进程内缓存，供站长改动工作区配置后立即生效。
func (s *WorkspaceService) InvalidateCache() {
	s.mu.Lock()
	s.byUser = make(map[int64]cachedWorkspace)
	s.grants = make(map[string]cachedGrant)
	s.grantCounts = make(map[int64]cachedGrantCount)
	s.accountIDs = make(map[int64]cachedAccountIDs)
	s.groupIDs = make(map[int64]cachedGroupIDs)
	s.mu.Unlock()
}

// InvalidateAccountIDs 清除账号白名单缓存，须在账号新建、删除或改变
// 所属工作区后调用。
//
// 不等 TTL 自然过期：账号被移出工作区后，若白名单仍含该 ID，
// 原工作区在最长一个 TTL 内仍能看到它的用量 —— 这是越权。
// 传 0 清全部（账号迁移涉及两个工作区时用）。
func (s *WorkspaceService) InvalidateAccountIDs(workspaceID int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if workspaceID <= 0 {
		s.accountIDs = make(map[int64]cachedAccountIDs)
	} else {
		delete(s.accountIDs, workspaceID)
	}
	s.mu.Unlock()
}
