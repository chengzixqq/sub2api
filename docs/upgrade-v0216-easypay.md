# 本地魔改版升级 v0.2.16 / 易支付 V1、V2

本次只交付本地源码。未部署生产，未切换支付实例，未发起真实充值或退款。

## 版本与保留范围

- 官方版本固定为 `v0.2.16`，提交 `01842576b3d5c1e2545b5a99ad555d36e28ed306`。
- 魔改基线为 `40bee5d2e39393c9d0edf9f9079fc4e4e00af50d`，另保留原工作区的 9 个图表文件。
- 工作分支：`codex/upgrade-v0216-easypay`。原 `main` 工作区及未提交修改保持原样。
- 官方标签中的 `backend/cmd/server/VERSION` 仍为 `0.2.15`。本分支改为 `0.2.16`，使非标签构建也显示正确版本。

## 冲突处理

预检预期 9 个冲突文件，实际合并出现 8 个；`backend/ent/mutation.go` 被 Git 自动合并，随后仍随整个 Ent 输出重新生成校验。

| 文件/行为 | 处理结果 |
| --- | --- |
| `gateway_handler.go` | 同时保留本地 `probeCoalescer` 和官方 `geminiCachedContentService`。 |
| `gateway_usage_billing.go` | 同时识别 `probe:`、`gcache:`。探测跟随请求仍计用户费用，但不重复记上游成本、账户配额和最后使用时间。 |
| 账户成本与缓存存储费 | 仅在 `ProviderCostRecorded && !isCacheStorageOnlyResult(result)` 时应用请求定价；按 token 部分计算账户定价后加回一次存储费。纯存储续期保留官方计费。 |
| `gateway_usage_billing_request_id_test.go` | 保留探测和 Gemini 缓存两组请求 ID 回归。 |
| `billing_service_test.go` | 保留魔改 Sonnet 5.5 和官方 Haiku 5.5 定价回归。 |
| `EditAccountModal.vue` | 同时保留 Grok 媒体类型和官方临时不可调度作用域。 |
| `MonitorSettingsPanel.vue` | 保存失败保留草稿，409 锁定保存并提示重新载入；吸收新增平台选择、去重及未知平台保留逻辑。 |
| 监控测试 | 保留本地草稿测试；官方平台测试单列文件，失败保存断言改为已批准的保留草稿行为。 |
| `wire_gen.go` / Ent 输出 | 从合并后的定义生成，不手工修改生成结果。Ent 在 Windows 并发生成遇文件映射占用后，改在隔离目录单线程生成；完整生成结果与合并输出一致。 |
| 自动合并后编译兼容 | 搜索附加费测试补齐官方新增的订阅计费布尔参数，值为 `false`，保留原测试语义。 |
| 本地权限与官方测试 | 官方分组译文测试补显式管理员作用域；Gemini 计费测试补本地 WorkspaceService 构造参数。保留权限检查。 |
| 分组译文授权 | 新增 `I18n` 沿用名称、描述的 GroupOps 限制；仅有 GroupBilling 权限时剥离译文修改并记录审计。 |
| 合并清理与静态检查 | 重复的消息级 `output_config` 清理合并为一次；仅供 unit 测试使用的探测 helper 移至对应测试文件，修复基线已有的 unused 静态检查失败。 |

历史 SQL 迁移保持原名、原文。新增官方 `242_group_i18n.sql`、`243_gemini_cached_contents.sql`、`244_gemini_cached_contents_channel_usage.sql`、`245_gemini_cached_contents_mutation_lease.sql`。本地 `242_drop_platform_check_constraints.sql` 保留；迁移以完整文件名标识，不是单独以数字标识。

逐文件校验确认 9 个图表文件及 304 个历史 SQL 文件与备份 SHA256 完全一致；4 个新增迁移与官方文本一致。

## 配置与兼容范围

`provider_key` 保持 `easypay`。缺省 `apiVersion` 为 `v1`，旧配置继续使用 MD5。V2 使用 `apiVersion=v2`、商户 `privateKey`、平台 `publicKey`，不回退 MD5。RSA 支持 PEM 或 Base64 DER，至少 2048 位；使用 SHA256、PKCS#1 v1.5 和标准 Base64。

