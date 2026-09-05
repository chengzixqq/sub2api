# 工作区(Workspace)机制 — 供应商委派管理

## 背景

站长一个人无法维护大量上游账号池。目标是把一部分账号池/分组的日常维护(加号、换凭证、测活、清错、配代理)委派给员工或供应商,同时保证:

- 供应商之间互不可见,只能碰自己的资源
- 终端用户、API Key、余额、支付仍由站长统管,供应商看不到客户名单
- 分组的商业参数(倍率、限额)不被供应商随意改动
- 网关调度行为零变化

现状:`用户 → API Key → 分组(Group) → 上游账号(Account)`,管理端是单一全局视图,角色只有 `admin`/`user`(`backend/internal/domain/constants.go:14`),无任何工作区/租户概念。

### 关键前置发现:调度器已具备所需能力

`account_groups` 关联表本就带 `priority` 字段(`backend/ent/schema/account_group.go:33`),而 `sortAccountsByPriorityAndLastUsed` + `shuffleWithinPriority`(`backend/internal/service/gateway_scheduling.go:1586`、`:1734`)的语义正是"priority 升序优先、同级随机平均"。

调度按 `groupID` 取账号,与"谁维护该账号"无关。因此工作区可做成**纯管理面概念,零侵入调度热路径**。要新增的只是"该 priority 由谁填" —— 站长在授权分组给某供应商时定下基准值,供应商加账号时自动继承且不可改,避免多家互卷抢流量。

## 需求与约束

| 项 | 决策 |
|---|---|
| 定位 | 委派管理,非多租户 |
| 供应商账号 | 复用现有用户体系,新增 `vendor` 角色 |
| 权限挂点 | 工作区级(同工作区成员权限相同) |
| 权限档 | 账号管理 / 分组运维 / 分组计费 / 代理 / 监控只读 |
| 用户与 API Key | 不归属工作区,站长统管 |
| 账号归属 | 单一归属(`accounts.workspace_id`) |
| 分组归属 | 多对多,用授权表表达 |
| 优先级 | 站长在授权时给定基准值,供应商不可改 |
| 共享分组可见性 | 各供应商只看得到自己的账号 |
| 共享分组配置 | 只有站长能改 |
| 账号↔分组关联 | 只有站长能跨区操作 |
| 存量数据 | 全部归入 `workspace_id=1`「站长直管」,admin 仍看全局 |
| 分组上架 | 供应商新建分组默认不对外可见,需站长确认 |
| 凭证 | 供应商仅可读写**自己**账号的凭证;别家凭证原文不可见 |
| 使用记录 | 供应商只能看自己账号产生的用量 |
| 操作记录 | 站长可总览旗下所有工作区的操作记录 |
| 成本结算 | 成本倍率挂 (工作区×分组),供应商可改但不得高于站长设的上限 |
| 供应商侧金额 | 只显示按成本倍率算出的成本金额,不显示官方原价与用户实付 |

## 数据模型

### 新表 `workspaces`

```
id, name, description, status, sort_order
perm_account_manage  bool  -- 账号增删改、测活、刷新
perm_group_ops       bool  -- 分组运维(模型路由、支持模型、降级、RPM)
perm_group_billing   bool  -- 分组计费(倍率、限额、图片/视频定价)
perm_proxy_manage    bool  -- 代理配置
perm_monitor_view    bool  -- 渠道监控/用量看板(只读)
created_at, updated_at, deleted_at
```

预置 `id=1`「站长直管」,不可删除。沿用 `mixins.TimeMixin` + `mixins.SoftDeleteMixin`。

### 新表 `workspace_members`

复合主键 `(workspace_id, user_id)`。成员是普通 `users` 记录,`role='vendor'`。

### 新表 `workspace_group_grants`

解决分组多对多的核心表:

