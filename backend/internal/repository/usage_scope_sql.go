package repository

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// appendUsageAccountScope 为用量查询追加账号白名单谓词。
//
// 返回待拼接的 SQL 片段（含前导 " AND "）与追加后的参数表。
// 不受限时返回空片段并原样返回 args。
//
// 受限且白名单为空时追加恒假条件：该工作区没有任何账号，
// 任何用量查询都应为零结果，而不是退化成全量可见。
//
// column 传表别名限定的列名（如 "ul.account_id"）或裸列名
// （"account_id"），由调用点的 FROM 子句决定。
func appendUsageAccountScope(ctx context.Context, column string, args []any) (string, []any) {
	ids, restricted := service.UsageAccountScopeFrom(ctx)
	if !restricted {
		return "", args
	}
	if len(ids) == 0 {
		return " AND FALSE", args
	}
	args = append(args, pq.Array(ids))
	return " AND " + column + " = ANY($" + itoa(len(args)) + ")", args
}

// appendUsageGroupGrantScope restricts usage rows to groups explicitly
// granted to the current vendor workspace.  Account membership alone is not
// sufficient: an account can retain historical account_groups rows after a
// grant is revoked, so group-level aggregates must enforce the grant table at
// the SQL boundary as well.
func appendUsageGroupGrantScope(ctx context.Context, column string, args []any) (string, []any, bool) {
	scope, ok := service.ScopeFromContext(ctx)
	if !ok || scope.Unrestricted {
		return "", args, false
	}
	if scope.WorkspaceID <= 0 {
		return " AND FALSE", args, true
	}
	args = append(args, scope.WorkspaceID)
	placeholder := "$" + itoa(len(args))
	return " AND " + column + " IN (SELECT group_id FROM workspace_group_grants WHERE workspace_id = " + placeholder + " AND enabled)", args, true
}

// appendUsageAccountScopeCondition 是 conditions 形态的变体，供以 []string
// 累积谓词再 strings.Join 的查询点使用。
//
// 签名与相邻的 appendUsageLogModelWhereCondition 等保持一致，
// 便于在同一串 append 链里顺序调用。不受限时原样返回两个切片。
func appendUsageAccountScopeCondition(
	ctx context.Context,
	column string,
	conditions []string,
	args []any,
) ([]string, []any) {
	ids, restricted := service.UsageAccountScopeFrom(ctx)
	if !restricted {
		return conditions, args
	}
	if len(ids) == 0 {
		return append(conditions, "FALSE"), args
	}
	args = append(args, pq.Array(ids))
	return append(conditions, column+" = ANY($"+itoa(len(args))+")"), args
}

// usageAccountScopeRestricted 报告当前 context 是否受账号白名单约束。
//
// 供预聚合表分支判断使用：那些表没有 account 维度，无法施加谓词，
// 受限时必须回落到原表查询。
func usageAccountScopeRestricted(ctx context.Context) bool {
	_, restricted := service.UsageAccountScopeFrom(ctx)
	return restricted
}
