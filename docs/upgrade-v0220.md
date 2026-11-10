# 魔改版升级至 v0.2.20

## 固定版本与交付边界

- 原魔改：`main@a4aa1f619c5371a744c7f6678069e9c4890b4d03`。
- 汇总修复：`3f4ccc38b4e455b538cfcde07d2c71b2110f158d`，纳入原修复工作区的全部 13 个文件。
- 官方：`v0.2.20@dab829a4e9b7d1cc307189aea8b0319eb29e29d0`，采用双亲 merge，未跟随 main。
- 升级分支：`codex/upgrade-v0.2.20`，工作区 `G:/weixinProject/sub2-v0220-upgrade`。
- 原 main、原修复工作区与历史 stash 保留。本次只交付本地源码，不包含远端推送或线上部署。

## 冲突处理

此次实际合并出现 50 个文本冲突文件，原始记录保存在交付目录的 `logs/upstream-merge.stdout`。

| 范围 | 合并结果 |
| --- | --- |
| 网关、失败计费与 probe | 保留一次性结算 guard、真实失败 usage、leader 同步落账屏障及 follower 上游成本去重；官方成功结算入口与 guard 的状态同步，失败用量不会被两个入口重复扣费。advisor 快照以深拷贝穿过失败和 probe 路径。正常成功发布 probe 结果；跨组 fallback 同步切换 key、分组、模型映射及结算 sink，且不向原组发布复用结果。 |
| 管理凭证与 MCP | 新旧凭证并存；保留 vendor、owner、工作区及二次验证边界，MCP 内部管理路由复用权限链。用户及受限管理响应对嵌套账单明细做白名单投影。MCP 默认关闭。 |
| 缓存与限流 | 合入官方新字段与减少查库的优化，保留魔改深拷贝、缓存隔离与现有限流规则。 |
| 导出和前端 | 保留冻结筛选、动态分页、取消和管理端深链；合入批量导出和 429 重试，重试等待可取消。advisor 明细独立展示及导出，不能把总费除以 executor token 当成单价。XLSX 用独立 Billing Details 工作表逐分量保存，避免多 iteration JSON 超出 Excel 单元格上限；金额保留 8 位精度及 null/0 区别。 |
| 数据库 | 官方 247–249 与魔改同编号文件按完整文件名共存，扩展乱序预检；纳入 258，新增 259。历史 SQL 不修改、不重命名。 |
| 构建 | 保留 Node 24.19.0、入口权限与后台任务清理；合并依赖并实际重新生成 Ent、Wire 和锁文件；修正官方标签内滞后的 VERSION 为 0.2.20。 |
| 图片兼容 | 保留真实图片有效性验证，并支持有界 SVG 尺寸读取；上游新增测试改用真实 PNG/GIF/SVG 样本。 |

## HTTP 断连收尾预算

```yaml
gateway:
  upstream_drain_timeout_seconds: 180
  image_upstream_drain_timeout_seconds: 900
```

显式 `0` 仅关闭对应的断连收尾总时限，负数和 duration 溢出拒绝启动。正常活跃流不受此预算影响；请求体预算、读取/写入超时、容量保护、既有流空闲超时与慢客户端暂停规则分别继续生效。

## Advisor 账单合同

- 完整 `usage.iterations` 的 `message` 逐条使用 executor 定价，`advisor_message` 使用该条实际 `model` 的 token 定价。顶层 executor usage 不再叠加，长上下文门槛按每条输入判断。
- 分解不完整时保留原 executor 结算，再加入已验证的 advisor 条目；重复累计 SSE 替换快照，缺字段或倒退的尾快照不能清空此前用量。
- advisor 使用站内分组、渠道、明确目录价格的既有优先级与倍率，跳过非 token 价卡，不继承 executor 服务档位或猜测为旧 Claude 型号。
- executor 按次计价时只收一次基础费，再加 advisor token 费。
- 声明的 advisor 模型没有明确价格时，转发前返回 `503 advisor_pricing_unavailable`。显式零价有效。
- 响应意外出现未定价模型时只结算可确定部分，未知费用为 JSON `null`、状态为 `partial_unpriced`，不自动追扣。
- 每个请求仍只有一条使用日志和一次幂等结算。旧 token 字段只表示 executor；`total_cost`、`actual_cost` 包含所有已定价分量。
- `actual_cost` 是用户扣费。账号统计继续使用 `account_stats_cost` 和历史 `account_rate_multiplier`，probe follower 不重复记录 provider 成本。

