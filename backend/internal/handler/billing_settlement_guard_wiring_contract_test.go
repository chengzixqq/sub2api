package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// billingSettlementGuardWiringFiles 是本契约测试覆盖的 4 个 handler 入口文件，
// 值是该文件内**转发循环的条数**，也就是 ObserveAttempt / ObserveForwardOutcome
// 各自必须出现的**精确次数**。
//
// gateway_handler.go 的 Messages 方法有两个转发循环（Gemini 平台分支 / 默认 Anthropic 分支），
// 两个分支共用同一个 guard：newBillingSettlementGuard 和 defer guard.Flush() 各只出现 1 次，
// 但 ObserveAttempt / ObserveForwardOutcome / MarkSettled 各出现 2 次（每个分支各一次）。
// 其余 3 个文件每个 handler 只有一条转发路径，上述调用均各出现 1 次。
//
// 这里必须断言**精确相等**而不是「≥1 次」：早先的 ≥1 判定在 gateway_handler.go 上有死角
// ——删掉第一个循环（Gemini 分支）的接线后，第二个循环的调用仍让计数 ≥1，测试保持绿色，
// 而 Gemini 家族的失败计费已经整体静默失效。
// openai_gateway_handler.go 有两条**已接线**的转发循环（Responses / Messages），
// 因此计数为 2。同文件内的 ResponsesWebSocket 刻意不接线：它按 turn 计费
// （AfterTurn 钩子内每轮各记一次 RecordUsage），而 guard 是每请求一次性结算，
// 接上去会在「连接内已有某个 turn 成功计费」之后再兜底记一笔，属多算方向。
// 详见 TestOpenAIWebSocketDeliberatelyNotGuardWired 的说明。
var billingSettlementGuardWiringFiles = map[string]int{
	"gateway_handler.go":                  2,
	"gateway_handler_chat_completions.go": 1,
	"gateway_handler_responses.go":        1,
	"gemini_v1beta_handler.go":            1,
	"openai_chat_completions.go":          1,
	"openai_embeddings.go":                1,
	"openai_images.go":                    1,
	"openai_alpha_search.go":              1,
	"openai_gateway_handler.go":           2,
	"grok_media.go":                       1,
}

// TestBillingSettlementGuardWiringContract 是一个 AST 契约测试，
// 守护 billingSettlementGuard 在 4 个 Claude/Gemini 系 handler 里的接线：
//
//  1. 每个创建了 guard 的函数，必须在同一函数体内有 defer guard.Flush()。
//     漏掉这一条，请求在转发循环中途某个 return 提前退出时，这次已经接触过上游、
//     本该按失败兜底计费的请求就永远不会结算——少算钱且事后无法追溯。
//  2. 每一处提交“成功路径” RecordUsage / RecordUsageWithLongContext 的
//     h.submitUsageRecordTask(...) 语句，在其所在语句列表（同一个 []ast.Stmt）里，
//     必须能往前找到一条 guard.MarkSettled()。漏掉这一条，defer guard.Flush() 会在
//     成功计费之后，把这次请求再按估算失败用量兜底计费一次——重复扣费。
//     本仓库计费总原则是"宁可少算，不可多算"，这一条比第 1 条更关键。
//  3. ObserveAttempt / ObserveForwardOutcome 在各文件内出现的次数，必须精确等于该文件的
//     转发循环条数（见 billingSettlementGuardWiringFiles）。漏掉其中一个循环，那条路径上
//     的失败请求会整体绕过失败兜底计费而静默失效。
//  4. guard.ObserveForwardOutcome(err, X) 的第二个参数 X 必须本身是一次
//     forwardDeliveredStreamContent(c) 调用，不能是裸的
//     c.Writer.Size() != writerSizeBeforeForward 之类的 BinaryExpr。这一条专门挡住本轮
//     修复要消灭的 Critical bug 复发：handler 层的字节启发式区分不了「上游真实投递的帧」
//     与「sub2api 自造的 keepalive ping / stream_timeout 等错误事件帧」，一旦第二个参数
//     被改回字节比较，挂死、零投递的上游请求会重新被误判为已投递而按整个估算 prompt 计费。
//  5. 每一处 guard.ObserveForwardOutcome(...) 调用，必须是其所在 if err != nil { ... } 块的
//     第一条语句（Body.List[0]）。如果前面插入了其他逻辑且该逻辑存在提前 return 的分支，
//     就会出现"这次 err != nil 却从未调用 ObserveForwardOutcome"的遗漏路径。
//
// 这几处此前只被 claudeFailureSink（纯函数，见 billing_failure_sink_test.go）和
// billingSettlementGuard 自身（billing_settlement_guard_test.go）覆盖，handler 里的
// 接线本身完全没有测试兜底——删掉任意一处接线，`go test ./internal/handler/` 都会
// 保持绿色。本测试直接解析 handler 源文件的 AST 校验接线的结构性存在，而不是跑一次
// 转发流程去断言副作用（那需要伪造完整的账号选择/上游转发依赖链，成本过高）。
func TestBillingSettlementGuardWiringContract(t *testing.T) {
	for name, forwardLoops := range billingSettlementGuardWiringFiles {
		t.Run(name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
			require.NoError(t, err)

			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				checkGuardFlushWiring(t, fset, name, fn)
				checkMarkSettledBeforeSuccessRecordUsage(t, fset, name, fn)
				checkObserveForwardOutcomeArgIsDeliveredCheck(t, fset, name, fn)
				checkObserveForwardOutcomeIsFirstStmtOfErrCheck(t, fset, name, fn)
				checkCyberPolicyResultGatesMarkSettled(t, fset, name, fn)
				checkMarkSettledExistsForHelperRecordUsage(t, fset, name, fn)
			}

			assertObserveCallCounts(t, file, name, forwardLoops)
		})
	}
}

