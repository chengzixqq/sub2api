package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

func ratePtr(v float64) *float64 { return &v }

// TestAccountFieldScopeVendorRateWithinRange 锁定区间内自设倍率可通过。
//
// 账号倍率就是结算倍率：供应商在站长划定的区间内自主定价。
// 这是它唯一能自主设定的商务参数，必须放行，否则委派管理无从落地。
func TestAccountFieldScopeVendorRateWithinRange(t *testing.T) {
	ptr := ratePtr(0.055)
	ctx := WithScope(context.Background(), VendorScopeWithSettlementRange(
		7, WorkspacePermissions{AccountManage: true}, ratePtr(0.05), ratePtr(0.06),
	))

	if err := applyAccountFieldScope(ctx, "update", &ptr); err != nil {
		t.Fatalf("区间内的倍率应通过，实得 %v", err)
	}
	// 通过即原样保留：这层只校验，不改写。
	if ptr == nil || *ptr != 0.055 {
		t.Errorf("倍率应原样保留，实得 %v", ptr)
	}
}

// TestAccountFieldScopeVendorRateAboveMaxRejected 锁定越过上限即拒。
//
// 上限是站长夹住供应商定价的唯一手段。这里报错而非静默丢弃：倍率是
// 供应商唯一的商务参数，静默丢弃会让它以为改成功了而实际按旧值结算，
// 事后对账才发现差额。
func TestAccountFieldScopeVendorRateAboveMaxRejected(t *testing.T) {
	ptr := ratePtr(99)
	ctx := WithScope(context.Background(), VendorScopeWithSettlementRange(
		7, WorkspacePermissions{AccountManage: true}, ratePtr(0.05), ratePtr(0.06),
	))

	err := applyAccountFieldScope(ctx, "update", &ptr)

	if !errors.Is(err, domain.ErrWorkspaceSettlementRateOutOfRange) {
		t.Fatalf("越上限应报 out-of-range，实得 %v", err)
	}
}

// TestAccountFieldScopeVendorRateBelowMinRejected 锁定跌破下限即拒。
//
// 下限的用途是防止供应商把倍率压到成本以下做倾销 —— 结算按倍率走，
// 压低倍率等于让站长替它垫付上游成本。
func TestAccountFieldScopeVendorRateBelowMinRejected(t *testing.T) {
	ptr := ratePtr(0.01)
	ctx := WithScope(context.Background(), VendorScopeWithSettlementRange(
		7, WorkspacePermissions{AccountManage: true}, ratePtr(0.05), ratePtr(0.06),
	))

	if err := applyAccountFieldScope(ctx, "update", &ptr); !errors.Is(err, domain.ErrWorkspaceSettlementRateOutOfRange) {
		t.Fatalf("跌破下限应报 out-of-range，实得 %v", err)
	}
}

// TestAccountFieldScopeVendorRateNotAdjustable 锁定「未设上限即不可调」。
//
// Max 为 nil 表示站长没开放自助调价。这是零值 Scope 的语义 ——
// 中间件未挂载、或 context 缺失后的兜底作用域都落在这里，
// 因此「不可调」必须是默认态，与全局的默认拒绝取向一致。
func TestAccountFieldScopeVendorRateNotAdjustable(t *testing.T) {
	ptr := ratePtr(0.05)
	ctx := WithScope(context.Background(), VendorScope(7, WorkspacePermissions{AccountManage: true}))

	if err := applyAccountFieldScope(ctx, "update", &ptr); !errors.Is(err, domain.ErrWorkspaceSettlementRateNotAdjustable) {
		t.Fatalf("未设上限时应报 not-adjustable，实得 %v", err)
	}
}

// TestAccountFieldScopeVendorNilRatePasses 锁定「没提交倍率」不等于「改成 0」。
//
// 账号表单提交整个对象，供应商只想换个凭证时倍率字段为 nil。
// 若把 nil 当 0 校验，就会因跌破下限而拒掉一次正常的凭证更新。
func TestAccountFieldScopeVendorNilRatePasses(t *testing.T) {
	var ptr *float64
	ctx := WithScope(context.Background(), VendorScopeWithSettlementRange(
		7, WorkspacePermissions{AccountManage: true}, ratePtr(0.05), ratePtr(0.06),
	))

	if err := applyAccountFieldScope(ctx, "update", &ptr); err != nil {
		t.Fatalf("未提交倍率应直接通过，实得 %v", err)
	}
	if ptr != nil {
		t.Error("未提交的倍率不应被写入值")
	}
}

// TestAccountFieldScopeVendorNoMinAllowsAnyBelowMax 锁定下限缺省视为 0。
//
// 站长只关心「不得高于」时不该被迫填一个下限。
func TestAccountFieldScopeVendorNoMinAllowsAnyBelowMax(t *testing.T) {
	ptr := ratePtr(0.001)
	ctx := WithScope(context.Background(), VendorScopeWithSettlementRange(
		7, WorkspacePermissions{AccountManage: true}, nil, ratePtr(0.06),
	))

	if err := applyAccountFieldScope(ctx, "update", &ptr); err != nil {
		t.Fatalf("无下限时低倍率应通过，实得 %v", err)
	}
}

// TestAccountFieldScopeVendorNegativeRateRejected 锁定负倍率被拒。
//
// 负倍率会让用量产生负成本，把结算金额倒扣回去。
func TestAccountFieldScopeVendorNegativeRateRejected(t *testing.T) {
	ptr := ratePtr(-1)
	ctx := WithScope(context.Background(), VendorScopeWithSettlementRange(
		7, WorkspacePermissions{AccountManage: true}, nil, ratePtr(0.06),
	))

	if err := applyAccountFieldScope(ctx, "update", &ptr); !errors.Is(err, domain.ErrWorkspaceSettlementRateOutOfRange) {
		t.Fatalf("负倍率应报 out-of-range，实得 %v", err)
	}
}

// TestAccountFieldScopeAdminUnaffected 锁定「站长行为逐字不变」。
//
// 这是整个工作区机制的硬约束：任何收窄都不得改变 admin 的既有行为。
// 站长不受区间约束 —— 区间本就是它设给供应商的。
func TestAccountFieldScopeAdminUnaffected(t *testing.T) {
	ptr := ratePtr(99)
	ctx := WithScope(context.Background(), AdminScope())

	if err := applyAccountFieldScope(ctx, "update", &ptr); err != nil {
		t.Fatalf("站长路径不应受区间约束，实得 %v", err)
	}
	if ptr == nil || *ptr != 99 {
		t.Fatalf("站长设的倍率应原样保留，实得 %v", ptr)
	}
}
