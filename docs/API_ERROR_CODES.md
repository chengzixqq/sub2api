# 管理端 / 用户端接口稳定错误码

客户端（Web、原生 App）按 `reason` 决定下一步：续期令牌、弹二次验证、引导合规确认或重新登录。下表中的 `reason` 是稳定契约，改名会让已发布的客户端失效；`TestStableErrorReasons`（`backend/internal/service/stable_error_codes_test.go`）会拦住服务层错误的改名。

## 响应格式

两种来源的错误都带字符串 `reason`，客户端统一读它即可：

- 中间件：`{"code":"TOKEN_EXPIRED","message":"Token has expired","reason":"TOKEN_EXPIRED"}`，`code` 是字符串，与 `reason` 相同。
- handler 信封：`{"code":401,"message":"...","reason":"REFRESH_TOKEN_INVALID","metadata":{...}}`，`code` 是 HTTP 状态码整数。
- 合规确认 423：`{"code":"ADMIN_COMPLIANCE_ACK_REQUIRED","reason":"ADMIN_COMPLIANCE_ACK_REQUIRED","message":"...","metadata":{"version":"...","document_url_zh":"...",...}}`。
- 登录 / 刷新限流 429：`{"code":"RATE_LIMITED","reason":"RATE_LIMITED","retry_after":12,"error":"rate limit exceeded","message":"..."}`，同时带 `Retry-After` 头；`error` 字段为兼容旧客户端保留。

建议解码方式：`code` 按整数或字符串都能解；识别用 `reason`，缺省时退回字符串 `code`。

## 错误码

