package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

// countScopeGroupRepo 只实现聚合计数一个方法。
//
// 记录入参 ids：本文件要断言的不只是「计数被改写」，
// 还包括「只查了自己该查的那些分组」。
type countScopeGroupRepo struct {
	// 嵌入 noop 骨架补齐接口其余方法：那些方法一旦被调到就 panic，
	// 因此本文件的绿灯同时证明了「只走聚合这一条路」。
	groupRepoNoop

	counts map[int64]GroupAccountCounts
	err    error
	gotIDs []int64
	gotWS  int64
	calls  int
}

func (r *countScopeGroupRepo) LoadAccountCountsScoped(_ context.Context, ids []int64, workspaceID int64) (map[int64]GroupAccountCounts, error) {
	r.calls++
	r.gotIDs = ids
	r.gotWS = workspaceID
	if r.err != nil {
		return nil, r.err
	}
	return r.counts, nil
}

// TestRescopeGroupAccountCountsOverwritesGlobalCounts 锁定共享分组计数按工作区改写。
//
// 分组列表原本带的是全站计数。分组同时授权给 A、B 时，
// A 看到「10 个账号」就等于知道 B 往里放了多少号 —— 池子规模是商业信息。
func TestRescopeGroupAccountCountsOverwritesGlobalCounts(t *testing.T) {
	repo := &countScopeGroupRepo{counts: map[int64]GroupAccountCounts{
		3: {Total: 2, Active: 1, RateLimited: 1},
	}}
	svc := &adminServiceImpl{groupRepo: repo}

	// 入参是全站视角的计数：10 个号里只有 2 个属于本工作区。
	groups := []Group{{ID: 3, AccountCount: 10, ActiveAccountCount: 8, RateLimitedAccountCount: 2}}
	if err := svc.rescopeGroupAccountCounts(vendorGroupCtx(7), groups); err != nil {
		t.Fatalf("改写计数失败: %v", err)
	}

	g := groups[0]
	if g.AccountCount != 2 || g.ActiveAccountCount != 1 || g.RateLimitedAccountCount != 1 {
		t.Fatalf("计数应被改写为本工作区口径，实得 total=%d active=%d limited=%d",
			g.AccountCount, g.ActiveAccountCount, g.RateLimitedAccountCount)
	}
	// 工作区 ID 必须真的传到仓储层：漏传会退化成全站聚合，
	// 而返回值恰好只含本区数据时，上面的断言仍会通过。
	if repo.gotWS != 7 {
		t.Errorf("应按工作区 7 聚合，实得 %d", repo.gotWS)
	}
}

// TestRescopeGroupAccountCountsZerosMissingGroups 锁定「查不到就归零」。
//
// 聚合结果缺键意味着该分组在本工作区里一个账号都没有。
// 若保留原值，缺键反而成了全站计数的直通车 —— 泄露路径与不改写等价。
func TestRescopeGroupAccountCountsZerosMissingGroups(t *testing.T) {
	repo := &countScopeGroupRepo{counts: map[int64]GroupAccountCounts{}}
	svc := &adminServiceImpl{groupRepo: repo}

	groups := []Group{{ID: 3, AccountCount: 10, ActiveAccountCount: 8, RateLimitedAccountCount: 2}}
	if err := svc.rescopeGroupAccountCounts(vendorGroupCtx(7), groups); err != nil {
		t.Fatalf("改写计数失败: %v", err)
	}

	g := groups[0]
	if g.AccountCount != 0 || g.ActiveAccountCount != 0 || g.RateLimitedAccountCount != 0 {
		t.Fatalf("缺键分组应归零，实得 total=%d active=%d limited=%d",
			g.AccountCount, g.ActiveAccountCount, g.RateLimitedAccountCount)
	}
}

