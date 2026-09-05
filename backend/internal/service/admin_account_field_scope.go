package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// 账号写入的字段级作用域过滤。
//
// 账号级倍率就是结算倍率：计费按账号取 rate_multiplier 作为成本口径，
// 供应商在站长划定的区间内自主定价（见 Scope.ValidateSettlementRate）。
//
// 因此这里与分组侧（admin_group_field_scope.go）的处置**不同**：分组侧是
// 丢弃越权字段，这里是校验区间并在越界时报错。差别的理由是可观测性 ——
// 倍率是供应商唯一能自主设定的商务参数，静默丢弃会让它以为改成功了，
// 而实际按旧值结算，事后对账才发现差额。报错则当场可见。
//
// 未开放调价（站长未设上限）时同样报错而非静默丢弃，理由相同。
//
// 这层必须在后端：前端已按 settlement_rate_max 决定是否渲染输入框，
// 但藏掉的字段照样能手工构造请求送上来。

// applyAccountFieldScope 校验账号写入中的作用域敏感字段。
//
// admin 原样放行 —— 工作区机制的硬约束是站长行为逐字不变。
// rate 为 nil（本次未提交倍率）时直接通过：不能把「没改」当成「改成 0」。
func applyAccountFieldScope(ctx context.Context, action string, rate **float64) error {
	scope := ScopeFromContextOrDeny(ctx)
	if !scope.IsVendor() {
		return nil
	}
	if *rate == nil {
		return nil
	}

	if err := scope.ValidateSettlementRate(**rate); err != nil {
		// 记审计：越界尝试必须留痕，否则供应商反复试探上限在日志里不可见。
		logger.LegacyPrintf("service.admin",
			"audit: vendor account %s settlement rate rejected workspace_id=%d rate=%v err=%v",
			action, scope.WorkspaceID, **rate, err)
		return err
	}
	return nil
}
