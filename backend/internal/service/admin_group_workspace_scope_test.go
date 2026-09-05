package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// stubGroupScopeCache 是 workspaceAccountScopeInvalidator 的最小实现。
//
// 只关心 ListGrantedGroupIDs：本文件断言的是分组读写的作用域收窄，
// 账号白名单失效与此无关。
type stubGroupScopeCache struct {
	granted []int64
	err     error
	calls   int
	// shared 让 IsGroupShared 返回固定值；本文件不断言共享判定，
	// 保持零值（不共享）即可，字段留在这里供同包其他测试复用。
	shared bool
}

func (s *stubGroupScopeCache) InvalidateAccountIDs(int64) {}

// IsGroupShared 报告分组是否被授权给多个工作区。
//
// 共享判定决定计费字段是否锁定，与本文件断言的分组读写收窄无关，
// 因此直接回放预设值。
func (s *stubGroupScopeCache) IsGroupShared(context.Context, int64) (bool, error) {
	return s.shared, nil
}

func (s *stubGroupScopeCache) ListGrantedGroupIDs(context.Context, int64) ([]int64, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.granted, nil
}

// vendorGroupCtx 构造一个权限档全开的 vendor 作用域上下文。
//
// 权限故意全开：本文件断言的是「作用域收窄」，
// 不能让断言侥幸依赖某个权限档恰好关闭。
func vendorGroupCtx(workspaceID int64) context.Context {
	return WithScope(context.Background(), VendorScope(workspaceID, WorkspacePermissions{
		AccountManage: true, GroupOps: true, GroupBilling: true,
		ProxyManage: true, MonitorView: true,
	}))
}

// adminGroupCtx 构造站长的不受限作用域上下文。
//
// 必须显式写入 AdminScope：零值 Scope 的 Unrestricted 为 false，
// 裸 context.Background() 会被判成受限的 vendor。
func adminGroupCtx() context.Context {
	return WithScope(context.Background(), AdminScope())
}

// TestFilterGroupsByScopeDropsUngranted 锁定未授权分组不出现在列表里。
//
// 这是验收发现的越权：工作区只授权 1 个分组，接口却返回全站 17 个。
// 分组行本身就带容量、费率与用量，泄露一行即泄露一家上游的商业参数。
func TestFilterGroupsByScopeDropsUngranted(t *testing.T) {
	cache := &stubGroupScopeCache{granted: []int64{3}}
	svc := &adminServiceImpl{workspaceScopeCache: cache}

	all := []Group{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}}
	got, err := svc.filterGroupsByScope(vendorGroupCtx(7), all)
	if err != nil {
		t.Fatalf("过滤失败: %v", err)
	}
	if len(got) != 1 || got[0].ID != 3 {
		t.Fatalf("只应留下已授权的 3 号分组，实得 %+v", got)
	}
}