V2 支持：

- 页面模式：`api/pay/submit`。
- API 模式：`api/pay/create`，固定 `method=jump`，只接受 HTTP(S) 跳转，不执行 HTML/JSAPI。
- 查单、退款、退款查询：`api/pay/query`、`api/pay/refund`、`api/pay/refundquery`。
- 成功响应和回调验签，时间戳允许正负 300 秒；成功码严格为整数 `0`。回调保留签名覆盖的标量扩展字段，拒绝重复参数。
- 网关基址中的 `GatewayV2/` 路径保留。V1 CID 不转换为 V2 的子商户 `channel_id`。

推荐为 V2 新建实例。已有订单的实例禁止切换协议；旧订单按照原实例和协议快照处理，历史缺省协议为 V1。停用实例不影响按 ID 绑定的旧订单核查。密钥不进入订单快照，列表、创建和更新响应统一隐藏凭据；留空更新保留原密钥。现有配置存储方式未改造为新的加密存储方案。

V1 未发现足以认定近期协议变更的证据。本次修复：

- `v1RefundSuccessCode` 缺省 `1`，可按服务商文档明确设 `0`；只匹配所选值，拒绝 `1.5` 等非法值。
- 接受 `urlscheme` 中的 `alipay://`、`alipays://`、`weixin://`，保留移动端 `payurl2` 优先级；没有可用入口时返回错误。
- 前端初次跳转、订单恢复、重开支付窗口及二维码备用链接统一拒绝危险协议。

## V2 退款恢复

在请求发送前，事务持久化 `REFUND_INITIATED` 和待确认状态，冻结原实例、商户退款号、网关金额、原订单与本地扣减策略。退款号由原实例和订单身份稳定派生，不随重试变更。

超时、无效签名、连接中断或无法确认的响应保留待确认。重复操作和重启恢复查询同一退款号，不重新提交退款；金额与扣减策略不可更改。确认网关已退款的事实独立持久化，余额或订阅结算在本地事务中完成。本地结算失败后仍可恢复，不能再次退钱。未知结果期间保留用户原权益；确认后按冻结策略扣减。

若进程在意图提交后、HTTP 发出前崩溃，系统仍只查原退款号，不猜测重新发送；网关持续无法查到时需人工核对后处理。此选择优先保证不重复退款。

## 文档来源与验收边界

- V1 文档：<https://hfy.huifuy.cn/doc_old.html>
- V2 文档及 SDK：<https://pay.epayx.cn/doc/sdk.html>
- SDK 2.0 SHA256：`307a5ea75e27c81096b2825acdc2a093d86026858a932d07024fb709c602e1ff`。

这些是公开运行网关提供的标准文档/SDK，并非已独立确认的软件原作者发布记录。按已批准假设适配聚付通 GatewayV2；真实商户通道尚未联调验收。本地模拟通过只证明协议实现与所采用的标准一致。

## 验证与恢复

完整命令、输入、原始标准输出/错误、退出码及文件哈希保存在交付包 `VERIFICATION.txt` 和 `checks/`。交付包同时包含 `BASELINE.tar.gz`、`baseline-manifest.json`、`history.bundle`、原未提交差异，以及 `MODIFIED_FILE.tar.gz`、`DIFF_FILE.patch`、`ROLLBACK.sh`。

`ROLLBACK.sh` 面向解压后的独立源码副本，恢复原始文件并验证 SHA256；不回滚生产数据库、容器或支付实例。应先在副本上执行恢复，不能把源码恢复当作数据库降级方案。原开发目录未被替换。

源码恢复示例（Git Bash / POSIX shell，需 Python 3.9+）：

```sh
mkdir restored-source
tar -xzf MODIFIED_FILE.tar.gz -C restored-source
bash ROLLBACK.sh "$(pwd)/restored-source"
```

脚本先核对目标副本的完整文件树和基线归档哈希，拒绝带 `.git` 的目录、符号链接或与交付版本不一致的副本。确认后恢复原始源码，并再次逐文件验哈希。`DIFF_FILE.patch` 应用在 `BASELINE.tar.gz` 解压后的副本上；验证记录包含重建结果与升级归档完全一致的证明。