// TestRescopeGroupAccountCountsAdminSkipsQuery 锁定站长计数逐字不变。
//
// admin 看的就是全站计数，既不该改写、也不该多花一次聚合查询。
func TestRescopeGroupAccountCountsAdminSkipsQuery(t *testing.T) {
	repo := &countScopeGroupRepo{counts: map[int64]GroupAccountCounts{3: {Total: 2}}}
	svc := &adminServiceImpl{groupRepo: repo}

	groups := []Group{{ID: 3, AccountCount: 10}}
	if err := svc.rescopeGroupAccountCounts(WithScope(context.Background(), AdminScope()), groups); err != nil {
		t.Fatalf("站长视角不应出错: %v", err)
	}
	if groups[0].AccountCount != 10 {
		t.Errorf("站长计数须保持全站口径 10，实得 %d", groups[0].AccountCount)
	}
	if repo.calls != 0 {
		t.Errorf("站长视角不应触发聚合查询，实际查了 %d 次", repo.calls)
	}
}

// TestRescopeGroupAccountCountsPropagatesError 锁定聚合失败不静默留下全站计数。
//
// 与计费路径的取舍相反：这里失败若回退成原值，泄露的就是全站规模。
// 宁可整个列表报错。
func TestRescopeGroupAccountCountsPropagatesError(t *testing.T) {
	boom := errors.New("聚合查询不可达")
	svc := &adminServiceImpl{groupRepo: &countScopeGroupRepo{err: boom}}

	groups := []Group{{ID: 3, AccountCount: 10}}
	if err := svc.rescopeGroupAccountCounts(vendorGroupCtx(7), groups); !errors.Is(err, boom) {
		t.Errorf("聚合失败应向上传递，实得 %v", err)
	}
}

// TestGroupAccountCountsIdleIsRemainderAndNeverNegative 锁定 idle 的减法口径。
//
// 四个桶两两不相交，idle 是 total 扣掉其余三桶后的余数，这样
// account_count 恒等于四项之和；口径不一致的输入（理论上不会出现）
// 也不能算出负数让客户端画出负宽度的色格。
func TestGroupAccountCountsIdleIsRemainderAndNeverNegative(t *testing.T) {
	c := GroupAccountCounts{Total: 10, Active: 3, RateLimited: 2, Error: 1}
	if got := c.Idle(); got != 4 {
		t.Fatalf("idle 应为 10-3-2-1=4，实得 %d", got)
	}
	if got := (GroupAccountCounts{Total: 1, Active: 2}).Idle(); got != 0 {
		t.Fatalf("口径不一致时 idle 应取 0，实得 %d", got)
	}

	g := Group{ID: 3}
	g.ApplyAccountCounts(c)
	if g.AccountCount != 10 || g.ActiveAccountCount != 3 || g.RateLimitedAccountCount != 2 ||
		g.ErrorAccountCount != 1 || g.IdleAccountCount != 4 {
		t.Fatalf("ApplyAccountCounts 应一次写入五项，实得 %+v", g)
	}
}

// TestRescopeGroupAccountCountsOverwritesErrorAndIdle 锁定新增的两个桶同样按工作区重算。
//
// 只改写老三项而漏掉 error / idle，vendor 就能用全站的错误数、停用数
// 反推别家的账号规模，与不重算等价。
func TestRescopeGroupAccountCountsOverwritesErrorAndIdle(t *testing.T) {
	repo := &countScopeGroupRepo{counts: map[int64]GroupAccountCounts{
		3: {Total: 5, Active: 1, RateLimited: 1, Error: 1},
	}}
	svc := &adminServiceImpl{groupRepo: repo}

	groups := []Group{
		{ID: 3, AccountCount: 30, ActiveAccountCount: 9, RateLimitedAccountCount: 3, ErrorAccountCount: 9, IdleAccountCount: 9},
		{ID: 4, AccountCount: 30, ErrorAccountCount: 9, IdleAccountCount: 9},
	}
	if err := svc.rescopeGroupAccountCounts(vendorGroupCtx(7), groups); err != nil {
		t.Fatalf("改写计数失败: %v", err)
	}
	if groups[0].ErrorAccountCount != 1 || groups[0].IdleAccountCount != 2 {
		t.Fatalf("error / idle 应改写为本工作区口径 1 / 2，实得 %d / %d",
			groups[0].ErrorAccountCount, groups[0].IdleAccountCount)
	}
	if groups[1].ErrorAccountCount != 0 || groups[1].IdleAccountCount != 0 || groups[1].AccountCount != 0 {
		t.Fatalf("缺键分组五项都应归零，实得 %+v", groups[1])
	}

	adminRepo := &countScopeGroupRepo{counts: map[int64]GroupAccountCounts{3: {Total: 1}}}
	adminSvc := &adminServiceImpl{groupRepo: adminRepo}
	adminGroups := []Group{{ID: 3, AccountCount: 30, ErrorAccountCount: 9, IdleAccountCount: 9}}
	if err := adminSvc.rescopeGroupAccountCounts(adminGroupCtx(), adminGroups); err != nil {
		t.Fatalf("站长视角不应出错: %v", err)
	}
	if adminRepo.calls != 0 || adminGroups[0].ErrorAccountCount != 9 || adminGroups[0].IdleAccountCount != 9 {
		t.Fatalf("站长视角应不查询且保持全站值，calls=%d group=%+v", adminRepo.calls, adminGroups[0])
	}
}