迁移 `259_usage_log_billing_details.sql` 增加可选 JSONB `usage_logs.billing_details`，无历史回填。明细版本为 1，状态包括 `complete`、`partial_usage`、`partial_unpriced`；保存模型、角色、原 iteration 索引、token、缓存 5m/1h 和费用分量。管理员明细包含内部价格来源和账号成本；用户、vendor 和受限管理输出不暴露这些内部字段。旧记录继续显示原有费用详情。

## 验证证据

完整命令、输入、原始 stdout/stderr、退出码与源码 hash 保存在交付目录 `G:/weixinProject/sub2-v0220-artifacts/VERIFICATION.txt` 及 `logs/`。最终结果另见同目录验证汇总。

本轮使用独立 PostgreSQL 18.1 和 Redis 7.2.8（Windows portable），仅监听 loopback。基线和升级测试分别使用不同数据库及 Redis DB；最终集成测试使用全新数据库，避免重复运行遗留数据影响断言。Redis 版本与仓库 Docker 默认 8.4 不同，未声称验证该镜像。

初轮基线与升级数据库测试共同暴露两类测试夹具问题：小时汇总隔离 schema 缺少 258，及 Windows 时钟粒度导致 TPS 样本 request_id 碰撞。升级版补齐真实迁移夹具、用确定序号生成样本 ID，未修改业务统计口径。并行全量检查曾触发内存不足，保留失败记录，最终改为低并发执行。

上游新增的零分配断言在全量 service 测试进程中受后台任务分配干扰；独立运行 10 次均通过。现将该测量放入独立测试子进程，保留零分配要求及全部功能断言。另为实际启用的 scoped-admin PostgreSQL 测试补齐魔改的 workspace 字段、上下文与 scoped repository stub，不降低权限断言。

最终冻结源码的后端全量 unit、integration、build、vet 均以退出码 0 完成。真实 PostgreSQL 权限专项为 103 个顶层测试、152 个子测试通过，0 失败、0 跳过；提示审计 PostgreSQL/Redis 专项 218 个测试事件通过。汇总并发发布、锁竞争、时区重建、清理回滚、迁移及 JSONB 账单往返均已实际运行。

后端 lint 仍为退出码 1：冻结基线 17 项，升级版 16 项；逐项比对无新增问题，修复了原配置文件的格式问题，其余历史问题保留并单独报告。

前端基线 lint、类型检查、441 文件/3450 项测试及构建通过。升级前端全量 456 文件/3682 项测试通过；最后 XLSX 局部修正另完成 6 文件/83 项定向测试、lint、类型检查与重新构建。全量结果与局部修正结果分别记录，没有将修正前全量结果写作修正后重跑。

全部 50 个冲突文件的逐项处置、双亲 blob 对照与原始补丁见交付目录 `CONFLICT_RESOLUTION.md` 和 `conflict-resolution-evidence.json`。完整基线失败、最终 lint 和实际跳过清单见 `VALIDATION_SUMMARY.md`。需要外部凭证或手工运行条件的测试不会被记作通过。

## 源码与数据库回退

四项交付为 `MODIFIED_FILE.tar`、`DIFF_FILE.patch`、`VERIFICATION.txt`、`ROLLBACK.sh`，位于同一交付目录。`BASELINE.tar` 是不可变的原 main 源码，`CUSTOM_BASELINE.tar` 是纳入汇总修复后的源码。

`ROLLBACK.sh <导出的源码副本目录>` 依赖同目录 `BASELINE.tar` 与 `MODIFIED_FILE.tar`，先校验 hash 和副本内容，再恢复原 main 源码。脚本拒绝带 `.git` 的真实检出目录；源码分支回退可直接切回仍保留的 main。补丁从原 main 重建最终升级源码，包含汇总修复、官方 merge 和 advisor 功能。

源码回退不撤销数据库迁移。本轮未操作线上数据库。部署时应先备份数据库；若需要数据库回退，停止新版本写入并恢复部署前备份，再启动匹配版本。不要删除 `billing_details` 或迁移记录来假装恢复，也不要重写历史迁移校验和。
