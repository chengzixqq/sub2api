# sub2api 失败请求计费对齐 newapi — 设计文档

日期：2026-07-27
分支：`billing-failure-parity`
基线：upstream `main` @ `dc893dd0b`

## 1. 问题

sub2api 对**失败请求完全不计费**，而上游供应商照常收费。差额由部署方承担。

结构性证据（非推断），`backend/internal/handler/gateway_handler.go:501-502`：

```go
reqLog.Error("gateway.forward_failed", forwardFailedFields...)
return
```

该 `return` 位于 `submitUsageRecordTask(...)` 之前，`RecordUsage` 在失败路径上**永远不会被调用**。仓库自身的注释也确认了这一意图，`backend/internal/repository/usage_log_repo.go:21` 把失败请求描述为「tokens=0、cost=0、不计费的占位记录」。

漏损面最大的场景不是硬错误，而是：上游已生成并计费了 token，但响应在返回途中失败（超时、流中断、连接被杀、客户端断开）。

## 2. 目标与非目标

**目标**：把 new-api 的失败/中断计费语义移植到 sub2api，使成功与失败汇入同一条计费路径。

**非目标**（明确排除，保持可与上游 rebase）：
- 不改 sub2api 的 USD 定价表、ent schema、管理端 UI
- 不引入 new-api 的整数 quota / ratio 体系
- 不引入预扣费（pre-consume），sub2api 保持后付费模型

## 3. 核心洞察

new-api 不漏钱**不是因为它有失败计费分支，而是因为它没有**。成功与失败都走同一个 `PostConsumeQuota`，区别仅在于 usage 缺失时的兜底。

因此本次改动的方向是**删除失败早退**，而不是新增失败分支。

## 4. 需要移植的四条 new-api 语义

以下均为 new-api 源码中的既有行为，本设计逐条对应移植。

### 4.1 无 usage 时的估算（`service/text_quota.go:335-341`）

上游未返回任何 usage 时：prompt tokens 取请求侧估算值，completion tokens 记 0。

### 4.2 流中断后的保守计费（`service/billing_usage.go:34-63`）

**这是与直觉相反、必须严格照搬的一条。** new-api 的 `conservativeInterruptedStreamUsage`：当已向下游输出过内容、但上游未给出可用的终止 usage 帧时：

- prompt tokens = 请求侧估算值（下限 0）
- completion tokens = 已接收的响应事件数，**下限为 1，绝不为 0**
- 同时打一条 warning 日志

注释原文说明了理由：防止一次已部分投递的响应变成免费请求。

因此本项目 §4.1 与 §4.2 是**两条不同的兜底路径**，不可合并为「output 一律记 0」。

### 4.3 零值护栏与审计（`service/quota.go:286-297`）

可计费 token 为 0 时，费用置 0 并写审计日志（new-api 标注「可能是上游超时」），而不是静默通过。

### 4.4 计费来源可审计（`service/billing_usage.go:65-109`）

new-api 把计费来源写入日志的 `admin_info.usage_billing_path`，且估算路径带 `-estimated` 后缀（如 `billing-usage-anthropic-estimated`）。管理端可据此区分「上游真实 usage」与「本地估算」。

非管理员视图会剥离 `admin_info`，因此该字段天然是管理员可见。

## 5. 不计费边界

new-api 通过「预扣费 → 退款净零」达成不收费，没有显式白名单。sub2api 是后付费、无可退之物，因此该语义**必须显式化**。

sub2api 已具备分类原语（`gateway_service.go:611` 的 `UpstreamFailoverError`，含 `StatusCode` / `Stage` / `Scope` / `Reason`）：

| 条件 | 判定 | 理由 |
|---|---|---|
| `Stage == account_auth`（凭证 401/403） | 不计费 | 请求从未到达模型，上游不计费 |
| `Scope == request`（400 参数错、模型不存在） | 不计费 | 上游拒绝在推理前 |
| 429 限流且未产出任何输出 | 不计费 | 未进入推理 |
| 超时 / 推理中 5xx / 连接被杀 / 客户端断开 | **计费** | 上游已产出并计费 |

判定输入还包括 `ForwardResult.ClientDisconnect`（`gateway_service.go:564`）。

## 6. 架构

### 6.1 新增：`backend/internal/service/gateway_billing_fallback.go`

单一职责——把「失败的转发结果」翻译成「计费决策」，不做 IO，可独立单测。

对外接口：