| HTTP | reason | 客户端处理 |
|---|---|---|
| 401 | TOKEN_EXPIRED | 用 refresh token 续期后重试 |
| 401 | INVALID_TOKEN、TOKEN_REVOKED、USER_NOT_FOUND、USER_INACTIVE、SESSION_BINDING_MISMATCH、REFRESH_TOKEN_INVALID、REFRESH_TOKEN_EXPIRED、REFRESH_TOKEN_REUSED、UNAUTHORIZED、INVALID_AUTH_HEADER、EMPTY_TOKEN | 清除会话，重新登录 |
| 401 | INVALID_ADMIN_KEY | 重新输入 Admin Key；管理访问令牌（sat_）不存在或已吊销也是这个码 |
| 401 | ACCESS_TOKEN_EXPIRED | 管理访问令牌已过期，重新登录后签发新令牌 |
| 401 | INVALID_CREDENTIALS | 提示邮箱或密码错误 |
| 403 | STEP_UP_REQUIRED | 弹 TOTP，`POST /api/v1/user/totp/step-up` 后重试原请求 |
| 403 | STEP_UP_TOTP_NOT_ENABLED | 提示先启用两步验证 |
| 403 | STEP_UP_ADMIN_API_KEY_FORBIDDEN | 提示改用账号登录（Admin Key 与管理访问令牌都不能做二次验证） |
| 403 | ACCESS_TOKEN_READ_ONLY | 只读的管理访问令牌不能执行写操作 |
| 403 | FORBIDDEN、WORKSPACE_DISABLED、USER_NOT_ACTIVE | 无权限 |
| 403 | BACKEND_MODE_ADMIN_ONLY | 后端模式只允许管理员登录 |
| 403 | BACKEND_MODE_SELF_SERVICE_DISABLED | 后端模式下用户自助功能已关闭 |
| 423 | ADMIN_COMPLIANCE_ACK_REQUIRED | 展示协议，`POST /api/v1/admin/compliance/accept` 后重试 |
| 404 | OPS_DISABLED | 显示“运维监控未开启”；用 `GET /api/v1/admin/ops/monitoring-status`（不受开关影响）的 `disabled_reason` 区分 `config_off` 与 `setting_off`，后者可 `PUT` 同一路径开启 |
| 409 | OPS_CONFIG_DISABLED | `PUT /api/v1/admin/ops/monitoring-status`：服务器配置文件关闭了运维监控（`ops.enabled=false`），提示“需要在服务器配置中启用运维监控”，不要引导去设置页，也不要重试 |
| 404 | OPS_REQUEST_NOT_FOUND | `GET /api/v1/admin/ops/requests/:request_id/attempts`：运维错误日志与使用记录都没有这个请求（参数可传运维 request_id、client_request_id 或使用记录的 `client:<client_request_id>`；`around` 收窄到前后 1 小时时也可能落在窗口外）。退回 `request-errors/:id/upstream-errors` 加 `admin/usage?request_id=client:<client_request_id>` 自己拼 |
| 409 | OPS_REQUEST_AMBIGUOUS | 同一路径：客户端自带的 X-Request-ID 被多次请求复用，命中了多条链。`metadata.client_request_ids` 逗号分隔列出最多 5 个服务端 client_request_id，选一个改用它重查；不要原样重试 |
| 400 | TOTP_INVALID_CODE、TURNSTILE_VERIFICATION_FAILED、TENCENT_CAPTCHA_VERIFICATION_FAILED、ALIYUN_CAPTCHA_VERIFICATION_FAILED | 重新输入 |
| 400 | TOTP_LOGIN_SESSION_EXPIRED | 登录第二步超时，回到密码页重新登录 |
| 400 | INVALID_SORT_BY | 参数校验类：查询参数取值不受支持（`/admin/dashboard/users-ranking` 的 `sort_by` 只接受 actual_cost / requests / tokens），按 message 修正参数，不要原样重试 |
| 400 | TOO_MANY_IDS | 批量查询的 ID 超过上限（`/admin/dashboard/users-usage`、`/admin/dashboard/api-keys-usage`、`/usage/dashboard/api-keys-usage`；`metadata.max` 为上限，当前 1000；`metadata.field` 指出是哪个字段），拆成更小的批次重试 |
| 400 | OPS_TIME_RANGE_TOO_LARGE | 时间跨度超过接口上限：`/admin/ops/dashboard/openai-token-stats`、`/admin/ops/system-metrics/history` 为 31 天（`metadata.max_days`=31，另有 1 小时夏令时容差），`/admin/ops/realtime-traffic` 为 1 小时（无 metadata）；缩短范围后重试 |
| 400 | INVALID_MAX_POINTS | 参数校验类：`/admin/ops/system-metrics/history` 的 `max_points` 必须是 1–1440 的整数（缺省 60），按 message 修正参数，不要原样重试 |
| 400 | INVALID_REQUEST_ID、INVALID_AROUND | 参数校验类：`/admin/ops/requests/:request_id/attempts` 的 request_id 去掉首尾空白后须为 1–256 字节；`around` 须为 RFC3339 时间（如 `2026-10-18T01:41:00Z`，带 `+08:00` 时要把 `+` 编码为 `%2B`）。按 message 修正参数，不要原样重试 |
| 400 | INVALID_SINCE_ID、SINCE_ID_WITH_PAGE | 参数校验类：`/admin/usage`、`/admin/ops/request-errors` 的增量游标 `since_id` 必须是不小于 0 的整数，且不能与 `page` > 1 同时传；去掉 `page` 或改用 `page=1` 全量刷新 |
| 400 | INVALID_TIMEZONE、INVALID_INCLUDE | 参数校验类：`/admin/dashboard/home` 的 `timezone` 须为 IANA 时区名（如 `Asia/Shanghai`，不传为服务器时区），`include` 只接受 stats、rates、hourly、ops、alerts、availability、channels 的逗号分隔组合（不传为全部）；按 message 修正参数，不要原样重试 |
| 409 | ACCOUNT_TEST_RUNNING | `POST /api/v1/admin/accounts/:id/test-runs`：该账号已有一个运行中的异步测试，`metadata.run_id` 是它的 run_id，改为轮询 `GET /api/v1/admin/account-test-runs/:run_id`，不要重复提交 |
| 404 | ACCOUNT_TEST_RUN_NOT_FOUND | `GET /api/v1/admin/accounts/:id/test-runs/latest` 与 `GET /api/v1/admin/account-test-runs/:run_id`：没有测试记录、run_id 不存在或不属于本工作区的账号；latest 返回它时显示“还没有测试记录” |
| 429 | ACCOUNT_TEST_BUSY | `POST /api/v1/admin/accounts/:id/test-runs`：本实例同时运行的异步测试已达上限（10 个），稍后重试 |
| 429 | RATE_LIMITED、TOTP_TOO_MANY_ATTEMPTS | 稍后再试（RATE_LIMITED 按 `retry_after` 秒） |
| 500 | ACCOUNT_RECOVERED_SCHEDULABLE_FAILED | `POST /api/v1/admin/accounts/:id/recover-state` 带 `{"schedulable":...}` 时：恢复已生效、调度开关未改（`metadata.recovered="true"`）。不要整体重试，先 GET 账号以读回的 schedulable 为准，再用 `POST /api/v1/admin/accounts/:id/schedulable` 单独重试；仍暂停时显示“已恢复 · 仍暂停”并给“启用调度”按钮 |
| 503 | STEP_UP_UNAVAILABLE、SERVICE_UNAVAILABLE、CAPTCHA_PROVIDER_CONFLICT | 暂时性错误，保留会话，稍后重试 |

`TOKEN_REVOKED` 表示会话已被撤销（退出登录、在其他设备上撤销、撤销全部会话、刷新令牌重放、改密），不要再用同一 refresh token 续期。

## 首页聚合各段的 error

`GET /api/v1/admin/dashboard/home` 只要鉴权与参数通过就返回 HTTP 200，每段各自带 `status`（ok / disabled / error / timeout）；不是 ok 时 `error` 为下列值之一，该段没有 `data`：

| status | error | 客户端处理 |
|---|---|---|
| disabled | OPS_DISABLED | ops、alerts 段：显示“运维监控未开启”，处理方式同上表的 OPS_DISABLED |
| disabled | CHANNEL_MONITOR_DISABLED | channels 段：显示“渠道监控未开启” |
| timeout | SECTION_TIMEOUT | 该段超过自己的时限，其余段照常显示；下次刷新自动重试（带 error / timeout 段的响应不进服务端缓存） |
| error | INTERNAL_ERROR | 该段的数据源出错且没有稳定 reason（原始错误只写服务端日志）；下次刷新重试 |
| error | 其他值 | 与对应独立接口的 reason 相同，如 OPS_REPO_UNAVAILABLE、OPS_PREAGG_NOT_READY、SERVICE_UNAVAILABLE |