// allGroupsCountRepo 供 /groups/all 三条服务路径使用：每次返回一份全站计数的分组副本。
type allGroupsCountRepo struct {
	countScopeGroupRepo
	groups []Group
}

func (r *allGroupsCountRepo) cloneGroups() []Group {
	return append([]Group(nil), r.groups...)
}

func (r *allGroupsCountRepo) ListActive(context.Context) ([]Group, error) {
	return r.cloneGroups(), nil
}

func (r *allGroupsCountRepo) ListActiveByPlatform(context.Context, string) ([]Group, error) {
	return r.cloneGroups(), nil
}

func (r *allGroupsCountRepo) ListWithFilters(context.Context, pagination.PaginationParams, string, string, string, *bool) ([]Group, *pagination.PaginationResult, error) {
	groups := r.cloneGroups()
	return groups, &pagination.PaginationResult{Total: int64(len(groups))}, nil
}

func newAllGroupsCountRepo(err error) *allGroupsCountRepo {
	return &allGroupsCountRepo{
		countScopeGroupRepo: countScopeGroupRepo{
			counts: map[int64]GroupAccountCounts{3: {Total: 4, Active: 1, RateLimited: 1, Error: 1}},
			err:    err,
		},
		groups: []Group{
			{ID: 3, Platform: PlatformAnthropic, AccountCount: 10, ActiveAccountCount: 8, RateLimitedAccountCount: 1, ErrorAccountCount: 1},
			{ID: 4, Platform: PlatformAnthropic, AccountCount: 6, ActiveAccountCount: 6},
		},
	}
}

