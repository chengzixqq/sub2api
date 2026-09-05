package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// 新建资源的归属值。
//
// 这一层曾把站长新建的账号/代理落成 workspace_id=0，而迁移 192 给
// accounts/proxies 加了 NOT NULL DEFAULT 1 与外键指向 workspaces(id)，
// 预置行只有 id=1。于是站长的每一次新建都撞 accounts_workspace_id_fkey
// 失败 —— 症状是「管理员无法创建账号」，看着像权限问题，实为外键违规。
//
// 本文件锁的就是这个口径：站长落 DefaultWorkspaceID，且绝不落 0。

// TestResolveNewAccountWorkspaceIDAdminGetsDefaultWorkspace 锁定站长归属值。
//
// 断言不写成 != 0 而是 == DefaultWorkspaceID：只要求非 0 的话，
// 落成任意无对应行的正数同样撞外键，测试却会放过。
func TestResolveNewAccountWorkspaceIDAdminGetsDefaultWorkspace(t *testing.T) {
	ctx := WithScope(context.Background(), AdminScope())

	if got := resolveNewAccountWorkspaceID(ctx); got != domain.DefaultWorkspaceID {
		t.Errorf("站长新建资源应归属工作区 %d，得到 %d", domain.DefaultWorkspaceID, got)
	}
}

// TestResolveNewAccountWorkspaceIDVendorGetsOwnWorkspace 锁定供应商落自己工作区。
//
// 归属绝不取自请求体，否则供应商可伪造字段把账号塞进别人的工作区。
func TestResolveNewAccountWorkspaceIDVendorGetsOwnWorkspace(t *testing.T) {
	ctx := WithScope(context.Background(), VendorScope(7, WorkspacePermissions{}))

	if got := resolveNewAccountWorkspaceID(ctx); got != 7 {
		t.Errorf("供应商新建资源应归属工作区 7，得到 %d", got)
	}
}

// TestResolveNewAccountWorkspaceIDNoScopeFallsBackToDefault 锁定无作用域的回落。
//
// 这里刻意不按「缺作用域即拒绝」处理：本函数只决定归属值，不做准入判定，
// 准入早在中间件与路由白名单处结束。内部编排（导入、OAuth 回调后的补建）
// 可能不带作用域，回落成 0 会撞外键，回落成 DefaultWorkspaceID 才写得进去。
func TestResolveNewAccountWorkspaceIDNoScopeFallsBackToDefault(t *testing.T) {
	if got := resolveNewAccountWorkspaceID(context.Background()); got != domain.DefaultWorkspaceID {
		t.Errorf("无作用域时应回落到 %d，得到 %d", domain.DefaultWorkspaceID, got)
	}
}

// TestResolveNewAccountWorkspaceIDNeverReturnsZero 直接锁死 0。
//
// 前三条各锁一个分支，这条锁的是「任何输入都不得产出 0」这个不变量 ——
// 将来加分支时，漏掉归属值的那条会在这里被拦下。
func TestResolveNewAccountWorkspaceIDNeverReturnsZero(t *testing.T) {
	cases := map[string]context.Context{
		"无作用域":       context.Background(),
		"站长":         WithScope(context.Background(), AdminScope()),
		"供应商":        WithScope(context.Background(), VendorScope(7, WorkspacePermissions{})),
		"零值作用域":      WithScope(context.Background(), Scope{}),
		"作用域带负工作区ID": WithScope(context.Background(), VendorScope(-1, WorkspacePermissions{})),
	}

	for name, ctx := range cases {
		if got := resolveNewAccountWorkspaceID(ctx); got <= 0 {
			t.Errorf("%s: 归属值为 %d，会撞 workspace_id 外键", name, got)
		}
	}
}

// TestOwnsWorkspaceIDRejectsNonPositive 锁定归属校验不放行非正值。
//
// 站长直管资源现在归属 1 而非 0，所以 0 只可能来自迁移前的残留行或零值
// Scope。零值 Scope 的 WorkspaceID 也是 0，若把 0 当可匹配，一个漏挂中间件
// 的请求就会与全部残留行相等而放行 —— 这正是最坏的失效方向。
func TestOwnsWorkspaceIDRejectsNonPositive(t *testing.T) {
	zeroScope := Scope{}
	if zeroScope.OwnsWorkspaceID(0) {
		t.Error("零值作用域不得匹配 workspace_id=0 的残留行")
	}

	vendor := VendorScope(7, WorkspacePermissions{})
	if vendor.OwnsWorkspaceID(0) {
		t.Error("供应商不得匹配 workspace_id=0")
	}
	if vendor.OwnsWorkspaceID(-1) {
		t.Error("供应商不得匹配负 workspace_id")
	}
	if !vendor.OwnsWorkspaceID(7) {
		t.Error("供应商应匹配自己的工作区")
	}
	if vendor.OwnsWorkspaceID(domain.DefaultWorkspaceID) {
		t.Error("供应商不得匹配站长直管工作区")
	}

	// 站长不受限：连残留行也照样放行，站长视角必须逐字不变。
	if !AdminScope().OwnsWorkspaceID(0) {
		t.Error("站长应对 workspace_id=0 的残留行放行")
	}
}

// TestOwnsWorkspaceDelegatesToValueVersion 锁定指针版与值版同口径。
//
// 两版曾各写一遍判定，指针版漏了非正值检查：一个 WorkspaceID=0 的
// 作用域会与取值为 0 的资源相等而放行。现在指针版转调值版，
// 这条守的是将来有人把它改回独立实现。
func TestOwnsWorkspaceDelegatesToValueVersion(t *testing.T) {
	zero := int64(0)
	own := int64(7)
	other := int64(8)

	vendor := VendorScope(7, WorkspacePermissions{})
	if vendor.OwnsWorkspace(&zero) {
		t.Error("指针版不得放行 0")
	}
	if vendor.OwnsWorkspace(nil) {
		t.Error("指针版不得放行 nil（不归属任何工作区）")
	}
	if vendor.OwnsWorkspace(&other) {
		t.Error("指针版不得放行别家工作区")
	}
	if !vendor.OwnsWorkspace(&own) {
		t.Error("指针版应放行自己的工作区")
	}

	if (Scope{}).OwnsWorkspace(&zero) {
		t.Error("零值作用域不得借 0 == 0 放行")
	}
}
