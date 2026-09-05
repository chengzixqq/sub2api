package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenAIWebSocketDeliberatelyNotGuardWired 把「ResponsesWebSocket 刻意不接
// billingSettlementGuard」这个决定固化成测试，防止日后有人把它当成漏接而补上去。
//
// 为什么不能接：billingSettlementGuard 是**每请求一次性**结算——Flush 由请求级
// defer 触发，MarkSettled 一旦置位就整体跳过兜底。而 ResponsesWebSocket 是一条
// 长连接，在 AfterTurn 钩子里**按 turn** 各记一次 RecordUsage
// （openai_gateway_handler.go:1942 一带）。两者语义不匹配：
//
//   - 一条连接跑了 5 个 turn，前 4 个成功计费、第 5 个失败。请求级 guard 只有一次
//     MarkSettled/Flush 的机会：若在成功 turn 上 MarkSettled，第 5 个 turn 的失败就
//     永远不结算（少算）；若不 MarkSettled，Flush 会在 4 次成功计费**之外**再按估算
//     prompt token 兜底记一笔（多算）。
//   - 更糟的是 GatewayUpstreamDeliveredKey 挂在共享的 *gin.Context 上：第一个 turn
//     投递内容后该标记就一直为 true，后续任何 turn 的失败都会被判定为「上游已投递」
//     而按整个估算 prompt 计费——多算方向，正是本分支最严重级别的缺陷。
//
// 正确做法是 per-turn 粒度的结算器，超出 Task 7b 范围。在那之前，WS 路径维持现状
// （失败 turn 不计费 = 少算方向，按「宁可少算，不可多算」可接受）。
func TestOpenAIWebSocketDeliberatelyNotGuardWired(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "openai_gateway_handler.go", nil, 0)
	require.NoError(t, err)

	fn := findFuncDecl(file, "ResponsesWebSocket")
	require.NotNil(t, fn, "openai_gateway_handler.go 里找不到 ResponsesWebSocket")

	guardVars := collectGuardVarNames(fn.Body)
	assert.Empty(t, guardVars,
		"ResponsesWebSocket 里出现了 newBillingSettlementGuard(...)（变量 %v）。"+
			"该 handler 按 turn 计费（AfterTurn 钩子内每轮各记一次 RecordUsage），"+
			"而 guard 是每请求一次性结算，接上去会造成多算："+
			"GatewayUpstreamDeliveredKey 挂在共享的 gin.Context 上，"+
			"首个 turn 投递内容后该标记恒为 true，之后任何失败 turn 都会被判为"+
			"「上游已投递」而按整个估算 prompt 计费。"+
			"若确实要给 WS 加失败计费，必须实现 per-turn 粒度的结算器，"+
			"并同步更新本测试与 billingSettlementGuardWiringFiles", guardVars)

	// 反向锚点：确认 AfterTurn 按 turn 计费这个前提仍然成立。前提一旦变了
	// （比如 WS 改成整条连接只记一次账），上面的豁免理由就需要重新评估。
	assert.True(t, hasAfterTurnPerTurnBilling(fn),
		"ResponsesWebSocket 里找不到 AfterTurn 钩子内的 RecordUsage 提交。"+
			"本测试豁免 WS 接线的理由是「它按 turn 计费」，该前提已不成立，"+
			"请重新评估是否应该接 guard")
}

// findFuncDecl 按名字找顶层函数/方法声明。
func findFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Body != nil && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

// hasAfterTurnPerTurnBilling 判断 fn 内是否存在 `AfterTurn: func(...)` 字段，
// 且该闭包体内含有提交成功路径 RecordUsage 的调用。
func hasAfterTurnPerTurnBilling(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "AfterTurn" {
			return true
		}
		ast.Inspect(kv.Value, func(inner ast.Node) bool {
			if found {
				return false
			}
			call, ok := inner.(*ast.CallExpr)
			if ok && isSubmitUsageRecordTaskCall(call) && containsSuccessRecordUsageCall(call) {
				found = true
				return false
			}
			return true
		})
		return true
	})
	return found
}
