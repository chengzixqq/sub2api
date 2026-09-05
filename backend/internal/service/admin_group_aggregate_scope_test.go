package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
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