// checkGuardFlushWiring 挡住“删掉 defer guard.Flush()”的漏计费变异：
// 函数体内每一次 newBillingSettlementGuard(...) 赋值，都必须能在同一函数体内
// 找到对应的 defer <var>.Flush()。
func checkGuardFlushWiring(t *testing.T, fset *token.FileSet, fileName string, fn *ast.FuncDecl) {
	t.Helper()

	guardVars := map[string]token.Pos{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		if name, ok := guardVarFromAssign(assign); ok {
			guardVars[name] = assign.Pos()
		}
		return true
	})
	if len(guardVars) == 0 {
		return // 本函数没有创建 guard，与本契约无关
	}

	deferredFlush := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		deferStmt, ok := n.(*ast.DeferStmt)
		if !ok {
			return true
		}
		recv, method, ok := callSelector(deferStmt.Call)
		if ok && method == "Flush" {
			deferredFlush[recv] = true
		}
		return true
	})

	for varName, pos := range guardVars {
		assert.True(t, deferredFlush[varName],
			"%s：函数 %s 里 %s := newBillingSettlementGuard(...)（%s）之后必须有 defer %s.Flush()，"+
				"否则转发循环中途 return 提前退出时，这次失败请求永远不会结算兜底计费",
			fileName, fn.Name.Name, varName, fset.Position(pos), varName)
	}
}

// checkCyberPolicyResultGatesMarkSettled 挡住 cyber_policy 双重计费变异（首轮复审的
// Critical）：在已接 guard 的函数体内，每一处 h.recordCyberPolicyIfMarked(...) 都必须把
// 返回值用作 if 条件，且该 if 体内出现 guard.MarkSettled()。
//
// 为什么这条规则必要：cyber 命中时服务层已按上游真实 usage 记了一笔账，而流式 cyber 会先
// 置位投递标记、返回的又是裸 error（不是 *UpstreamFailoverError），Flush 的
// 「ferr == nil && !outputStarted」早退不成立 —— 丢掉 MarkSettled 就会在 cyber 那笔之外
// 再按整份估算 prompt 兜底记第二笔。这种多算无法靠纯函数测试发现（需要完整的账号选择/
// 上游转发依赖链），所以与其它接线规则一样用 AST 结构性锚定。
//
// 反向也被锚住：若把 if 去掉退回裸调用（丢 MarkSettled）或把 MarkSettled 移出 if 体
// （无条件结算 ⇒ 普通失败请求被误标已结算、少算），本规则都会失败。
func checkCyberPolicyResultGatesMarkSettled(t *testing.T, fset *token.FileSet, fileName string, fn *ast.FuncDecl) {
	t.Helper()

	guardVars := collectGuardVarNames(fn.Body)
	if len(guardVars) == 0 {
		// ResponsesWebSocket 刻意不接 guard（按 turn 计费），其 cyber 调用不受本规则约束。
		return
	}

	// 先收集所有「作为 if 条件出现且 if 体内有 MarkSettled」的合规调用位置。
	gated := map[token.Pos]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		call, ok := ifStmt.Cond.(*ast.CallExpr)
		if !ok || !isRecordCyberPolicyIfMarkedCall(call) {
			return true
		}
		if markSettledPrecedes(append([]ast.Stmt{}, ifStmt.Body.List...), guardVars) {
			gated[call.Pos()] = true
		}
		return true
	})

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isRecordCyberPolicyIfMarkedCall(call) {
			return true
		}
		assert.True(t, gated[call.Pos()],
			"%s：函数 %s 里 %s 处的 h.recordCyberPolicyIfMarked(...) 返回值没有被用作 if 条件、"+
				"或该 if 体内缺少 guard.MarkSettled()。cyber 命中时服务层已按上游真实 usage 记账，"+
				"不 MarkSettled 会让 defer guard.Flush() 按估算 prompt 再记第二笔（多算）",
			fileName, fn.Name.Name, fset.Position(call.Pos()))
		return true
	})
}

