# 定制 sub2api 合并官方 v0.2.18

## 起点、历史与提交

- 仓库：`G:/weixinProject/sub2`；分支：`chore/upgrade-upstream-v0.2.18`。
- 起点：`main@5905b30a90725e4706734e135a9b6e898d9b6c0f`；官方标签：`v0.2.18@de86961b627e48db75d61fff8c05535d44a00cbe`。
- 共同祖先：官方 v0.2.16 `01842576b3d5c1e2545b5a99ad555d36e28ed306`。
- 使用 `git switch -c chore/upgrade-upstream-v0.2.18 main`、指定标签 fetch、`git merge v0.2.18`。保留双亲合并历史，没有 rebase、squash 或 cherry-pick 重放。
- 生产提交 `e5ab7e4f4`、上次升级 `bdfb5d939` 均保留为祖先；生产提交到原 main 恰有 133 个提交。原生产定制及 App 适配没有被替换成官方树。
- 本报告随合并提交一起保存；实际合并号、后续提交号、推送验证见外部 `FINAL-REPORT.md` 与 `final-state.json`。本文件不把尚未执行的提交或推送记为成功。

## A 类冲突处理

初次 merge 有 18 个文本冲突文件。其余重叠行为按下方 B 表落实；独立增量、配置、测试和 i18n 双保留。

| 文件 | 处理 |
|---|---|
| `Dockerfile` | 采用官方 Node 24、Go 1.27.3，保留定制入口 `chmod 0755`。 |
| `backend/cmd/server/VERSION` | 保留官方标签中的原值 `0.2.17`；v0.2.18 标签内该值未递增。 |
| `backend/internal/payment/types.go` | 双方 import、V1/V2 类型、`RequestedAt`、`NotificationConfirmer` 全保留。 |
| `backend/internal/service/payment_fulfillment.go / RetryFulfillment` | 合入官方订单类型白名单，保留本地 lease、验真、交易号校验与 CAS。 |
| `backend/internal/service/gateway_count_tokens.go` | 本地 URL 脱敏与官方凭据/协议 404 保护同时保留。 |
| `backend/internal/service/gateway_forward.go` | 官方共享 SSE 错误 helper 与本地 partial usage、脱敏同时保留。 |
| `backend/internal/service/gateway_usage_billing.go` | 保留 `ProviderCostRecorded`、成本量化及 probe 成本归属，合入 service_account 配额和官方日/周提示。 |
| `backend/internal/service/antigravity_gateway_service.go` | 官方 Images 与本地 StatusCode、限流 trace 双保留。 |
| `backend/internal/service/admin_service_group_test.go` 及支付/网关测试 | 保留双方测试意图；补本地 AdminScope、WorkspaceService 构造参数和严格身份夹具。 |
| `backend/internal/payment/provider/easypay_query_test.go` | 双方查询格式用例与定制严格成功码、身份校验回归均保留；按 B6/B9 补精确数值和 MD5 签名夹具。 |
| `.github/workflows/*`、`backend/go.mod`、前端依赖 | 官方版本/依赖升级优先，保留定制额外配置。没有工具依赖污染 go.mod/go.sum。 |
| 三个 `ChannelMonitorView` 前端 spec | 补测试所需 Pinia 初始化，恢复隔离挂载；不改渠道监控业务行为。 |
| `backend/internal/handler/gemini_image_generation_gate_test.go / runGeminiImageGateEndpoint` | 补齐本地 Gemini 兼容转发服务依赖；不改变生产 gate。 |
| `backend/internal/service/account_quota_service_account_test.go`、`gateway_usage_billing_account_quota_test.go`、`usage_billing_quantize_test.go` | 测试参数补 6 处 `ProviderCostRecorded: true`，与真实成本归属匹配；保留 follower=false 的防重复计费回归。 |

Ent schema 与 Wire 输入没有变化，生成文件无差异，没有手改生成代码。

## B 类决定及落实

用户批复：`11 21 31 41 51 63 73 83 92 101 111`。