```go
type BillingProvenance string // "upstream_usage" | "estimated" | "none"

type FailureBillingDecision struct {
    Billable   bool
    Usage      ClaudeUsage        // 仅 Billable 为 true 时有意义
    Provenance BillingProvenance
    Reason     string             // 写入日志，供审计
}

// estimatedPromptTokens 由 handler 用既有 tokenizer 能力算出（见 6.3），
// 传值而非传请求体，使本函数保持纯函数、可独立单测。
func DecideFailureBilling(
    res *ForwardResult,
    ferr *UpstreamFailoverError,
    estimatedPromptTokens int,
) FailureBillingDecision
```

依赖：仅依赖已有的 `ForwardResult`（`gateway_service.go:554`，其 `Usage` 字段类型为 `ClaudeUsage`——sub2api 将各家 provider 的用量统一归一到该结构）与 `UpstreamFailoverError`。不依赖 gin、ent、Redis。

计费决策产出后，由调用方写回 `ForwardResult.Usage`，再塞进既有的 `RecordUsageInput`（`gateway_usage_billing.go:39`，其 `Result *ForwardResult` 字段即用量载体）。因此**不需要**新增任何用量传输结构。

### 6.2 结算兜底守卫（取代原「每个早退点手工挂钩」方案）

原文设想在失败早退点前调用一个 `submitFailedUsageRecordTask`。实测早退面远超预期：

| 失败出口 | 站点数 |
|---|---|
| `handleFailoverExhausted` / `openaiHandleFailoverExhausted` | 36 |
| `failoverClientGone` | 28 |
| `*ForwardErrorAlreadyCommunicated` | 9 |

合计 73 处，分布在 10 个 handler 文件、两个 handler 家族（`GatewayHandler` 与
`OpenAIGatewayHandler`）。逐点挂钩有两个致命问题：一是漏挂即静默漏钱，而漏挂无法被编译器发现；
二是上游每次新增早退点都会重新漏钱，直接违反「保持可同步上游」这一约束。

改为**单一 defer 兜底守卫**：请求内只注册一次，无论从哪个 `return` 退出都必然执行。

```go
// backend/internal/handler/billing_settlement_guard.go
type billingSettlementGuard struct {
    settled bool          // 成功路径已提交 RecordUsage
    account *service.Account
    ferr    *service.UpstreamFailoverError
    // 其余为构造时捕获的请求级快照
}

// 在 apiKey/user 解析完成后调用，紧跟 defer guard.Flush()
func newBillingSettlementGuard(c *gin.Context, ...) *billingSettlementGuard
func (g *billingSettlementGuard) ObserveAttempt(account *service.Account)  // 循环内每次选号后
func (g *billingSettlementGuard) MarkSettled()                            // 成功路径
func (g *billingSettlementGuard) Flush()                                  // defer；未结算则走失败计费
```

挂钩点从 73 处降到 ~13 处（每个转发循环 1 处 `ObserveAttempt` + 每个 handler 1 处
`defer`/`MarkSettled`），且**新增早退点默认被覆盖**而非默认漏钱——默认方向反了过来。

两个 handler 家族共用同一个守卫，因为两者都持有 `*gin.Context`；`FailoverState` 做不到，
OpenAI 家族用的是局部 `lastFailoverErr` 变量而非 `FailoverState`。

守卫本身不做计费判断，只负责「保证 §6.1 的纯函数被调用一次」。

### 6.3 估算来源

sub2api 已依赖 `github.com/tiktoken-go/tokenizer`，且已有 `EstimateGrokCountTokens`（`openai_gateway_count_tokens.go`）、`estimateGeminiCountTokens`（`gemini_messages_compat_service.go:2491`）。估算复用这些既有能力，不新引入 tokenizer。

估算失败（请求体不可解析）时退回 0 并打 warning——**绝不猜一个大数**。

### 6.4 复用既有计费管线

决策产出的 usage 仍然走既有的 `CalculateCostUnified` → `CostBreakdown`（`billing_service.go:155`）→ `applyUsageBilling` → `repo.Apply`。不新增第二套费用计算。

## 7. 数据流

```
Forward 失败
  ↓
DecideFailureBilling(res, ferr, est)   ← 纯函数，可单测
  ↓ Billable=false ────────────→ 记 0 成本占位行，provenance=none
  ↓ Billable=true
submitFailedUsageRecordTask(...)        ← 复用既有 worker pool
  ↓
RecordUsage → CalculateCostUnified → applyUsageBilling → repo.Apply
  ↓
usage_log 行携带 provenance 标记
```

## 8. 连带必须处理

### 8.1 成功判定过滤器（原节述反了，已更正）

`backend/internal/repository/usage_log_repo.go:21` 的 `usageLogSuccessFilterUL` 是 `ul.actual_cost > 0`。
它**准入**有成本的行。改动后失败行会携带真实 cost，于是失败行会**通过**该过滤器，被全部
Dashboard 指标当成成功请求统计。本节早先写的「真实收入被过滤掉」方向相反，作废。