// isRecordCyberPolicyIfMarkedCall 判断调用是否为 h.recordCyberPolicyIfMarked(...)。
func isRecordCyberPolicyIfMarkedCall(call *ast.CallExpr) bool {
	_, method, ok := callSelector(call)
	return ok && method == "recordCyberPolicyIfMarked"
}

// checkMarkSettledBeforeSuccessRecordUsage 挡住“删掉 guard.MarkSettled()”的多计费变异：
// 函数体内每一处提交“成功路径” RecordUsage / RecordUsageWithLongContext 的
// h.submitUsageRecordTask(...) 语句，在其所在语句列表（同一个 []ast.Stmt）里，
// 必须能往前找到一条 guard.MarkSettled() 语句。
func checkMarkSettledBeforeSuccessRecordUsage(t *testing.T, fset *token.FileSet, fileName string, fn *ast.FuncDecl) {
	t.Helper()

	guardVars := collectGuardVarNames(fn.Body)
	// 本函数体内没有 guard 变量时，本规则不适用：
	//
	//   - recordAlphaSearchUsage / recordGrokMediaUsage 是独立的记账辅助函数，
	//     guard 在其调用方（AlphaSearch / handleGrokMedia）里。在辅助函数体内
	//     找不到 MarkSettled 是正常的。
	//   - ResponsesWebSocket 刻意不接 guard（按 turn 计费，见文件头注释）。
	//
	// 注意本规则的适用范围仅限「成功记账 submitXxx 语句与 MarkSettled 出现在同一语句块」
	// 的形态。上面两个辅助函数把成功记账搬出了调用方语句块，调用方体内没有任何
	// submitXxx 语句，本规则对 grok_media.go / openai_alpha_search.go 因此**不生效**
	// （此前这里的注释声称「guardVars 非空时规则照旧生效」，对这两个文件是错的）。
	// 那两处 MarkSettled 的咬合由 checkMarkSettledExistsForHelperRecordUsage 单独负责。
	if len(guardVars) == 0 {
		return
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		stmts := stmtListOf(n)
		if stmts == nil {
			return true
		}
		for i, stmt := range stmts {
			exprStmt, ok := stmt.(*ast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := exprStmt.X.(*ast.CallExpr)
			if !ok || !isSubmitUsageRecordTaskCall(call) || !containsSuccessRecordUsageCall(call) {
				continue
			}
			assert.True(t, markSettledPrecedes(stmts[:i], guardVars),
				"%s：函数 %s 里 %s 处的 h.submitUsageRecordTask(成功路径 RecordUsage) 语句，"+
					"同一语句块内它之前没有先出现 guard.MarkSettled()，会导致 defer guard.Flush() "+
					"在成功计费之后又按估算失败用量多计一次费用",
				fileName, fn.Name.Name, fset.Position(stmt.Pos()))
		}
		return true
	})
}

// successRecordUsageHelperFuncs 是把「成功路径记账」搬进独立辅助函数的记账入口。
//
// 这些辅助函数内部自己调用 h.submitXxxUsageRecordTask(成功路径 RecordUsage)，所以在它们的
// 调用方（AlphaSearch / handleGrokMedia）体内找不到任何 submitXxx 语句，
// checkMarkSettledBeforeSuccessRecordUsage 那条「同一语句块」规则对调用方不生效。
// 少了本规则，删掉这两个调用方的 guard.MarkSettled() 契约测试会保持绿色（已实测）。
//
// 新增这类辅助函数时必须登记到这里，否则该端点的 MarkSettled 又会回到无人咬合的状态。
var successRecordUsageHelperFuncs = map[string]bool{
	"recordGrokMediaUsage":   true,
	"recordAlphaSearchUsage": true,
}