| 项目 / 文件与函数 | 用户决定 | 最终落实 |
|---|---|---|
| B1 `service/account.go / GetModelMapping、PrepareSharedSnapshot` | ① | 原子只读缓存 + 官方无分配签名；保留 platform/Google One/config/runtime 等失效维度，快照预热不回写对象内缓存。 |
| B2 `repository/scheduler_cache.go / GetSnapshot` | ① | 官方 memo/revision/10 秒上限 + 本地 metadata version/ID 校验、WorkspaceID、旧格式本地投影且不写 Redis。旧写者不推进 revision 的最长 10 秒延迟是已接受边界。 |
| B3 `gateway_handler_chat_completions.go / ChatCompletions`、`gateway_handler_responses.go / Responses` | ① | 失败统一 billingSettlementGuard，成功 MarkSettled；probe leader 同步持久化后发布复用结果。吸收官方 drain、SSE 终止帧，保留 error event 的真实 usage，避免双记账。 |
| B4 `gemini_messages_compat_service.go / Forward、ForwardNative`、`gemini_chat_completions_compat_service.go / forwardClaudeBodyAsChatCompletions` | ① | 保留 result+error 与真实 usage/images；吸收客户端断开、finished、实际图片数保护。兼容流剥除图片时不标为真实已投递，防止图片-only 且无 usage 的读错误误触发估算扣费。 |
| B5 `gemini_messages_compat_service.go / Forward`、`gemini_compat_thinking.go` | ①，检查开关 | 已有 `ThinkingSignatureRetryEnabled`、`ThinkingToolDowngradeRetryEnabled`，均默认 false，直接复用。清理历史 thinking 后保留原请求 thinking/effort/includeThoughts；无有效变化不重试，工具降级仍受开关控制。 |
| B6 `payment/provider/easypay.go / queryOrder` | ③ | POST 请求同时携带 URL query 与表单参数，兼容 PHP 标量；保留严格成功码、真实 pid/out_trade_no/trade_no。UseNumber 避免浮点舍入将非精确成功码误当 1。 |
| B7 `payment_webhook_handler.go / handleNotify`、`payment_fulfillment.go / HandleVerifiedPaymentNotification` | ③ | 使用真实验签 provider，按订单状态最多查单一次；保留交易号核验、V2/reconcile/admin retry 和完成/未知订单 ACK。 |
| B8 `payment_refund.go / PrepareRefund、ExecuteRefund、QueryAndFinalizeRefund` | ③ | V2 保留 immutable intent；普通支付宝/Stripe pending 保留权益扣减、只查不重发，失败事务内归还；归还失败则回滚事务并记录 `REFUND_ROLLBACK_FAILED`。订阅归还后在事务提交后再次失效缓存，避免提交前旧值回填竞态。 |
| B9 `payment/provider/easypay.go / VerifyNotification` | ② | legacy 参与签名的非空参数值（含 name）均拒绝 `&`、`=` 和控制字符，强制 MD5、拒重复参数；保留合法名称正例。 |
| B10 `admin_group.go / CreateGroup` 及生图 gate | ① | 接受 Gemini/Antigravity 创建分组的官方默认和原文迁移，保留定制权限 scope。 |
| B11 `dto/mappers.go / AccountFromServiceShallow`、`account.go / SupportsAccountQuota` | ① | 接受 service_account 配额及 quota_* 可选字段；新增 App DTO 回归，保留原 id/type/platform/status/error/runtime/token 字段契约。 |

### B4 扣费比较

两方案都有扣费路径，同真实用量、图片数、价卡和倍率时通常同价。①可能在以下条件扣得更多，但没有固定倍数：

- 已切号且 `ForceCacheBilling=true`：②成功结算可能携带该标志，将普通输入转缓存读取；①失败 sink 沿用现有策略不带该标志。在按 token 计价、缓存读取单价更低时①更多，差额来自这部分输入的单价差。纯按张图片或按次定价通常不受此转桶影响。
- 普通非取消读错误：①保留真实 usage；原样官方可能丢 result，产生漏计/估算差异。不能在缺少真实用量时断言固定高低。
- 无真实 usage：仍由原有 `failure_billing_upstream_usage_only` 开关和失败规则决定，②也不是一律免费。

成功/失败均实际调用 RecordUsage；旧“成功 Gemini 必然 long-context 2x”注释已纠正，没有据此改计费政策。未读取生产价卡或在线实测金额。用户追问未改选，仍按 B4=①落实。

## 迁移与 App 保护面

- 原有 320 个 SQL Git blob 全部不变，无改名、改内容、重编号。
- `246_dashboard_hourly_invalidation.sql` 与官方 `246_enable_gemini_image_generation_groups.sql` 原名原文并存。
- 新增 `TestDuplicate246MigrationsHaveStableDictionaryOrder`，锁定 dashboard 在 enable 之前执行；与原 238 用例同输入验证。
- `admin/me_handler.go`、`server/routes/admin.go`、`docs/API_ERROR_CODES.md` 与原 main blob 一致，adminMeCapabilities 与稳定 reason 文档未改。
- 根目录三个未跟踪对象未读取内容、未修改、未暂存；没有裸 stash、force push、main 合入、部署或服务器操作。

## 验证结果

首轮 go build 因本机 `runtime: cannot allocate memory` 退出 1；降低 Go 并发后构建通过，最终又验证一次。不是将首轮失败记为通过。