// TestFilterGroupsByScopeAdminUnrestricted 锁定站长视角逐字不变。
//
// 工作区机制的硬约束：admin 必须与引入之前完全一致。
// 因此不查授权表（calls 为 0），且原样返回同一切片。
func TestFilterGroupsByScopeAdminUnrestricted(t *testing.T) {
	cache := &stubGroupScopeCache{granted: []int64{3}}
	svc := &adminServiceImpl{workspaceScopeCache: cache}

	all := []Group{{ID: 1}, {ID: 2}}
	got, err := svc.filterGroupsByScope(adminGroupCtx(), all)
	if err != nil {
		t.Fatalf("站长视角不应出错: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("站长应看到全部分组，实得 %d 条", len(got))
	}
	if cache.calls != 0 {
		t.Errorf("站长视角不应查询授权表，实际查了 %d 次", cache.calls)
	}
}

func TestFilterGroupsByScopeMarksSharedBillingLockedForVendor(t *testing.T) {
	cache := &stubGroupScopeCache{granted: []int64{3}, shared: true}
	svc := &adminServiceImpl{workspaceScopeCache: cache}

	got, err := svc.filterGroupsByScope(vendorGroupCtx(7), []Group{{ID: 3}})
	if err != nil {
		t.Fatalf("过滤失败: %v", err)
	}
	if len(got) != 1 || !got[0].BillingLocked {
		t.Fatalf("共享分组必须向 vendor 标记计费锁，实得 %+v", got)
	}
}

// TestGrantedGroupIDsEmptyIsNotUnrestricted 锁定「零授权」不等于「不受限」。
//
// nil map 表示站长不受限，空 map 表示 vendor 一个都看不到。
// 若把空切片折叠成 nil，未授权 vendor 会拿到全站分组 —— 与验收的越权同源。
func TestGrantedGroupIDsEmptyIsNotUnrestricted(t *testing.T) {
	cache := &stubGroupScopeCache{granted: nil}
	svc := &adminServiceImpl{workspaceScopeCache: cache}

	granted, err := svc.grantedGroupIDs(vendorGroupCtx(7))
	if err != nil {
		t.Fatalf("查询授权失败: %v", err)
	}
	if granted == nil {
		t.Fatal("零授权必须返回空 map，返回 nil 会被上层当作站长不受限")
	}
	if len(granted) != 0 {
		t.Fatalf("零授权应为空集，实得 %d 项", len(granted))
	}
}

// TestRequireGroupAccessDeniesUngranted 锁定按 ID 读写未授权分组被拒。
//
// 越权须返回 ErrWorkspaceScopeViolation（对外 404）而非 403：
// 错误码差异本身就是一个存在性探针。
func TestRequireGroupAccessDeniesUngranted(t *testing.T) {
	svc := &adminServiceImpl{workspaceScopeCache: &stubGroupScopeCache{granted: []int64{3}}}
	ctx := vendorGroupCtx(7)

	if err := svc.requireGroupAccess(ctx, 3); err != nil {
		t.Errorf("已授权分组应放行，实得 %v", err)
	}
	err := svc.requireGroupAccess(ctx, 4)
	if !errors.Is(err, domain.ErrWorkspaceScopeViolation) {
		t.Errorf("未授权分组应返回 ErrWorkspaceScopeViolation，实得 %v", err)
	}
}

// TestGrantedGroupIDsDeniesWhenScopeCacheMissing 锁定装配疏漏不静默放行。
//
// 若工作区服务未注入就回落成「不受限」，一次 wire 疏漏即把全站分组
// 交给所有 vendor。宁可全员报错，不可静默越权。
func TestGrantedGroupIDsDeniesWhenScopeCacheMissing(t *testing.T) {
	svc := &adminServiceImpl{}

	_, err := svc.grantedGroupIDs(vendorGroupCtx(7))
	if !errors.Is(err, domain.ErrWorkspaceScopeViolation) {
		t.Errorf("缺少工作区服务时应拒绝，实得 %v", err)
	}
}

// TestGrantedGroupIDsPropagatesQueryError 锁定授权查询失败不降级为放行。
//
// 与计费路径的取舍相反：计费发生在请求已转发之后，查询失败必须回退
// 以免多收或漏计；而这里是准入判定，失败只能拒绝。
func TestGrantedGroupIDsPropagatesQueryError(t *testing.T) {
	boom := errors.New("授权表不可达")
	svc := &adminServiceImpl{workspaceScopeCache: &stubGroupScopeCache{err: boom}}

	if _, err := svc.grantedGroupIDs(vendorGroupCtx(7)); !errors.Is(err, boom) {
		t.Errorf("查询失败应向上传递，实得 %v", err)
	}
}

// TestFilterGroupIDsByScopeTrimsSummaries 锁定汇总端点按授权裁剪。
//
// 用量与容量汇总走独立 service，不经本 service 取数，
// 只能在拿到结果后剔除未授权分组 —— 验收里「暴露容量、费率和用量」即此处。
func TestFilterGroupIDsByScopeTrimsSummaries(t *testing.T) {
	svc := &adminServiceImpl{workspaceScopeCache: &stubGroupScopeCache{granted: []int64{2, 5}}}

	got, err := svc.FilterGroupIDsByScope(vendorGroupCtx(7), []int64{1, 2, 3, 5, 8})
	if err != nil {
		t.Fatalf("裁剪失败: %v", err)
	}
	if len(got) != 2 || got[0] != 2 || got[1] != 5 {
		t.Fatalf("只应留下已授权的 2、5，实得 %v", got)
	}
}