// TestGetAllGroupsPathsRescopeCountsForVendor 锁定 /groups/all 的三条服务路径按工作区重算计数。
//
// 这三条路径此前只做行级过滤，vendor 在下拉框里就能看到共享分组的全站账号数。
func TestGetAllGroupsPathsRescopeCountsForVendor(t *testing.T) {
	paths := map[string]func(*adminServiceImpl, context.Context) ([]Group, error){
		"GetAllGroups": func(s *adminServiceImpl, ctx context.Context) ([]Group, error) { return s.GetAllGroups(ctx) },
		"GetAllGroupsByPlatform": func(s *adminServiceImpl, ctx context.Context) ([]Group, error) {
			return s.GetAllGroupsByPlatform(ctx, PlatformAnthropic)
		},
		"GetAllGroupsIncludingInactive": func(s *adminServiceImpl, ctx context.Context) ([]Group, error) {
			return s.GetAllGroupsIncludingInactive(ctx)
		},
	}
	for name, call := range paths {
		t.Run(name+"/vendor", func(t *testing.T) {
			repo := newAllGroupsCountRepo(nil)
			svc := &adminServiceImpl{groupRepo: repo, workspaceScopeCache: &stubGroupScopeCache{granted: []int64{3}}}
			got, err := call(svc, vendorGroupCtx(7))
			if err != nil {
				t.Fatalf("vendor 读取失败: %v", err)
			}
			if len(got) != 1 || got[0].ID != 3 {
				t.Fatalf("只应返回已授权的 3 号分组，实得 %+v", got)
			}
			g := got[0]
			if g.AccountCount != 4 || g.ActiveAccountCount != 1 || g.RateLimitedAccountCount != 1 ||
				g.ErrorAccountCount != 1 || g.IdleAccountCount != 1 {
				t.Fatalf("计数应为本工作区口径 4/1/1/1/1，实得 %+v", g)
			}
			if len(repo.gotIDs) != 1 || repo.gotIDs[0] != 3 || repo.gotWS != 7 {
				t.Fatalf("应只按工作区 7 聚合 3 号分组，实得 ids=%v ws=%d", repo.gotIDs, repo.gotWS)
			}
		})
		t.Run(name+"/admin", func(t *testing.T) {
			repo := newAllGroupsCountRepo(nil)
			svc := &adminServiceImpl{groupRepo: repo, workspaceScopeCache: &stubGroupScopeCache{granted: []int64{3}}}
			got, err := call(svc, adminGroupCtx())
			if err != nil {
				t.Fatalf("站长读取失败: %v", err)
			}
			if len(got) != 2 || got[0].AccountCount != 10 || got[0].ErrorAccountCount != 1 {
				t.Fatalf("站长应看到全部分组与全站计数，实得 %+v", got)
			}
			if repo.calls != 0 {
				t.Fatalf("站长视角不应触发聚合查询，实际查了 %d 次", repo.calls)
			}
		})
		t.Run(name+"/rescope-error", func(t *testing.T) {
			boom := errors.New("聚合查询不可达")
			repo := newAllGroupsCountRepo(boom)
			svc := &adminServiceImpl{groupRepo: repo, workspaceScopeCache: &stubGroupScopeCache{granted: []int64{3}}}
			got, err := call(svc, vendorGroupCtx(7))
			if !errors.Is(err, boom) || got != nil {
				t.Fatalf("聚合失败应整体报错、不带全站计数返回，实得 groups=%+v err=%v", got, err)
			}
		})
	}
}

// updateGroupCountRepo 让 UpdateGroup 读到全站计数，并记录是否写过库。
type updateGroupCountRepo struct {
	countScopeGroupRepo
	group   Group
	updated *Group
}

func (r *updateGroupCountRepo) GetByID(context.Context, int64) (*Group, error) {
	cloned := r.group
	return &cloned, nil
}

func (r *updateGroupCountRepo) Update(_ context.Context, group *Group) error {
	r.updated = group
	return nil
}

func newUpdateGroupCountRepo(err error) *updateGroupCountRepo {
	return &updateGroupCountRepo{
		countScopeGroupRepo: countScopeGroupRepo{
			counts: map[int64]GroupAccountCounts{7: {Total: 3, Active: 1, Error: 1}},
			err:    err,
		},
		group: Group{
			ID: 7, Name: "g", Platform: PlatformAnthropic, Status: StatusActive,
			AccountCount: 20, ActiveAccountCount: 15, RateLimitedAccountCount: 2, ErrorAccountCount: 2, IdleAccountCount: 1,
		},
	}
}