首轮前端全量有三个渠道监控测试文件缺少 Pinia 初始化，补齐测试环境后重跑全量通过。首轮后端 unit 输出也完整保留：新增配额测试漏设 `ProviderCostRecorded` 成本归属字段、网关放行测试未构造兼容转发依赖，以及在最后支付修正前启动的旧测试二进制。相关问题逐项定向复验后，再对最终源码执行下列完整检查；没有通过删测试或改计费政策来消除失败。

### backend-build-final

目录：`G:/weixinProject/sub2/backend`；命令：`go build ./...`；exit=0。

stdout：空。

stderr：空。

### backend-vet

目录：`G:/weixinProject/sub2/backend`；命令：`go vet ./...`；exit=0。

stdout：空。

stderr：空。

### backend-vet-unit

目录：`G:/weixinProject/sub2/backend`；命令：`go vet -tags unit ./...`；exit=0。

stdout：空。

stderr：空。

### backend-vet-integration

目录：`G:/weixinProject/sub2/backend`；命令：`go vet -tags integration ./...`；exit=0。

stdout：空。

stderr：空。

### backend-unit

目录：`G:/weixinProject/sub2/backend`；命令：`go test -tags unit ./...`；exit=0。

stdout 原样尾部：

```text
?   	github.com/Wei-Shaw/sub2api/internal/testutil	[no test files]
?   	github.com/Wei-Shaw/sub2api/internal/util/httputil	[no test files]
ok  	github.com/Wei-Shaw/sub2api/internal/util/logredact	(cached)
ok  	github.com/Wei-Shaw/sub2api/internal/util/responseheaders	(cached)
ok  	github.com/Wei-Shaw/sub2api/internal/util/urlvalidator	(cached)
ok  	github.com/Wei-Shaw/sub2api/internal/web	(cached)
ok  	github.com/Wei-Shaw/sub2api/migrations	(cached)
ok  	github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1	(cached)
```

stderr：空。

### backend-fulltest

目录：`G:/weixinProject/sub2`；命令：`C:/Program Files/Git/bin/bash.exe G:/weixinProject/sub2-testenv/pw/fulltest.sh`；exit=0。

stdout 原样尾部：

```text
tags=unit: ok=60 failedTests=0 failedPkgs=[]
== integration failures not in baseline:
== end
```

stderr：空。

### frontend-typecheck

目录：`G:/weixinProject/sub2/frontend`；命令：`C:/Program Files/nodejs/npx.cmd vue-tsc --noEmit`；exit=0。

stdout：空。

stderr 原样尾部：

```text
npm warn Unknown project config "manage-package-manager-versions". This will stop working in the next major version of npm. See `npm help npmrc` for supported config options.
```

### frontend-vitest-final

目录：`G:/weixinProject/sub2/frontend`；命令：`C:/Program Files/nodejs/pnpm.cmd exec vitest run --maxWorkers=4 --minWorkers=1`；exit=0。

stdout 原样尾部：

```text
 ✓ src/i18n/__tests__/wsModeLocaleDesc.spec.ts (1 test) 1ms
 ✓ src/views/admin/__tests__/GroupsView.compositePlatforms.spec.ts (1 test) 1ms

 Test Files  441 passed (441)
      Tests  3448 passed (3448)
   Start at  03:49:41
   Duration  185.14s (transform 28.50s, setup 87.14s, collect 96.28s, tests 37.65s, environment 283.22s, prepare 65.58s)

```

stderr 原样尾部：

```text
    status: 'active'
  }
]  ... > 
  at <Anonymous> 
  at <Anonymous> 
  at <ProxiesView ref="VTU_COMPONENT" > 
  at <VTUROOT>

```

本机测试库实际检查：

```text
127.0.0.1:55432 - accepting connections
```

因此执行了用户指定的 `fulltest.sh`。脚本管道会掩盖退出状态，另解析 unit JSON 并独立捕获 repository integration 的完整输出和真实退出码，不以脚本 exit=0 代替全量通过。

`fulltest.sh` 的集成顶层失败 22 个，与脚本基线完全一致。第一次独立 integration 复用了脚本留下的测试库，另出现两个顶层失败：`TestGatewayRoutingSuite` 受残留 `usage-billing-sa-quota-*` 账号影响，期望 2 条却得到 3 条；`TestUsageLog_UpstreamModelMismatchFilterAndPartialIndex` 的功能断言通过，但 EXPLAIN 选择 created_at 索引，未选断言要求的索引。原始失败输出已保留为外部 `prior-reused-db-*` 记录。