// checkMarkSettledExistsForHelperRecordUsage 挡住「删掉 guard.MarkSettled()」在
// 辅助函数记账形态下的多计费变异：函数体内既创建了 guard、又调用了
// successRecordUsageHelperFuncs 里的成功记账辅助函数，则该函数体内必须存在
// guard.MarkSettled()。
//
// 只要求「函数体内存在」而不要求「先于辅助函数调用」，因为这两处的 MarkSettled 位于外层
// if err == nil 块、辅助函数调用位于更内层的嵌套块，二者不在同一语句列表里，用顺序断言会
// 误报。存在性已足够咬住变异：删掉 MarkSettled 本规则即失败；而 guard 创建整条删掉则
// 编译不过（guard 未定义）。
func checkMarkSettledExistsForHelperRecordUsage(t *testing.T, fset *token.FileSet, fileName string, fn *ast.FuncDecl) {
	t.Helper()

	guardVars := collectGuardVarNames(fn.Body)
	if len(guardVars) == 0 {
		return // 辅助函数自身、以及刻意不接 guard 的 ResponsesWebSocket
	}

	helperCalled := ""
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch callee := call.Fun.(type) {
		case *ast.Ident: // 包级函数：recordGrokMediaUsage(...)
			if successRecordUsageHelperFuncs[callee.Name] {
				helperCalled = callee.Name
			}
		case *ast.SelectorExpr: // 方法：h.recordAlphaSearchUsage(...)
			if successRecordUsageHelperFuncs[callee.Sel.Name] {
				helperCalled = callee.Sel.Name
			}
		}
		return true
	})
	if helperCalled == "" {
		return
	}

	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if recv, method, ok := callSelector(call); ok && method == "MarkSettled" && guardVars[recv] {
			found = true
		}
		return true
	})

	assert.True(t, found,
		"%s：函数 %s（%s）调用了成功记账辅助函数 %s 并持有 guard，但函数体内没有任何 "+
			"guard.MarkSettled()。该端点的成功请求会在正常记账之外，被 defer guard.Flush() "+
			"按估算 prompt 再记一笔（多算）",
		fileName, fn.Name.Name, fset.Position(fn.Pos()), helperCalled)
}

// assertObserveCallCounts 断言文件内 guard.ObserveAttempt(...) 与
// guard.ObserveForwardOutcome(...) 各自出现的次数，精确等于该文件的转发循环条数。
//
// 用精确相等而不是「≥1 次」：gateway_handler.go 是唯一有两个转发循环的文件，
// ≥1 判定下删掉其中任意一个循环的接线，另一个循环的调用仍让计数 ≥1，测试保持绿色，
// 而那条分支上的失败请求已经整体不再结算。
func assertObserveCallCounts(t *testing.T, file *ast.File, fileName string, forwardLoops int) {
	t.Helper()

	var attemptCount, outcomeCount int
	ast.Inspect(file, func(n ast.Node) bool {
		exprStmt, ok := n.(*ast.ExprStmt)
		if !ok {
			return true
		}
		_, method, ok := callSelector(exprStmt.X)
		if !ok {
			return true
		}
		switch method {
		case "ObserveAttempt":
			attemptCount++
		case "ObserveForwardOutcome":
			outcomeCount++
		}
		return true
	})

	assert.Equal(t, forwardLoops, attemptCount,
		"%s：本文件有 %d 个转发循环，每个都必须调用一次 guard.ObserveAttempt(account)，"+
			"实际只找到 %d 次。少一次就意味着有一条转发路径上的失败请求永远拿不到账号，"+
			"Flush 会当成“从未接触上游”整体跳过结算",
		fileName, forwardLoops, attemptCount)
	assert.Equal(t, forwardLoops, outcomeCount,
		"%s：本文件有 %d 个转发循环，每个都必须在 if err != nil 的第一行调用一次 "+
			"guard.ObserveForwardOutcome(err, forwardDeliveredStreamContent(c))，"+
			"实际只找到 %d 次。少一次就意味着有一条转发路径上的失败请求既不记录失败原因、"+
			"也不定格 outputStarted，Flush 会按估算 prompt token 多计一次费用",
		fileName, forwardLoops, outcomeCount)
}

