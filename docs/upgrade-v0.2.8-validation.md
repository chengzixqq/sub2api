# 魔改版同步官方 v0.2.8：合并与验证记录

验证日期：2026-09-25。

## 基线与范围

- 本地基线：`f20846e5590c`，以及原工作区未提交的 48 项修改/新文件。
- 官方目标：[`v0.2.8`](https://github.com/Wei-Shaw/sub2api/releases/tag/v0.2.8)，提交 `fd80b08c90b55edcad5b00171b53f08721d30da1`，发布时间 2026-09-23。
- 集成分支：`codex/official-v0.2.8-sync`，使用独立 worktree。
- 原工作区未重置、未清理；48 项文件的 SHA-256 与保护清单完全一致，合并工作区无遗漏文件。
- 本次只修改源码并进行本地验证，未连接生产服务器、执行生产迁移或切换线上流量。

## 已确认的业务取舍

| 项目 | 合并结果 |
| --- | --- |
| 新版渠道监控 | 保留采集器、compact/legacy 兼容、探针、API、权限投影、设置与新工作台；未清空数据 |
| Workspace/vendor | 保留资源归属、委派和 fail-closed 授权；官方新增账号操作继续受本地权限检查约束 |
| Claude 定制 | 保留本地策略、签名兼容和账号 header 覆写 |
| 调账审计 | 保留管理员余额调整历史、幂等和入口；关闭支付时仍可查询调账历史 |
| 失败计费与防重 | 保留量化、失败请求计费、usage settlement guard、原子探针结算、供应商 follower 排除 |
| 错误处理 | 保留脱敏、监控与 timing 钩子；不恢复原始上游错误 body 或敏感 URL 输出 |
| 推理力度计价 | 采用官方 `reasoning_effort_multipliers`；Fable 5.1 max 未配置默认 1 倍，旧显式配置由新增迁移保留 |
| 超时/连接池 | 保留默认空闲超时和可选健康机制；自动上游熔断仍默认关闭，不使用全局 `Connection: close` |

## 官方发布说明覆盖

| 官方功能/修复 | 集成与验证证据 |
| --- | --- |
| GPT-6 Sol/Luna、Claude Opus 5.5、Grok 4.7 | 模型常量、目录与映射测试；Opus 5.5 不支持参数在请求改写前拒绝 |
| OpenCode Go 用量窗口 | 官方查询/刷新服务、同 Key 共享、账号管理接口、前端用量展示和模型 metadata |
| 按推理力度计价 | 各转发路径保留最终 effort；渠道配置深拷贝；显式 max 迁移、未配置默认值回归 |
| Claude Code 版本同步 | 同步服务、运行时版本与 Wire 启停连接 |
| 简易模式限额 | API Key 消费窗口可选开启；未开关时不额外扣余额；绑定规则同时保留 Workspace 权限 |
| 月度备份/日志保留 | 归档策略、恢复状态、S3 密钥保留、日志配置类型及前端设置 |
| 推荐邀请/联盟提现 | Codex 邀请接口、积分展示、线下提现操作及操作 ID 幂等迁移 |
| TypeSafe 内容审计 | 独立配置档、运行时服务、提醒标签绕过防护和尾随 system 处理 |
| 插件账号目录 | 结构化只读 metadata；剥离凭据、关系图和本地 Workspace 归属信息 |
| 工具 Schema/多协议兼容 | 非法 required、数组 schema、Anthropic/Responses 转换与 reasoning 传递回归 |
| OpenAI/Codex 图像和模型 | OAuth 图像别名、映射目标过滤、复合组兼容图片路由、图片输入 metadata |
| 上游连接与 WebSocket | 终止事件及时退出、先取消再回收响应、HTTP/2 保活；断开后保留 usage 且不误触发 failover |
| 调度/代理/订阅 | 响应归属路由、配额/倍率回退、代理恢复和到期处理、兑换不足一天余量及负向更新锁 |
| 管理界面交互 | 官方列表请求时序、IME、日期、下拉、失败重试修复与本地新监控界面共存 |

对“官方修改但最终保持本地实现”的文件另作对照审计：保留差异主要来自 Workspace、调账审计、监控、探针、脱敏和多实例并发保护。例如保留多实例启动时的槽位清理规则，避免蓝绿实例互相清除并发统计。

## 生命周期与计费回归

- 每个 HTTP 请求有独立取消上下文；响应关闭负责取消和幂等回收。
- 解压 reader 与关闭互斥；gzip、br、deflate、zstd 均覆盖普通和 TLS 指纹请求。
- scanner 终止时停止投递、取消/关闭上游、等待读取退出，之后进行 usage 结算。
- 保留按实际字节活动判断的 idle timeout，以及原生 Claude 严格帧校验、第三方兼容解析和签名保护。
- 覆盖终止帧后上游连接不立即 EOF、HTTP/1 后续请求、客户端断开、错误/超时和重复关闭。
- WebSocket HTTP bridge 在客户端已断开时保留已观测 usage，避免以 failover 丢失本轮计费结果。
- simple-mode、provider/follower、首次原子落账失败重试、客户端取消和禁止重复写 usage 均有回归。

## 数据库兼容

原有 **298 个 SQL 文件内容未变**，仅增加以下 3 个官方文件：

1. `238b_content_moderation_engine_meta.sql`
2. `239_channel_reasoning_effort_multipliers.sql`
3. `240_affiliate_ledger_operation_id.sql`

迁移器按完整文件名识别和排序，因此与本地 `238_channel_monitor_observation.sql`、`239_channel_monitor_compact.sql`、`240_channel_monitor_probe.sql` 没有同名覆盖。未重编号或改写已发布迁移。

在隔离的本地 PostgreSQL 18.1 上验证：全新库执行全部 301 个迁移；历史库先执行本地 298 个迁移、写入 10 类业务哨兵数据，再升级至 301 个迁移。两条路径最终 schema 一致，迁移重放安全，Workspace/vendor、调账、监控历史/配置/compact/探针数据保留。旧显式 max=2.5 成功迁移；未配置渠道仍无倍率覆写。

## 验证结果

| 检查 | 结果 |
| --- | --- |
| 前端全量测试 | 371 个测试文件、2,691 个测试通过 |
| 前端类型检查、lint、i18n | 通过 |
| 前端生产构建 | 通过 |
| 后端全量 unit | 通过，`go test -tags=unit ./...` |
| 公共 HTTP 层 race（含 unit tag） | 通过 |
| Claude/SSE/WS HTTP bridge 定向 race | 通过 |
| 后端 lint（默认及 integration tag） | 均通过，0 issues；测试断言整理后另补跑 168 项回归全部通过 |
| Wire 生成 | 通过，保留本地服务注入与生命周期 |
| 后端嵌入前端的生产构建 | 通过；`-version` 输出 0.2.8 |
| migrations 包 | 48 项测试通过 |
| PostgreSQL + Redis repository integration | 1,586 个测试/子用例通过；2 项原有跳过，见下文 |
| 历史库升级/schema/哨兵保留 | 通过 |
| 后端全量 integration | 54 个测试包、13,884 个测试/子用例通过；7 项原有跳过，见下文 |
| 官方发布工具 | 10 项测试通过；兼容 macOS Bash 3.2 的仓库名转小写写法，发布标签和 dry-run 行为不变 |
| CI 部署脚本检查 | Apple 容器 mock 生命周期、Compose 安全/环境/资源、Caddy 缓存与 SSE、简易模式配置解析均通过；未启动真实容器 |
| 原工作区保护、冲突标记、diff 空白检查 | 通过 |

验证工具为 Go 1.27.1、pnpm 9.15.9；保留仓库 Go 1.27 要求与原 pnpm lockfile 格式。生产构建只在本地生成，未构建或发布线上镜像。

测试代码兼容修正包括：官方新增测试补齐 Workspace 依赖与授权上下文；compact 分类断言采用现有分类字段；分页测试遵循 page size；本地集成环境统一 UTC。Redis 使用官方源码构建的 8.4.0，仅绑定 loopback、关闭持久化并使用独立测试 DB；测试适配器不清空数据库。这些调整没有放松生产权限、计费规则或 SQL 迁移校验。

全量 integration 的 7 项跳过均由仓库原有条件决定，本次没有新增 skip 或删除失败断言：

- `TestDingTalkOAuthStart_Disabled`：已有占位测试。
- `TestDialerAgainstCaptureServer`：没有配置专用指纹采集服务。
- `TestConcurrencyCacheSuite/TestGetAccountsLoadBatch`：仓库已有 TODO 跳过；其他 Redis 并发套件已执行。
- `TestUsageQuery_Profile`：没有启用可选 10 万行性能压测。
- `TestContentModerationTypeSafeLive`、`TestEstimateOpenAIInputTokens_CompareWithOpenAIAPI`：没有提供真实 API 密钥。
- `TestPluginRuntimeIntegration`：没有提供外部插件进程测试包。

因此，这次验证不代表真实生产上游、外部插件包或大规模性能压测已验收。

## 本地复核命令

```sh
# frontend：pnpm 9
pnpm run typecheck
pnpm run lint:check
pnpm exec vitest run
pnpm run build

# backend：Go 1.27
go generate ./cmd/server
go test -tags=unit ./...
go test -tags=integration ./...
golangci-lint run ./...
golangci-lint run --build-tags=integration ./...
go build -tags=embed -trimpath -ldflags='-s -w -X main.Version=0.2.8' -o /tmp/sub2api-v0.2.8-server ./cmd/server
```

集成测试必须使用独立测试数据库或仓库默认 testcontainers 环境，不能将生产 DSN 用作测试输入。

这次完整集成运行使用 `SUB2API_TEST_POSTGRES_DSN` 和 `SUB2API_TEST_REDIS_URL` 指向本地临时数据库；Redis URL 必须为 loopback 且 DB 大于 0。默认未设置这些变量时仍使用仓库原有容器测试路径。

## 第二轮源码复核与修复

复核日期：2026-09-25；复核起点为已提交的升级版本 `ae3c821ca6c821c0ee27faba80a6a7b5277916cd`。上文记录的是首轮合并验证；测试通过不代表所有边界路径都已覆盖。第二轮从生产调用链重新检查，确认并修复了以下缺口。

| 问题 | 修复及新增验证 |
| --- | --- |
| 错误响应替换 body 时遗失原 body 的关闭 | Claude 普通 400、Grok 及 Alpha Search direct/PAT 路径先关闭原 body，再安装缓存内容；验证关闭次数与错误处理结果 |
| Claude 签名重试混用了两次错误响应 | 保留实际重试的响应体、状态及请求 ID，避免第二次状态配第一次错误内容 |
| gzip/zstd 初始化早于空闲超时，可能阻塞在压缩头 | 解压 reader 延迟到首次 Read 初始化，响应头同步规范化；验证仅响应头、部分 zstd 头、并发关闭及无效压缩头完整回退 |
| 慢客户端或事件队列阻塞被误算为上游空闲 | scanner 投递事件期间暂停上游空闲计时；虚拟时间回归同时验证本地反压不误超时、恢复后真实空闲仍到期 |
| Claude 重试及错误日志漏脱敏 | 正文日志统一遵守开关，先清理 JSON 凭据、敏感 URL/查询参数，再限长；覆盖 signature/tool/budget retry、普通 HTTP、SSE error、retry exhausted |
| OpenCode Go/Ollama 用量操作可跨 Workspace，按 Key 共享也跨边界 | HTTP 层拒绝缺失管理 scope，服务层验证账号归属及读写权限；SQL 成员查询、锁、CAS、调度和 singleflight 均按 Workspace + Key 隔离；同 Workspace 别名继续共享，后台采集继续工作 |
| OpenCode Go 全局设置缺路由及 owner 写入保护 | 注册 GET/PUT 路由；全局写入校验 owner，vendor 不可修改全站开关 |
| Claude 账号定制异步串值、继承选项保存失败 | 切换/关闭时重置状态，忽略过时响应；仅成功加载且有修改时保存，提交前冻结当前账号值；继承项从稀疏 override map 中省略 |
| mixed 监控覆盖时间与延迟不一致 | 在选择最终最早数据水位之后计算延迟，避免历史覆盖已滞后但延迟显示 0 |
| 失败/部分响应结算丢失最终推理计价信息 | guard、决策和两类结算 sink 保留最终模型、推理力度及响应计费信息；保持未配置 1 倍，覆盖显式 2.5 倍、重试改 1.5 倍、重试清除 effort 和重复 Flush |
| 异步计费读取已复用的 Gin 请求上下文 | 在请求 goroutine 中捕获计费时间与请求计费字段，再提交 worker；延迟执行并清空原请求的回归验证结算不依赖已回收的请求 |

本轮未新增或改写 SQL 文件；原工作区保护清单中的 48 项文件校验和仍完全一致。所有修改仍仅在独立升级 worktree；未部署、未推送远端、未操作生产数据库。

### 第二轮阶段性验证结果（2026-09-25）

| 检查 | 结果 |
| --- | --- |
| 前端全量测试 | 372 个文件、2,697 项通过 |
| 前端 typecheck、lint、生产 build | 全部通过；构建内 i18n 3 项通过 |
| 后端全量 unit | 59 个测试包、22,000 个测试/子用例通过，无失败 |
| 后端全量 integration | 54 个测试包、13,934 个测试/子用例通过，无失败 |
| 审计专用数据库与 Redis 集成 | 另用独立空库补跑 211 个测试/子用例，零跳过；包含全量命令中缺少专用环境变量而跳过的 9 项审计测试 |
| 后端 lint | 默认及 integration tag 均 0 issues；关闭同类问题输出上限 |
| 定向 race | HTTP/解压、Claude/SSE/WS、日志脱敏、跨 Workspace 权限、失败结算全部通过 |
| 嵌入前端的后端生产构建 | 通过，版本输出 0.2.8 |
| 格式、SQL 与原工作区保护 | gofmt 和 diff 空白检查通过；301 个迁移文件未变；原工作区 48 项文件未变 |

上述测试数字包含各自套件内子用例，unit、integration 和审计补跑相互有重复，不能相加作为独立用例数。

全量 integration 原有 17 项跳过中，9 项审计测试已通过专用本地 PG/Redis 补跑。其余为首轮记录的 7 项条件跳过，以及本轮 `TestJA3Fingerprint` 因外部 `tls.peet.ws` 返回 EOF 按已有逻辑跳过；未据此声称外部指纹服务已验收。unit 另外保留原有网络开关和 SQLite 无法构造历史 NULL 行的条件跳过，未新增跳过或弱化断言。


## 第二轮最终收尾验收（2026-09-26）

本节是第二轮最后修复后的验收记录；上节保留的是 2026-09-25 的阶段性结果，不能把早于最后修复的测试直接当作最终验证。源码仍以官方 `v0.2.8` 提交 `fd80b08c90b55edcad5b00171b53f08721d30da1` 与原魔改合并提交 `ae3c821ca6c821c0ee27faba80a6a7b5277916cd` 为基础，未升级到其他官方版本。

### 最后完成的修复

- **账号缓存并发安全**：将模型映射及账号请求头缓存从共享 `Account` 实例移出，使用有界 512 槽原子快照。缓存键保留源配置、平台、Google One 状态和运行时版本差异；保留源 map 引用，避免地址复用导致缓存误命中。覆盖并发读取、按值复制、配置隔离及缓存失效。
- **测试全局状态隔离**：服务包在 `TestMain` 一次初始化 Gin 和时区；必须观察不同进程时区或进程级分配计数的测试使用同一测试二进制的独立进程，仍执行原断言且保留 race 插桩，没有通过跳过测试或提高阈值掩盖问题。
- **管理端测试夹具同步**：扩大 race 范围后发现 Grok 日志缓冲区并发读写、模拟仓库异步更新与断言读取竞态。日志改用加锁且复制的快照；仓库桩读写同锁并等待异步更新，保留原非空断言和凭据不得泄露的断言。生产实现未因此改动。
- **新增测试规范**：日志缓冲区使用私有具名字段，避免暴露未加锁方法；慢客户端回归显式处理 `strings.Builder.WriteString` 的返回值。

### 最终验证结果及覆盖口径

| 检查 | 结果 |
| --- | --- |
| 前端全量测试 | 372 个文件、2,697 项通过 |
| 前端类型检查、lint、生产构建 | 全部通过；构建内 i18n 3 项通过；输出 188 个资源文件，入口引用完整 |
| 后端全量 unit | 59 个测试包、22,004 个测试/子用例通过，0 失败；最终仅测试夹具调整后，管理端整个包 726 项另行补测通过 |
| 后端全量 integration | 54 个测试包、13,935 个测试/子用例通过，0 失败 |
| 专用审计 PostgreSQL/Redis 补测 | 211 个测试/子用例通过，0 失败、0 跳过；覆盖全量命令中的 9 项审计条件跳过 |
| 整个后端 race 及失败包修复复测 | 完整首轮 58 个测试包通过，仅管理端 2 个夹具测试失败；修复后管理端整个包 726 项 race 通过，原失败 2 项各重复 10 轮通过；其余包生产代码未变。服务包整包 race 已通过，包括账号映射缓存与分配计数隔离 |
| 慢客户端背压回归 | 测试规范修正后另跑 10 轮 race，共 30 个测试/子用例通过 |
| 默认及 integration 标签 lint | 均 0 issues，未限制同类问题输出数量 |
| 补充 unit 标签增量 lint | 本次全部改动（含新文件）0 issues；全库历史问题单列于下文，未声称 unit 标签全库 lint 通过 |
| Wire 生成及嵌入前端的后端生产构建 | 均通过；Wire 生成结果未变化；二进制版本输出 0.2.8 |
| 源码、格式及保护检查 | 原工作区 48 项 SHA-256 均未变，301 个 SQL 迁移文件未变，未解决合并冲突为 0；变更 Go 文件 gofmt、diff 空白检查通过 |

上述数量包含各套件子用例，不应相加当作独立测试总数。完整 race 首轮确实失败过，验收依据是**完整范围运行加失败包修复后的整包复测**，并非最后重新执行了一次全库零失败命令。

集成验证后对 4,467 个文件重新校验：除本报告更新外，生产源码无变化，仅最后三个 `unit` 标签测试文件调整；这三个文件已补测，且不参与 integration 构建。全库 unit 之后的代码变更也仅限这三个测试文件。最后的构建仍对应同一生产源码与前端资源。

### 保留的限制与非阻断问题

- 全量 integration 的 16 项条件跳过中，9 项审计测试由独立环境补跑覆盖；剩下 7 项为首轮已记录的钉钉占位、专用 TLS capture、Redis 并发缓存既有 TODO、10 万行查询性能压测、TypeSafe 实网、真实 OpenAI API、外部插件包条件测试。本次 `TestJA3Fingerprint` 在 integration 中通过，不再将先前外部 EOF 当作本次失败。
- unit/race 保留原有网络开关及 SQLite 无法构造历史 NULL 行的条件跳过；不据此声称真实上游、生产数据库历史数据或外部插件已验收。
- 额外开启 `--build-tags=unit` 的全库 lint 最初报告 299 项；本次新增的 3 项测试规范问题已修复。剩余 **296 项全部位于测试文件、且告警对应行在基线提交中已存在**（depguard 6、errcheck 241、gofmt 3、govet 3、staticcheck 35、unused 8），没有生产代码告警。该附加检查不同于仓库默认 CI lint；未禁用规则、未添加忽略项，也未为清理历史测试债务扩大本次变更。逐行基线对照与增量 lint 结果已归档。
- 前端构建仍有既有的 chunk 体积、caniuse-lite 数据和 Node 24 DEP0190 非阻断警告，未在升级收尾中更换依赖。

### 证据、环境与交付边界

本次使用 Go 1.27.1、golangci-lint 2.13.0、Node 24.19.0、pnpm 9.15.9。数据库验证专门新建 PostgreSQL 18.1（loopback 55439）和 Redis 8.4.0（loopback 56380），使用独立测试库；结束后已停止本次实例，未停止其他任务原有实例。

收尾前 239 项修改已备份；最终验证日志、命令、退出码、计数、源码哈希与本报告归档于项目旁的 `sub2api-sync-backups/v0.2.8-closeout-20260926-020245/`，其 `validation-summary.json` 记录最终提交，`validation-evidence.tar.gz` 保存验证证据。首次失败日志也保留，未仅保留成功输出。

本次交付是 **0.2.8 魔改源码、第二轮修复及本地回归验收**。所有提交留在 `codex/official-v0.2.8-sync`；原 `sub2api` 工作区仍保持 0.2.5 及原本未提交内容。未推送远端、未发布镜像、未部署线上、未操作生产数据库，因此不声称朋友的真实上游超时已经在线验证解决。