```
workspace_id, group_id       -- 复合主键
base_priority        int     -- 站长给定,写入 account_groups.priority
cost_rate_multiplier decimal(10,4)  -- 成本倍率(结算口径),如 A 家 0.05
cost_rate_max        decimal(10,4)  -- 站长设的上限,供应商改不得超过
enabled              bool
created_at
```

一个分组可授权给多个工作区(A、B 同供组 1);一个工作区可拿到多个分组(C 维护组 1、2)。**分组表不加 `workspace_id`** —— 归属非单一,单字段表达不了。账号则相反,一个账号只可能由一家提供,单字段足够,这也让"各自只看自己的账号"退化成一句 `WHERE workspace_id = N`。

`cost_rate_multiplier` 是结算核心:A 家在组 1 按 0.05x、B 家按 0.06x。供应商在自己工作区可下调(主动降价),但不得高于 `cost_rate_max`,防止单方面抬价。供应商新加账号自动继承,与 `base_priority` 机制一致。

### 存量表加列

- `accounts.workspace_id` — `NOT NULL DEFAULT 1`,加索引
- `proxies.workspace_id` — 同上
- `groups.listed` — `bool DEFAULT false`,上架标记
- `audit_logs.workspace_id` — 可空(站长自身操作无工作区归属)

`NOT NULL DEFAULT 1` 而非可空:免去所有权限判定里的 NULL 分支,存量数据无需搬运。

## 权限与访问控制

### 角色

`backend/internal/domain/constants.go` 新增 `RoleVendor = "vendor"`。复用现有 JWT、2FA、会话绑定、审计,不新建认证链路。

### 中间件 `VendorScope`

新增 `backend/internal/server/middleware/vendor_scope.go`,挂在 `admin` 组鉴权之后(`backend/internal/server/routes/admin.go:22` 一带)。解析作用域入 gin context:

- `admin` → `{unrestricted: true}`,行为与现状完全一致
- `vendor` → `{workspaceID, perms}`,由 `workspace_members` + `workspaces` 查出,带缓存

现有 `AdminOnly()`(`backend/internal/server/middleware/admin_only.go`)不动;`validateJWTForAdmin` 的 `user.IsAdmin()` 检查需放宽以容纳 vendor,准入由白名单收口。

### 路由白名单

管理端有 35+ 路由组、账号一组即 50+ 端点,不逐个改造。采用**默认拒绝 + 白名单**:仅显式列出的端点对 vendor 开放,其余 403。新增功能默认关闭,不会因遗漏而漏权。

| 权限档 | 开放端点 |
|---|---|
| `perm_account_manage` | `/admin/accounts/*`(排除导出)、各平台 OAuth 授权端点 |
| `perm_group_ops` | `/admin/groups/:id` 运维字段 |
| `perm_group_billing` | `/admin/groups/:id` 计费字段 |
| `perm_proxy_manage` | `/admin/proxies/*` |
| `perm_monitor_view` | `/admin/channel-monitors/*`、`/admin/accounts/:id/stats` 等只读 |
| 恒开(无需开关) | `/admin/usage-logs`(作用域过滤后的自家用量)、`/admin/workspaces/me`(读自己的授权与成本倍率) |

`/admin/accounts/data`(导出,现要求 step-up 2FA)整体排除在 vendor 之外。

### 分组运维/计费两档落地

`groups` 的 PUT 是单一大接口,字段混杂两类。不拆接口,改为 **service 层按权限过滤入参**:白名单字段集,vendor 提交无权字段则丢弃(不报错,避免前端体验割裂)并记审计。

计费类字段约 12 个:`rate_multiplier`、`peak_*`、`*_limit_usd`、`image_price_*`、`video_price_*`、`batch_image_*`;其余归运维类。

### 数据作用域

全部在 service 层入口做,**不下沉 repository**(`account_repo.go` 3688 行不动):