// checkObserveForwardOutcomeArgIsDeliveredCheck 挡住"第二个参数被改回裸字节比较"的回归——
// 这正是本轮修复要消灭的 Critical bug 的复发形态（见 gateway_billing_fallback.go 里
// GatewayUpstreamDeliveredKey 的文档）：handler 层用 c.Writer.Size() 之类的字节启发式，
// 区分不了「上游真实投递的帧」和 sub2api 自己在读循环挂死时写的 keepalive ping /
// stream_timeout、stream_read_error 等错误事件帧，只有 forwardDeliveredStreamContent(c)
// 读取的 service.GatewayUpstreamDeliveredKey 标记才准确。
func checkObserveForwardOutcomeArgIsDeliveredCheck(t *testing.T, fset *token.FileSet, fileName string, fn *ast.FuncDecl) {
	t.Helper()

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		exprStmt, ok := n.(*ast.ExprStmt)
		if !ok {
			return true
		}
		call, ok := exprStmt.X.(*ast.CallExpr)
		if !ok {
			return true
		}
		_, method, ok := callSelector(exprStmt.X)
		if !ok || method != "ObserveForwardOutcome" {
			return true
		}
		if len(call.Args) != 2 {
			return true // 参数个数不对，编译期已经会报错，不是本测试要管的事
		}

		argCall, isCall := call.Args[1].(*ast.CallExpr)
		calleeName := ""
		if isCall {
			if ident, ok := argCall.Fun.(*ast.Ident); ok {
				calleeName = ident.Name
			}
		}
		assert.True(t, isCall && calleeName == "forwardDeliveredStreamContent",
			"%s：函数 %s 里 %s 处 guard.ObserveForwardOutcome 的第二个参数是 %T（%v，调用目标 %q），"+
				"不是 forwardDeliveredStreamContent(c) 调用——handler 层用字节数量/BinaryExpr 之类的"+
				"启发式区分不了「上游真实投递的帧」与「sub2api 自造的 keepalive ping / stream_timeout "+
				"等错误事件帧」，改回裸表达式会让挂死、零投递的上游请求重新被误判为已投递而按整个"+
				"估算 prompt 计费（这正是本轮要修的 Critical bug）",
			fileName, fn.Name.Name, fset.Position(call.Pos()), call.Args[1], call.Args[1], calleeName)
		return true
	})
}

// checkObserveForwardOutcomeIsFirstStmtOfErrCheck 挡住"ObserveForwardOutcome 被挪到
// if err != nil 块靠后位置"的回归：如果前面插入了其他逻辑，一旦那段逻辑存在提前 return
// 的分支，就会出现"这次 err != nil 但从未调用 ObserveForwardOutcome"的遗漏路径，
// Flush 兜底结算时用到的 outputStarted 会停留在上一次循环的陈旧值，多算/少算方向不可控。
func checkObserveForwardOutcomeIsFirstStmtOfErrCheck(t *testing.T, fset *token.FileSet, fileName string, fn *ast.FuncDecl) {
	t.Helper()

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok || ifStmt.Body == nil {
			return true
		}

		for i, stmt := range ifStmt.Body.List {
			exprStmt, ok := stmt.(*ast.ExprStmt)
			if !ok {
				continue
			}
			_, method, ok := callSelector(exprStmt.X)
			if !ok || method != "ObserveForwardOutcome" {
				continue
			}
			assert.Equal(t, 0, i,
				"%s：函数 %s 里 %s 处 guard.ObserveForwardOutcome(...) 不是所在 if 块的第一条语句"+
					"（实际排第 %d 条）。前面插入的逻辑一旦存在提前 return 的分支，就会出现某次 "+
					"err != nil 却从未调用 ObserveForwardOutcome 的遗漏路径，Flush 兜底结算时 "+
					"outputStarted 会停留在上一次循环的陈旧值",
				fileName, fn.Name.Name, fset.Position(stmt.Pos()), i)
		}
		return true
	})
}