// TestUpdateGroupVendorResponseUsesWorkspaceCounts 锁定 PUT 响应对 vendor 也按工作区计数。
//
// 编辑响应直接回显 GetByID 读到的分组，此前带的是全站计数。
func TestUpdateGroupVendorResponseUsesWorkspaceCounts(t *testing.T) {
	newSvc := func(repo *updateGroupCountRepo) *adminServiceImpl {
		return &adminServiceImpl{groupRepo: repo, workspaceScopeCache: &stubGroupScopeCache{granted: []int64{7}}}
	}

	repo := newUpdateGroupCountRepo(nil)
	got, err := newSvc(repo).UpdateGroup(vendorGroupCtx(7), 7, &UpdateGroupInput{Name: "x"})
	if err != nil {
		t.Fatalf("vendor 编辑失败: %v", err)
	}
	if got.AccountCount != 3 || got.ActiveAccountCount != 1 || got.RateLimitedAccountCount != 0 ||
		got.ErrorAccountCount != 1 || got.IdleAccountCount != 1 {
		t.Fatalf("PUT 响应应为本工作区计数 3/1/0/1/1，实得 %+v", got)
	}
	if repo.updated == nil {
		t.Fatal("正常编辑应写库")
	}

	boom := errors.New("聚合查询不可达")
	failing := newUpdateGroupCountRepo(boom)
	if _, err := newSvc(failing).UpdateGroup(vendorGroupCtx(7), 7, &UpdateGroupInput{Name: "x"}); !errors.Is(err, boom) {
		t.Fatalf("重算失败应返回该错误，实得 %v", err)
	}
	if failing.updated != nil {
		t.Fatal("重算失败必须在写库之前返回")
	}

	adminRepo := newUpdateGroupCountRepo(nil)
	adminGot, err := newSvc(adminRepo).UpdateGroup(adminGroupCtx(), 7, &UpdateGroupInput{Name: "x"})
	if err != nil {
		t.Fatalf("站长编辑失败: %v", err)
	}
	if adminGot.AccountCount != 20 || adminGot.ErrorAccountCount != 2 || adminGot.IdleAccountCount != 1 || adminRepo.calls != 0 {
		t.Fatalf("站长应保持全站计数且不查询，实得 %+v calls=%d", adminGot, adminRepo.calls)
	}
}

// TestRequireGroupIDsGrantedRejectsUngranted 锁定账号不得绑定未授权分组。
//
// 绑定是把账号送进某个分组的调度池。绑到未授权分组，
// 等于把自家号塞进别人的池子，或反过来借别人的分组承接流量。
func TestRequireGroupIDsGrantedRejectsUngranted(t *testing.T) {
	svc := &adminServiceImpl{workspaceScopeCache: &stubGroupScopeCache{granted: []int64{3}}}
	ctx := vendorGroupCtx(7)

	if err := svc.requireGroupIDsGranted(ctx, []int64{3}); err != nil {
		t.Errorf("已授权分组应放行，实得 %v", err)
	}
	// 混入一个未授权分组：部分合法不等于整体合法。
	err := svc.requireGroupIDsGranted(ctx, []int64{3, 4})
	if !errors.Is(err, domain.ErrWorkspaceScopeViolation) {
		t.Errorf("含未授权分组应返回 ErrWorkspaceScopeViolation，实得 %v", err)
	}
}

// TestRequireGroupIDsGrantedAdminUnrestricted 锁定站长可跨区绑定。
//
// 「账号↔分组关联只有站长能改」是本方案的硬不变量之一，
// 前半句是拦住 vendor，后半句是站长必须畅通。
func TestRequireGroupIDsGrantedAdminUnrestricted(t *testing.T) {
	cache := &stubGroupScopeCache{granted: []int64{3}}
	svc := &adminServiceImpl{workspaceScopeCache: cache}

	if err := svc.requireGroupIDsGranted(WithScope(context.Background(), AdminScope()), []int64{4, 99}); err != nil {
		t.Fatalf("站长应可绑定任意分组，实得 %v", err)
	}
	if cache.calls != 0 {
		t.Errorf("站长视角不应查询授权表，实际查了 %d 次", cache.calls)
	}
}

// TestRequireGroupIDsGrantedEmptyIsNoop 锁定空绑定列表不误报。
//
// 清空账号的分组绑定是合法操作（把号从池子里摘出来），
// 不该因为「没有任何分组在授权表里」而被判越权。
func TestRequireGroupIDsGrantedEmptyIsNoop(t *testing.T) {
	svc := &adminServiceImpl{workspaceScopeCache: &stubGroupScopeCache{granted: nil}}

	if err := svc.requireGroupIDsGranted(vendorGroupCtx(7), nil); err != nil {
		t.Errorf("空绑定列表应放行，实得 %v", err)
	}
}