- 读账号/代理:列表查询追加 `workspace_id = N`
- 写账号/代理:先 `GetByID` 校验归属,不符返回 404
- 分组:仅能操作授权表中 `enabled` 的分组

### 共享分组详情的数据隔离

共享分组(同时授权给 A、B)的详情页会列出组内账号,这是**唯一会漏出别家数据的地方**。处理:vendor 视角下,分组详情的账号列表按 `workspace_id` 过滤,只列自己的号。别家账号既不出现、凭证原文更无从读取 —— 不需要额外的掩码逻辑,过滤即隔离。

### 硬不变量

1. **账号↔分组关联只有站长能改。** vendor 调 `BindGroups`/`AddToGroup`(`account_repo.go:1657`、`:1707`)时,目标分组必须在自己的授权表内,且 `priority` 强制取 `base_priority`,忽略传入值。跨区关联一律拒绝。
2. **共享分组的计费配置只有站长能改。** 分组被授权给 ≥2 个工作区时,`perm_group_billing` 自动失效(A 改倍率会直接影响 B 的结算)。在 service 层判定,不依赖手工开关。
3. **vendor 可读写凭证,但仅限 `workspace_id` 属于自己的账号。** 由上述写路径归属校验覆盖(`Update`、`UpdateCredentials`、`BatchUpdateCredentials` 同路径)。

### 上架门

`groups.listed` 默认 false。未上架分组不出现在可领取分组列表与 `/v1/models`。站长在后台确认后生效。

## 成本结算口径

现有链路已具备成本概念:`gateway_usage_billing.go:174` 处 `accountCost = total_cost × account_rate_multiplier`,倍率取自 `account.BillingRateMultiplier()`(`service/account.go:155`),并作为快照写入 `usage_logs.account_rate_multiplier`。

改动一处:计费时倍率的取值优先级改为 **`workspace_group_grants.cost_rate_multiplier`(按该账号的 workspace_id + 本次请求的 group_id 查) → 回退 `account.rate_multiplier` → 回退 1.0**。取到的值照旧写入 `usage_logs.account_rate_multiplier` 快照,历史账单不因后续调价而漂移。

这是本方案**唯一触及计费路径的改动**,需带缓存(授权表数据量小、变更极少)以免每请求多一次查询。调度路径仍然零改动。

### 供应商侧用量视图

复用 `usage_logs`,不新建表。vendor 调 `/admin/usage-logs` 时:

- 作用域:`account_id IN (SELECT id FROM accounts WHERE workspace_id = N)`
- 金额字段:只返回 `account_rate_multiplier` 与按它算出的成本金额;`total_cost`(官方原价)、`actual_cost`(用户实付)在序列化层剔除

裁剪做在序列化层而非前端,意味着这些接口对 admin 与 vendor 返回不同结构。前端需按角色处理字段缺失 —— 这是有意为之:让原价永远不进入 vendor 的响应体,比在前端藏起来更可靠。

站长侧视图不变,仍可见全部三档金额,便于核对毛利。

## 供应商操作记录

`audit_logs` 表已有 `actor_user_id`、`actor_role`、`action`、`method`、`path`、`status_code`、`extra`,`AuditLogFilter`(`service/audit_log.go:64`)已支持按操作人/动作/时间筛选。因此不新建表:

- 写入时从 `VendorScope` 取 `workspace_id`
- `AuditLogFilter` 加 `WorkspaceID *int64`,`buildAuditLogsWhere`(`repository/audit_log_repo.go:125`)加对应条件
- 站长侧新增「供应商操作记录」页:总览旗下所有工作区,可按工作区、操作人、动作、时间筛选

vendor **不可** 访问该页(不在白名单内),避免互相窥探操作。

## 界面

**admin 视角**:保持不变,不需要切工作区。新增入口:

- 侧边栏「工作区」页:列表、五个权限开关、成员管理、分组授权(选分组 + 填 `base_priority`、`cost_rate_multiplier`、`cost_rate_max`)
- 侧边栏「供应商操作记录」页
- 账号/分组列表加「所属工作区」/「授权供应商」列;账号编辑抽屉加工作区选择器;分组列表加「上架」开关列

**vendor 视角**:同一前端应用与登录页,按角色渲染。侧边栏仅显示获授权模块。列表天然只含自己的数据,无需工作区切换器(仅一个工作区)。

- 加账号时分组下拉仅含授权分组,且隐藏 priority 输入(由 `base_priority` 决定)
- 「用量与结算」页:自家账号的用量记录,金额一律按成本倍率呈现;可查看并在上限内调整自己各分组的成本倍率

`/api/v1/auth/me` 增返 `workspace` 与 `perms`,前端据此控制菜单与按钮显隐。**前端仅做体验,拦截全在后端。**

## 错误处理与可观测

- 越权访问别家资源返回 **404**(非 403),避免通过错误码探测别家账号 ID 存在性
- 权限不足返回 403 + 权限档名称,便于供应商申请开权
- 审计:现有 `audit_log` 中间件已覆盖管理路由,vendor 操作自动入库;额外记录被丢弃的越权字段、所有分组授权变更

## 实施顺序

1. Ent schema + 迁移(建三表、加列、预置工作区 1)
2. `domain` 角色常量 + 工作区 domain 模型与 repository
3. `VendorScope` 中间件 + 路由白名单
4. service 层作用域校验与字段过滤(账号 → 代理 → 分组)
5. 成本倍率接入计费:倍率取值优先级改造 + 缓存
6. 用量视图作用域 + 金额字段裁剪;审计 `workspace_id` 写入与过滤
7. `auth/me` 扩展 + 前端(工作区管理页、操作记录页、vendor 用量结算页、按角色渲染)
8. 测试

## 测试与验证

**测试重点:**

- **作用域隔离** — A 家读不到/改不到 B 家账号;共享分组详情内仅见自己账号(凭证原文无从读取);用量记录只含自家账号;越权返回 404
- **权限矩阵** — 五个开关的开/关组合;共享分组时 `perm_group_billing` 自动失效;无权字段被丢弃且记审计;vendor 写凭证仅限自有账号;vendor 调成本倍率超 `cost_rate_max` 被拒
- **成本口径** — 同一分组内 A(0.05x)/B(0.06x) 各按自家倍率入账;倍率快照写入 `usage_logs` 后调价不影响历史账单;vendor 侧响应不含 `total_cost`/`actual_cost`
- **回归** — admin 视角行为与改造前一致;调度不受影响(现有调度测试全绿即证明)

**命令:**

```bash
cd backend && go build ./... && go test ./internal/...
```

```bash
cd backend && go test ./internal/service/ -run 'Scheduling|AccountSelection' -v
```

**端到端手验:**

1. 建工作区 A(仅给账号管理 + 监控)、工作区 B(全给)
2. 把「Claude 高级组」同时授权给 A(`base_priority=10`、cost=0.05、max=0.05)、B(=20、cost=0.06、max=0.06)
3. 以 A 的 vendor 登录:确认看不到代理菜单、看不到 B 的账号、加号后 `account_groups.priority` 为 10;打开共享分组详情确认只列自家账号
4. 以 B 登录:确认因分组被共享而无法改倍率;尝试把成本倍率调到 0.1 被拒、调到 0.055 通过
5. 以 admin 登录:确认全局视图与改造前一致,新建分组处于未上架态
6. 走一次网关请求,确认 A 家账号优先被选中、A 家内部随机分布
7. 分别用 A、B 家账号各跑几次请求,核对 A 家按 0.05x 计、B 家按 0.06x 计,两侧均看不到官方原价与用户实付
8. 以 admin 打开「供应商操作记录」页,确认能看到两个工作区的全部操作,且可按工作区筛选

**回滚:**删除新增列与新表即可,无数据搬运。