该过滤器自己的注释已说明选这个代理的原因：schema 中没有 success 列，新增列要做迁移、风险大。

**决定（已确认）**：新增一个专用列 `billing_provenance`，过滤器改为同时排除已计费失败行。

该列要同时承担两个**互相独立**的轴，取值表必须体现这一点：

1. 这一行是成功请求还是失败请求（决定是否进成功统计）
2. 用量来自上游真实 usage 还是本地估算（§4.4 审计）

两者不是同一件事：上游返回 200 但没给 usage 帧的请求是**成功**的，却要标估算；失败但拿到了
上游 usage 的请求有**真实用量**，却不能进成功统计。因此取值为二维交叉：

| 值 | 含义 | 进成功统计 |
|---|---|---|
| `NULL` | 历史行；成功且用量来自上游 | 是 |
| `estimated` | 成功，但上游未给 usage，用量为本地估算 | 是 |
| `failed_upstream` | 失败，用量来自上游真实 usage | 否 |
| `failed_estimated` | 失败，用量为本地估算 | 否 |

```
ul.actual_cost > 0 AND (ul.billing_provenance IS NULL OR ul.billing_provenance = 'estimated')
```

成功路径在上游给了 usage 时**不写该列**（保持 NULL），历史行行为完全不变。

过滤器采用白名单而非黑名单：将来新增任何 `failed_*` 取值都会**默认被排除**，而不是默认漏进
成功统计。与 §6.2 的守卫同一个原则——让默认方向站在安全一侧。

`billing_type` 不可复用：它已编码钱包(0)/订阅(1)。`request_type` 也不可复用：它编码传输模式
（sync/stream/ws_v2/live）加一个 outcome（cyber=4），塞入 failed 会丢掉失败行的 stream/sync 区分。

### 8.2 该列不进 ent schema

`usage_logs` 的列并非都在 `backend/ent/schema/usage_log.go` 里——`request_type`（迁移 061/173/188）
与 `image_input_tokens`（迁移 179）都只存在于 SQL 迁移中，ent schema 没有它们。usage_logs 加列的
既有惯例是**只写 SQL 迁移，不动 ent schema**。因此 §2 的非目标「不改 ent schema」依然成立。

### 8.3 写入路径是手工维护的裸 SQL

`backend/internal/repository/usage_log_repo_insert.go` 用裸 SQL 插入，列清单在 8 处重复
（240/695/785/844/943/1028/1087/1154 行附近），另有 44 行的类型数组与
`usage_log_repo_query.go:22` 的 `usageLogSelectColumns`。加列必须穷尽这些位置，漏一处则运行时插入失败。

## 9. 错误处理

- 计费决策本身 panic/出错 → 记 0 + 告警，**不得阻断响应返回**（计费在 detached goroutine 中，已隔离）
- 估算不可用 → 0 + warning
- 与 new-api 一致：可计费量为 0 时写审计日志而非静默

## 10. 测试

表驱动，`失败类型 × (有/无 usage)` → 期望 cost。

必须硬回归的四条：
1. 凭证 401 → cost == 0
2. 429 未产出 → cost == 0
3. 上游超时 → cost > 0
4. 流中断且已输出 → completion tokens >= 1（§4.2，防止回归成免费）

沿用既有 `gateway_handler_billing_error_test.go` 的风格。

## 11. 已知未决项（实现阶段第一步）

仓库中有 12+ 处 `RecordUsage` 调用点：`gateway_handler.go:540,977`、`gateway_handler_chat_completions.go:314`、`gateway_handler_responses.go:295`、`grok_media.go:484`、`openai_alpha_search.go:243`、`openai_chat_completions.go:347`、`openai_embeddings.go:249`、`openai_gateway_handler.go:684,1196,1972`、`openai_images.go:381`。

本设计**尚未逐一核实**每个调用点的失败早退是否同构。实现阶段第一步必须是穷举这些路径并列出实际形态，再决定接入方式。已知另有四处显式「不计费」站点需单独判定是否属于 §5 白名单：`openai_alpha_search.go:120`、`openai_cyber_policy.go:19`、`openai_gateway_chat_completions.go:315`、`openai_gateway_messages.go:453`。

## 12. 风险

- **LGPL-3.0**：自部署自用无开源义务；对外提供服务并分发二进制时，需公开对 LGPL 部分的修改。
- **上游同步**：核心逻辑集中在 1 个新文件 + 过滤器修改，可 rebase；但接入点分散在 12+ handler，上游重构 handler 时会产生冲突。这是接受的代价——替代方案（改造单一中间件）需要重构 sub2api 的转发层，代价更高且更难同步。