随后只对脚本明确指定的本机 `127.0.0.1:55432/sub2api_test` 执行 DROP DATABASE / CREATE DATABASE，重新运行同一独立 integration。上述两个额外失败均未再现；下方数字取此次干净测试库结果。`TestBatchSnapshotUsage_OverwriteMultiKey` 最后也未再现，不能据此宣称已修复。复用数据和执行计划敏感性的只读复核见外部 `integration-reuse-review.md`；未为此改变生产源码或测试断言。

完整 unit JSON：60 个 package 通过，26410 条含子测试的 pass 事件，失败 package/test 均为 0。

独立 integration exit=1，21 个顶层失败；相对基线新增顶层失败 0。已知失败如下（原样名称）：

```text
--- FAIL: TestAccountRepoSuite
--- FAIL: TestApiKeyCacheSuite
--- FAIL: TestBillingCacheSuite
--- FAIL: TestBlueGreenConcurrencyStartupPreservesLivePeersIntegration
--- FAIL: TestBlueGreenConcurrencyStartupPrunesExpiredSlotsIntegration
--- FAIL: TestBlueGreenSchedulerIncompleteLegacyMetadataIntegration
--- FAIL: TestBlueGreenSchedulerMixedMetadataIntegration
--- FAIL: TestConcurrencyCacheSuite
--- FAIL: TestEmailCacheSuite
--- FAIL: TestGatewayCacheSuite
--- FAIL: TestGeminiTokenCacheSuite
--- FAIL: TestIdentityCacheSuite
--- FAIL: TestOpsOutputTPSDistribution
--- FAIL: TestRedeemCacheSuite
--- FAIL: TestSchedulerCacheGroupLifecycleLeaseOwnerAndTTLIntegration
--- FAIL: TestSchedulerCacheRetireAndReopenFencesOldEpochIntegration
--- FAIL: TestSchedulerCacheSnapshotUsesSlimMetadataButKeepsFullAccount
--- FAIL: TestSchedulerSnapshotOutboxReplay
--- FAIL: TestSessionLimitCacheSuite
--- FAIL: TestUpdateCacheSuite
--- FAIL: TestUserMsgQueueCacheSuite
```

独立 integration stdout 原样尾部：

```text
            	            				G:/weixinProject/sub2/backend/internal/repository/user_msg_queue_cache_integration_test.go:25
            	            				H:/go/pkg/mod/github.com/stretchr/testify@v1.11.1/suite/suite.go:188
            	Error:      	Expected value not to be nil.
            	Test:       	TestUserMsgQueueCacheSuite/TestReconcileExpiredLockCandidatesRemovesNaturallyExpiredLockIndex
            	Messages:   	Redis tests require the Docker harness or SUB2API_TEST_REDIS_URL
FAIL
FAIL	github.com/Wei-Shaw/sub2api/internal/repository	53.404s
FAIL
```

定向回归另外覆盖缓存并发（race）、metadata 滚动升级、失败计费一次性/probe 屏障、Gemini 图片/thinking/retry 开关、EasyPay 严格身份与签名、回调单次查单、V2 intent 和 pending 退款。各模块命令、首轮错误及修正后输出保存在外部 implementation 记录，未删除失败证据。

## 未解决或后续跟进

- 集成测试仍存在上列本机基线失败，不能宣称集成全部通过；按指定脚本的顶层测试名称比较没有新增失败，不能据此证明各失败测试的所有子场景均与历史相同。环境和既有断言的具体原因见完整 integration 日志。
- 官方 v0.2.18 标签内 VERSION 为 0.2.17；本次遵守“版本文件官方优先”保留。
- B4 缓存计费成功/失败差异沿用既有策略；如需统一优惠，另行决定计费政策。
- 旧支付宝退款缺少可查询编号时仍可能返回 `REFUND_QUERY_UNSUPPORTED`，需人工核对，不能猜状态或重发。
- 升级分支完成后仍需用户自行评估发布；本任务不部署。

## 四角色回退验证工件

固定目录：`G:/weixinProject/sub2-upgrade-v0218-artifacts`。

- `MODIFIED_FILE.tar.gz`：最终 tracked 源码快照。
- `DIFF_FILE.patch`：从原 main 重建修改副本的 binary/full-index patch。
- `VERIFICATION.txt`：命令、输入、literal stdout/stderr、exit、SHA256、检查与状态转换证据。
- `ROLLBACK.sh`：只接受匹配的独立源码副本，利用同目录 pristine baseline 和 manifest 恢复；拒绝含 .git 的真实 checkout。

同输入迁移顺序检查：BASELINE 仅 238、MODIFIED 238+246、ROLLBACK 再次仅 238；实际回退执行后逐文件 SHA256 等于 pristine。最终实测输出以 VERIFICATION.txt 为准。回退未作用于真实仓库或数据库。