// callSelector 判断 expr 是否是形如 x.Method(...) 的调用，返回接收者标识符名与方法名。
func callSelector(expr ast.Expr) (recv string, method string, ok bool) {
	call, isCall := expr.(*ast.CallExpr)
	if !isCall {
		return "", "", false
	}
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel {
		return "", "", false
	}
	ident, isIdent := sel.X.(*ast.Ident)
	if !isIdent {
		return "", "", false
	}
	return ident.Name, sel.Sel.Name, true
}

// isNewGuardCall 判断 expr 是否是对包级函数 newBillingSettlementGuard(...) 的调用。
func isNewGuardCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	ident, ok := call.Fun.(*ast.Ident)
	return ok && ident.Name == "newBillingSettlementGuard"
}

// guardVarFromAssign 若 stmt 形如 `x := newBillingSettlementGuard(...)`，返回变量名 x。
func guardVarFromAssign(stmt ast.Stmt) (string, bool) {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
		return "", false
	}
	if !isNewGuardCall(assign.Rhs[0]) {
		return "", false
	}
	ident, ok := assign.Lhs[0].(*ast.Ident)
	if !ok {
		return "", false
	}
	return ident.Name, true
}

// collectGuardVarNames 收集函数体内所有由 newBillingSettlementGuard(...) 创建的变量名。
func collectGuardVarNames(body *ast.BlockStmt) map[string]bool {
	names := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		if name, ok := guardVarFromAssign(assign); ok {
			names[name] = true
		}
		return true
	})
	return names
}

// stmtListOf 返回节点自带的语句列表（块 / switch-case / select-case），
// 用于在“同一个语句列表”的粒度上判断 MarkSettled 与 submitUsageRecordTask 的先后顺序。
func stmtListOf(n ast.Node) []ast.Stmt {
	switch v := n.(type) {
	case *ast.BlockStmt:
		return v.List
	case *ast.CaseClause:
		return v.Body
	case *ast.CommClause:
		return v.Body
	default:
		return nil
	}
}

// isSubmitUsageRecordTaskCall 判断 call 是否是「提交成功路径用量记录」的调用。
//
// 三个名字都要匹配，因为两个 handler 家族用的提交入口不同：
//   - submitUsageRecordTask：Claude/Gemini 家族。
//   - submitOpenAIUsageRecordTask：OpenAI 家族，多一个 result 形参
//     （result.ImageCount > 0 时改走 mandatory 池）。
//   - submitMandatoryUsageRecordTask：alpha_search 的成功路径直接用 mandatory 池。
//
// 漏掉后两个名字，OpenAI 家族所有成功路径都不会被第 2 条规则（MarkSettled 必须
// 先于成功记账）检查到，删掉 guard.MarkSettled() 测试仍会绿——正是本契约要挡的多算变异。
func isSubmitUsageRecordTaskCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch sel.Sel.Name {
	case "submitUsageRecordTask", "submitOpenAIUsageRecordTask", "submitMandatoryUsageRecordTask":
		return true
	}
	return false
}

// containsSuccessRecordUsageCall 判断 submitUsageRecordTask 调用的参数（通常是异步闭包体）
// 内部是否含有 h.gatewayService.RecordUsage(...) 或 RecordUsageWithLongContext(...)。
// 两个方法名都要匹配：Gemini 长上下文成功路径调用的是后者（见 gemini_v1beta_handler.go），
// 其余三个文件调用的是前者。
func containsSuccessRecordUsageCall(call *ast.CallExpr) bool {
	found := false
	for _, arg := range call.Args {
		if found {
			break
		}
		ast.Inspect(arg, func(n ast.Node) bool {
			if found {
				return false
			}
			inner, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := inner.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name != "RecordUsage" && sel.Sel.Name != "RecordUsageWithLongContext" {
				return true
			}
			recvSel, ok := sel.X.(*ast.SelectorExpr)
			if ok && recvSel.Sel.Name == "gatewayService" {
				found = true
				return false
			}
			return true
		})
	}
	return found
}

// markSettledPrecedes 判断 stmts（某条语句之前、同一语句列表内的语句切片）里，
// 是否存在对 guardVars 中某个 guard 变量调用 MarkSettled() 的语句。
func markSettledPrecedes(stmts []ast.Stmt, guardVars map[string]bool) bool {
	for _, stmt := range stmts {
		exprStmt, ok := stmt.(*ast.ExprStmt)
		if !ok {
			continue
		}
		recv, method, ok := callSelector(exprStmt.X)
		if ok && method == "MarkSettled" && guardVars[recv] {
			return true
		}
	}
	return false
}
