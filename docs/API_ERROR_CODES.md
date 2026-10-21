# 管理端 / 用户端接口稳定错误码

客户端（Web、原生 App）按 `reason` 决定下一步：续期令牌、弹二次验证、引导合规确认或重新登录。下表中的 `reason` 是稳定契约，改名会让已发布的客户端失效；`TestStableErrorReasons`（`backend/internal/service/stable_error_codes_test.go`）会拦住服务层错误的改名。

## 响应格式

两种来源的错误都带字符串 `reason`，客户端统一读它即可：

- 中间件：`{"code":"TOKEN_EXPIRED","message":"Token has expired","reason":"TOKEN_EXPIRED"}`，`code` 是字符串，与 `reason` 相同。
- handler 信封：`{"code":401,"message":"...","reason":"REFRESH_TOKEN_INVALID","metadata":{...}}`，`code` 是 HTTP 状态码整数。
- 合规确认 423：`{"code":"ADMIN_COMPLIANCE_ACK_REQUIRED","reason":"ADMIN_COMPLIANCE_ACK_REQUIRED","message":"...","metadata":{"version":"...","document_url_zh":"...",...}}`。
- 登录 / 刷新限流 429：`{"code":"RATE_LIMITED","reason":"RATE_LIMITED","retry_after":12,"error":"rate limit exceeded","message":"..."}`，同时带 `Retry-After` 头；`error` 字段为兼容旧客户端保留。
- 账号混合渠道 409：`{"code":409,"reason":"MIXED_CHANNEL_WARNING","error":"mixed_channel_warning","message":"mixed_channel_warning: Group '...' contains both Antigravity and Anthropic accounts. ...","metadata":{"group_id":"7","group_name":"...","current_platform":"Antigravity","other_platform":"Anthropic"},"details":{"group_id":7,"group_name":"...","current_platform":"Antigravity","other_platform":"Anthropic"}}`。`error` 与 `message` 为兼容旧客户端保留；`metadata` 全是字符串，`details` 与 `POST /api/v1/admin/accounts/check-mixed-channel` 返回的 details 同形（group_id 为数字）。同为 409 的还有 `IDEMPOTENCY_KEY_CONFLICT`、`IDEMPOTENCY_RETRY_BACKOFF` 等，必须按 `reason` 区分。

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
| 403 | ACCESS_TOKEN_READ_ONLY | 只读的管理访问令牌不能执行写操作。例外：撤销自己，以及两个只读的批量 POST `/api/v1/admin/accounts/runtime/batch`、`/api/v1/admin/accounts/today-stats/batch`（`/admin/me` 的 capabilities 含 `accounts.runtime_batch` 起放行；`accounts/usage/batch` 仍拒绝，它带 `force` 时会探测上游）；capabilities 含 `push.notifications` 起，还放行令牌主人自己的推送设备：`POST /api/v1/admin/push/devices`、`DELETE /api/v1/admin/push/devices/:id`、`PUT /api/v1/admin/push/devices/:id/subscription`、`POST /api/v1/admin/push/devices/:id/test`（`PUT /api/v1/admin/push/config` 仍拒绝），但注册时只能 `platform=ios`，登记 Bark / ntfy / Webhook 返回本错误（`metadata.field` 为 `platform`） |
| 403 | FORBIDDEN、WORKSPACE_DISABLED、USER_NOT_ACTIVE | 无权限 |
| 403 | BACKEND_MODE_ADMIN_ONLY | 后端模式只允许管理员登录 |
| 403 | BACKEND_MODE_SELF_SERVICE_DISABLED | 后端模式下用户自助功能已关闭 |
| 423 | ADMIN_COMPLIANCE_ACK_REQUIRED | 展示协议，`POST /api/v1/admin/compliance/accept` 后重试 |
| 404 | OPS_DISABLED | 显示“运维监控未开启”；用 `GET /api/v1/admin/ops/monitoring-status`（不受开关影响）的 `disabled_reason` 区分 `config_off` 与 `setting_off`，后者可 `PUT` 同一路径开启 |
| 409 | OPS_CONFIG_DISABLED | `PUT /api/v1/admin/ops/monitoring-status`：服务器配置文件关闭了运维监控（`ops.enabled=false`），提示“需要在服务器配置中启用运维监控”，不要引导去设置页，也不要重试 |
| 404 | OPS_REQUEST_NOT_FOUND | `GET /api/v1/admin/ops/requests/:request_id/attempts`：运维错误日志与使用记录都没有这个请求（参数可传运维 request_id、client_request_id 或使用记录的 `client:<client_request_id>` / `probe:<request_id>`；`around` 收窄到前后 1 小时时也可能落在窗口外）。客户端自带的 X-Request-ID 含 `/` 时，编码成 `%2F` 也匹配不到这条路由，网关返回纯文本 404（没有 reason），这类请求改用服务端 client_request_id（UUID）查。退回 `request-errors/:id/upstream-errors` 加 `admin/usage?request_id=client:<client_request_id>` 自己拼 |
| 409 | OPS_REQUEST_AMBIGUOUS | 同一路径：客户端自带的 X-Request-ID 被多次请求复用，命中了多条链。`metadata.client_request_ids` 逗号分隔列出最多 5 个服务端 client_request_id（最新的请求在前），`metadata.client_request_id_times` 与之一一对应，是各自最新一条运维记录的 created_at（RFC3339）；按时间选一个改用它重查；不要原样重试 |
| 400 | TOTP_INVALID_CODE、TURNSTILE_VERIFICATION_FAILED、TENCENT_CAPTCHA_VERIFICATION_FAILED、ALIYUN_CAPTCHA_VERIFICATION_FAILED | 重新输入 |
| 400 | CAPTCHA_PROVIDER_MISMATCH | 带验证码的认证接口（`POST /api/v1/auth/login`、`register`、`send-verify-code`、`forgot-password`、`passkey/login/begin`）请求体里的可选 `captcha_provider` 不是站点当前生效的验证码提供方（声明值去掉首尾空白、不区分大小写比较；站点没开验证码时带了声明也返回这个码；不带声明不校验）。`metadata.captcha_provider` 是当前生效的提供方（`""` / `turnstile` / `tencent` / `aliyun`，与 `GET /api/v1/settings/public` 的 `captcha_provider` 同源），按它重新做一次验证后重试，不要原样重试。同时开启多个提供方时优先返回 503 `CAPTCHA_PROVIDER_CONFLICT`。`/admin/me` 的 capabilities 含 `auth.captcha_provider` 时才有这个码；登录前用公开设置里有没有 `captcha_provider` 键判断，老服务端会忽略 `captcha_provider` 与 `aliyun_captcha_param` |
| 400 | TOTP_LOGIN_SESSION_EXPIRED | 登录第二步超时，回到密码页重新登录 |
| 400 | CLAUDE_OAUTH_SESSION_NOT_FOUND、GEMINI_OAUTH_SESSION_NOT_FOUND、ANTIGRAVITY_OAUTH_SESSION_NOT_FOUND、OPENAI_OAUTH_SESSION_NOT_FOUND、GROK_OAUTH_SESSION_NOT_FOUND | 各平台 OAuth 换码（`/admin/accounts/exchange-code`、`exchange-setup-token-code`、`/admin/gemini/oauth/exchange-code`、`/admin/antigravity/oauth/exchange-code`、`/admin/openai/exchange-code`、`/admin/grok/oauth/exchange-code` 等）：授权会话不存在或已过期（30 分钟，存在内存里，服务重启也会丢）。提示“授权链接已过期”，重新生成授权链接；按后缀 `_OAUTH_SESSION_NOT_FOUND` 识别即可 |
| 400 | CLAUDE_OAUTH_INVALID_STATE、GEMINI_OAUTH_INVALID_STATE、ANTIGRAVITY_OAUTH_INVALID_STATE、OPENAI_OAUTH_INVALID_STATE、GROK_OAUTH_INVALID_STATE | 同上接口：授权码不是这次生成的链接换来的（Claude 只在粘贴的授权码带 `#state` 时校验；Gemini / Antigravity 的空 state 也是这个码）。提示重新打开本次链接授权，或重新生成；按后缀 `_OAUTH_INVALID_STATE` 识别 |
| 400 | CLAUDE_OAUTH_CODE_REJECTED、GEMINI_OAUTH_CODE_REJECTED、ANTIGRAVITY_OAUTH_CODE_REJECTED、OPENAI_OAUTH_CODE_REJECTED、GROK_OAUTH_CODE_REJECTED | 同上接口：上游 token 端点拒绝了授权码（上游 4xx 且响应 `error` 为 `invalid_grant`，或不带客户端配置类错误码的 400；408 / 429 不算），通常是授权码已用过、已过期或复制不完整；`metadata.upstream_status` 为上游 HTTP 状态码字符串。提示重新授权，不要原样重试。Claude / Gemini / Antigravity / OpenAI 的会话仍在，可用同一链接换新授权码；Grok 的会话在换码前已消费，要重新生成链接。上游 5xx、408、429、网络错误，不带 `invalid_grant` 的 401 / 403 / 404（WAF 或 Cloudflare 拦截、地区封锁，授权码其实没用掉），以及 `invalid_client` / `unauthorized_client` / `unsupported_grant_type` / `invalid_scope`（服务端 OAuth 客户端配置问题）都不是这个码，保持原样（Claude 500、OpenAI / Grok 502 `*_OAUTH_TOKEN_EXCHANGE_FAILED`、Gemini / Antigravity 400 无 reason；`reauthorize` 接口把这些统一为 502 `OAUTH_EXCHANGE_FAILED`）；Grok 明确的授权拒绝仍是 403 `GROK_OAUTH_ENTITLEMENT_DENIED`。`/admin/me` 的 capabilities 含 `accounts.oauth_error_reasons` 时这些 reason 才齐全 |
| 400 | NOT_OAUTH | `POST /api/v1/admin/accounts/:id/reauthorize`（以及 `apply-oauth-credentials`、刷新凭据）：账号不是 OAuth / Setup Token 类型，不能重新授权，不要重试 |
| 400 | REAUTH_UNSUPPORTED | `POST /api/v1/admin/accounts/:id/reauthorize`：这个 OAuth 账号不能走一步重新授权，`metadata.detail` 说明原因：`shadow_account`（影子账号，凭据跟随母账号）、`codex_personal_access_token`（Codex PAT 账号，改用 PAT 重新导入）、`unsupported_platform`（只支持 anthropic / openai / gemini / antigravity / grok）、`auth_kind_not_supported`（`auth_kind=setup-token` 只用于 Claude）。不要重试；换码阶段带原因码的非 502 错误（上面各 `*_OAUTH_*` 行、403 `GROK_OAUTH_ENTITLEMENT_DENIED` 等）原样透传，处理方式相同；上游或网络失败统一为 502 `OAUTH_EXCHANGE_FAILED`（见下） |
| 404 | PROXY_NOT_FOUND | 代理不存在或不属于本工作区（越权与不存在同码）。供应商（vendor）调 Claude 授权接口（`/admin/accounts/generate-auth-url`、`generate-setup-token-url`、`exchange-code`、`exchange-setup-token-code`、`cookie-auth`、`setup-token-cookie-auth`）时，请求里的 `proxy_id` 只能是本工作区的代理，否则返回这个码且不会发出任何上游请求；站长不受限。重新拉取代理列表后改选，或不传 `proxy_id` 直连，不要原样重试 |
| 400 | INVALID_SORT_BY | 参数校验类：查询参数取值不受支持（`/admin/dashboard/users-ranking` 的 `sort_by` 只接受 actual_cost / requests / tokens），按 message 修正参数，不要原样重试 |
| 400 | TOO_MANY_IDS | 批量查询的 ID 超过上限（`/admin/dashboard/users-usage`、`/admin/dashboard/api-keys-usage`、`/usage/dashboard/api-keys-usage` 当前为 1000；`POST /api/v1/admin/accounts/runtime/batch` 的 `account_ids` 当前为 200，按去重、去掉非正数之后的个数计；`GET /api/v1/admin/groups/capacity-summary` 的 `group_id` 与 `group_ids` 合计去重后当前为 100（`metadata.field`=group_ids）；`metadata.max` 为上限，`metadata.field` 指出是哪个字段），按 `metadata.max` 拆成更小的批次重试 |
| 400 | OPS_TIME_RANGE_TOO_LARGE | 时间跨度超过接口上限：`/admin/ops/dashboard/openai-token-stats`、`/admin/ops/system-metrics/history` 为 31 天（`metadata.max_days`=31，另有 1 小时夏令时容差），`/admin/ops/realtime-traffic` 为 1 小时（无 metadata）；缩短范围后重试 |
| 400 | INVALID_MAX_POINTS | 参数校验类：`/admin/ops/system-metrics/history` 的 `max_points` 必须是 1–1440 的整数（缺省 60），按 message 修正参数，不要原样重试 |
| 400 | INVALID_REQUEST_ID、INVALID_AROUND | 参数校验类：`/admin/ops/requests/:request_id/attempts` 的 request_id 去掉首尾空白后须为 1–256 字节的合法 UTF-8，且不含 NUL；`around` 须为 RFC3339 时间（如 `2026-10-18T01:41:00Z`，带 `+08:00` 时要把 `+` 编码为 `%2B`）。按 message 修正参数，不要原样重试 |
| 400 | INVALID_SINCE_ID、SINCE_ID_WITH_PAGE | 参数校验类：`/admin/usage`、`/admin/ops/request-errors` 的增量游标 `since_id` 必须是不小于 0 的整数，且不能与 `page` > 1 同时传；去掉 `page` 或改用 `page=1` 全量刷新 |
| 400 | INVALID_TIMEZONE、INVALID_INCLUDE | 参数校验类：`/admin/dashboard/home` 的 `timezone` 须为 IANA 时区名（如 `Asia/Shanghai`，不传为服务器时区），`include` 只接受 stats、rates、hourly、ops、alerts、availability、channels 的逗号分隔组合（不传为全部）；按 message 修正参数，不要原样重试 |
| 400 | INVALID_GROUP_FILTER | 参数校验类：分组过滤参数 `group_id` 必须是正整数（`GET /admin/accounts/status-summary` 的查询参数、`POST /api/v1/admin/accounts/today-stats/batch` 请求体里的可选 `group_id`；后者传 null 等于不传）；按 message 修正参数，不要原样重试。vendor 对 today-stats/batch 传未授权的分组返回 404 RESOURCE_NOT_FOUND（不区分分组不存在） |
| 400 / 404 | INVALID_GROUP_ID、GROUP_NOT_FOUND | `GET /api/v1/admin/groups/capacity-summary` 的可选 `group_id`（可重复，每个值是单个正整数）/ `group_ids`（逗号分隔，可重复）：值不是正整数（含 `group_id=1,2` 这种把逗号写进 group_id 的）返回 400 `INVALID_GROUP_ID`，按 message 修正参数，不要原样重试；任一分组不存在或已删除时整次返回 404 `GROUP_NOT_FOUND`，`metadata.group_id` 为请求顺序里第一个缺失的 ID，去掉它后重试。停用分组不报错，只是不出现在结果里（全部停用时 data 为 `[]`）；参数只有空值或逗号等于不带参数，返回全部启用分组。vendor 请求未授权的分组返回 404 `RESOURCE_NOT_FOUND`（不带 metadata，不区分分组是否存在）。`/admin/me` 的 capabilities 含 `groups.capacity_filter` 时才支持这两个参数，老服务端会忽略它们并返回全部分组 |
| 400 | INVALID_ACCOUNT_STATUS_FILTER | 参数校验类：`GET /api/v1/admin/accounts`（以及账号导出、`/admin/accounts/upstream-billing-rates` 等共用账号列表筛选的接口）的 `status` 按逗号拆分、去空白、去重后超过 16 个值（`metadata.max`=16）。减少筛选值后重试，不要原样重试；单个值不会触发 |
| 400 | INVALID_READ_STATUS_FILTER | 参数校验类：`GET /api/v1/admin/announcements/:id/read-status` 的 `read_state` 只接受 read / unread（不区分大小写，空值不过滤），`eligible` 只接受 true / false / 1 / 0 等布尔值（空值不过滤）。`/admin/me` 的 capabilities 含 `announcements.read_stats` 时才支持这两个参数；按 message 修正参数，不要原样重试 |
| 400 | INVALID_USER_ID、INVALID_PLATFORM、INVALID_QUOTA_LIMIT、VALIDATION_ERROR | 参数校验类：单平台限额接口（`PUT` / `DELETE /api/v1/admin/users/:id/platform-quotas/:platform`、`POST .../:platform/restore`）。`INVALID_USER_ID`：路径里的用户 ID 不是整数；`INVALID_PLATFORM`：平台不是平台清单里的具体平台（如 `invalid platform: foo`，composite 也不行）；`INVALID_QUOTA_LIMIT`：某档限额为负数、不是有限数或不小于 1e10（限额列 decimal(20,10) 的上限；message 为 `daily_limit_usd must be >= 0`、`... must be a finite number`、`... must be less than 10000000000`，整表 `PUT .../platform-quotas` 共用同一校验，那里只有 message、没有 reason）；`VALIDATION_ERROR`：PUT 请求体不是单个 JSON 对象、字段类型不对或含未知字段（严格解码，防止字段名拼错被当成三档全空而删掉限额）。按 message 修正后重试，不要原样重试。用户不存在为 404 `USER_NOT_FOUND`，限额服务未启用为 503 `SERVICE_UNAVAILABLE` |
| 400 | USER_BALANCE_NOT_EDITABLE | `PUT /api/v1/admin/users/:id` 的请求体带了 `balance`（包括 0；`null` 等于不传）。以前这个字段被静默忽略，现在整个请求被拒绝、什么都不改，也不占用 Idempotency-Key。去掉 `balance` 重发；改余额用 `POST /api/v1/admin/users/:id/balance`（会写充值与调整记录）。`/admin/me` 的 capabilities 含 `users.balance_ledger` 时才有这个码，老服务端会忽略 balance 并返回 200 |
| 404 | USER_PLATFORM_QUOTA_NOT_FOUND | `DELETE /api/v1/admin/users/:id/platform-quotas/:platform`：该平台没有活跃限额；`POST .../:platform/restore`：该平台从没有被取消过限额，没有可恢复的记录；`POST /api/v1/admin/users/:id/platform-quotas/reset` 重置不存在的限额也是这个码（message 不变）。重新 GET 限额列表刷新界面，不要重试 |
| 409 | USER_PLATFORM_QUOTA_ACTIVE_EXISTS | `POST /api/v1/admin/users/:id/platform-quotas/:platform/restore`：该平台已经有活跃限额（之后有人重新配置过），不能再恢复旧的那条。重新 GET 限额列表，需要改值时用 `PUT .../:platform`。`/admin/me` 的 capabilities 含 `users.platform_quota_item` 时才有这三个单平台接口 |
| 409 | REDEEM_CODE_USED | 兑换码已进入用户的充值与调整记录：被兑换过（status=used，或 used_by 不为空，即兑换后又被改成过期 / 禁用的码），或是余额 / 并发调整留下的 admin_balance、admin_concurrency 记录。`DELETE /api/v1/admin/redeem-codes/:id`（message 为 `used redeem codes cannot be deleted`）、`POST /api/v1/admin/redeem-codes/:id/expire`、`POST /api/v1/admin/redeem-codes/batch-update` 改状态 / 有效期 / 分组（整批拒绝，库内无变化；只改备注不受影响）返回这个码。不要重试，刷新列表后对这些码只提供复制、查看使用者、编辑备注。`POST /api/v1/admin/redeem-codes/batch-delete` 不返回这个码：受保护的码被跳过，响应 `skipped_used` 为个数、`skipped_ids` 为 ID（升序，没有时为 `[]`），`deleted` 是实际删除的行数（不存在、重复的 ID 不计入）。`/admin/me` 的 capabilities 含 `redeem_codes.delete_guard` 时删除才受保护，老服务端会直接删除 |
| 404 | REDEEM_CODE_NOT_FOUND | 兑换码不存在。`DELETE /api/v1/admin/redeem-codes/:id` 返回它时视为已删除（capabilities 含 `redeem_codes.delete_guard` 起才返回 404，以前不存在也返回 200）；其他兑换码接口刷新列表后重试 |
| 409 | ACCOUNT_TEST_RUNNING | `POST /api/v1/admin/accounts/:id/test-runs`：该账号已有一个运行中的异步测试，`metadata.run_id` 是它的 run_id，改为轮询 `GET /api/v1/admin/account-test-runs/:run_id`，不要重复提交 |
| 409 | MIXED_CHANNEL_WARNING | 新建 / 编辑账号绑定的分组里同时有 Antigravity 与 Anthropic 账号（`POST /api/v1/admin/accounts`、`PUT` / `PATCH /api/v1/admin/accounts/:id`、`POST /api/v1/admin/accounts/bulk-update`；信封见上文“响应格式”）。用 `metadata` 或 `details` 的分组名与两个平台弹确认框，确认后在请求体加 `confirm_mixed_channel_risk=true` 重发。新建接口的 `Idempotency-Key` 可选（不带照常执行）；带了 key 的话，确认后请求体变了，必须换一个新的 key 重发，沿用旧 key 会收到 409 `IDEMPOTENCY_KEY_CONFLICT`。`current_platform` / `other_platform` 是展示用的首字母大写名称（Antigravity、Anthropic），不是平台枚举。`/admin/me` 的 capabilities 含 `accounts.mixed_channel_details` 时才有 reason / metadata / details，否则先调 check-mixed-channel 预检。分组批量成员接口（`groups.member_batch`）的逐项结果用的是另一个码 `MIXED_CHANNEL_RISK`。vendor 绑定未授权的分组时先返回 404 `RESOURCE_NOT_FOUND`，不会走到这个码 |
| 400 | IDEMPOTENCY_KEY_INVALID | `Idempotency-Key` 去掉首尾空白后超过 128 字节，或含空白 / 非可见 ASCII 字符。换一个合法的 key（如 UUID）重发 |
| 400 | IDEMPOTENCY_KEY_REQUIRED | 只有强制带 key 的接口（余额调整、订阅批量操作等）在服务端关闭 `idempotency.observe_only` 后才会返回；带上 key 重发。下文“幂等键”一节列出的可选接口不会返回这个码 |
| 409 | IDEMPOTENCY_KEY_CONFLICT | 同一个 key 已用于请求体不同的请求（指纹含方法、路由、操作者与请求体）。换一个新的 key 再发；不要原样重试 |
| 409 | IDEMPOTENCY_IN_PROGRESS | 同一个 key 的首次请求还在执行。按 `Retry-After` 秒后用同一个 key 原样重试；连续多次仍是这个码（执行进程可能已退出，记录要到 24 小时后才过期）时，改为读回服务器状态核对，不要换 key 重发 |
| 409 | IDEMPOTENCY_RETRY_BACKOFF | 同一个 key 的上次执行失败，仍在 5 秒退避期内。按 `Retry-After` 秒后用同一个 key 原样重试；要改请求体（如确认混合渠道风险后带 `confirm_mixed_channel_risk=true`）必须换新 key |
| 503 | IDEMPOTENCY_STORE_UNAVAILABLE | 幂等存储暂时不可用，带 key 的写请求没有执行（fail-close）。不要换 key 重发，先读回服务器状态核对，稍后再用同一个 key 重试。重放快照超过 64KB 被截断时也会返回这个码，这时只能读回核对 |
| 404 | ACCOUNT_TEST_RUN_NOT_FOUND | `GET /api/v1/admin/accounts/:id/test-runs/latest` 与 `GET /api/v1/admin/account-test-runs/:run_id`：没有测试记录、run_id 不存在或不属于本工作区的账号；latest 返回它时显示“还没有测试记录” |
| 400 | INVALID_PUSH_TOKEN | `POST /api/v1/admin/push/devices`：设备令牌格式不对（iOS 的 `apns_token` 须为 64–200 位十六进制；Bark 的 `token` 是设备 key；ntfy 的 `token` 是主题，只能含字母、数字、`-`、`_`），`metadata.field` 指出字段；修正后再注册，不要原样重试 |
| 400 | INVALID_PUSH_TARGET | 同一接口：`platform` 不是 ios / bark / ntfy / webhook，`endpoint` 不是允许的 URL（默认只接受公网主机的 https；局域网或 http 的 ntfy / Bark 需要站长打开 `security.push_targets.allow_private_hosts` / `allow_insecure_http`，两者默认关闭，与 `security.url_allowlist` 无关），或 `bundle_id`、`environment`（production / sandbox）、`timezone`（IANA 名）、`secret` 不合法；请求体不是 JSON 时 `metadata.field` 为 `body` |
| 400 | INVALID_PUSH_SUBSCRIPTION | 注册时的 `subscription` 或 `PUT /api/v1/admin/push/devices/:id/subscription`：`events` 只能是 ops_alert.fired、ops_alert.resolved、account.error、account.test_finished，`min_severity` 只能是 P0–P3，`quiet_hours` 的 start / end 须为 HH:MM，`group_ids` 至多 100 个正整数，`platforms` 至多 20 个；`metadata.field` 指出字段 |
| 404 | PUSH_DEVICE_NOT_FOUND | 推送设备不存在或不属于当前管理员（越权与不存在同码）；从设备列表里去掉它 |
| 409 | PUSH_DEVICE_LIMIT_REACHED | 当前管理员的推送设备已达上限（`metadata.max`，当前 20）；先删除不用的设备。重新注册已有的同一令牌不受限制 |
| 502 | PUSH_DELIVERY_FAILED | `POST /api/v1/admin/push/devices/:id/test`：推送服务没有接受这条通知，`metadata.provider_status` 为对方的 HTTP 状态码（网络错误为 0），`metadata.provider_reason` 为对方给出的原因（如 APNs 的 `Unregistered`、`BadDeviceToken`）。APNs 410 / BadDeviceToken / DeviceTokenNotForTopic、其他渠道 410 会同时停用设备（列表里 `enabled=false` 与 `disabled_reason`），重新注册同一令牌即可恢复 |
| 503 | PUSH_NOT_CONFIGURED | 测试 iOS 设备时站点没有启用或没有配全 APNs（`GET /api/v1/admin/push/config` 的 `ready=false`），或保存的 p8 私钥已无法解密；提示站长补全 APNs 配置，或改用 Bark / ntfy / Webhook |
| 400 | INVALID_APNS_CONFIG | `PUT /api/v1/admin/push/config`：`apns_team_id` / `apns_key_id` 须为 10 位大写字母数字，`apns_private_key` 须为 APNs 的 .p8（P-256）PEM，`apns_topic` 须为 bundle ID，`apns_environment` 只能是 production / sandbox，启用（`apns_enabled=true`）时团队 ID、Key ID、私钥必须齐全；`metadata.field` 指出字段 |
| 400 | SECRET_ENCRYPTION_KEY_NOT_CONFIGURED | 写入需要加密保存的密钥（S3 SecretAccessKey、APNs p8 私钥）时，服务器没有配置固定的 `TOTP_ENCRYPTION_KEY`：自动生成的密钥每次重启都会变，保存的密文会解不开。提示站长先配置固定密钥 |
| 429 | ACCOUNT_TEST_BUSY | `POST /api/v1/admin/accounts/:id/test-runs`：本实例同时运行的异步测试已达上限（10 个），稍后重试；该账号已有运行中的测试时优先返回 409 ACCOUNT_TEST_RUNNING |
| 429 | RATE_LIMITED、TOTP_TOO_MANY_ATTEMPTS | 稍后再试（RATE_LIMITED 按 `retry_after` 秒） |
| 500 | ACCOUNT_RECOVERED_SCHEDULABLE_FAILED | `POST /api/v1/admin/accounts/:id/recover-state` 带 `{"schedulable":true}` 时：恢复已生效，但启用调度这一步报错（`metadata.recovered="true"`）。调度开关可能没改，也可能已经改了（写库成功、随后回读失败时同样返回此码）。不要整体重试，先 GET 账号以读回的 schedulable 为准，仍暂停时显示“已恢复 · 仍暂停”并给“启用调度”按钮（`POST /api/v1/admin/accounts/:id/schedulable` 单独重试）。`{"schedulable":false}` 不会返回此码：暂停在恢复之前写入，写入失败时不做恢复、返回原错误，恢复失败时账号已暂停，两种情况都可以整体重试 |
| 500 | REAUTH_PERSIST_FAILED | `POST /api/v1/admin/accounts/:id/reauthorize`：换码已成功、凭据保存失败（`metadata.retry_within_seconds`=300，`metadata.cause_reason` 为保存失败的原因码，可能没有）。授权码已用掉，不要重新换码：从收到这次 500 起 5 分钟内用同一 `session_id` 原样重试（每次保存失败都重新计时），服务端直接保存暂存的凭据（暂存在本实例内存里，重启或打到别的实例会丢，那时只能重新生成授权链接）。保存成功后 5 分钟内再用同一 `session_id` 重放会直接返回账号（重新读取的当前账号，不再写入） |
| 502 | OAUTH_EXCHANGE_FAILED | `POST /api/v1/admin/accounts/:id/reauthorize`：换码在上游或网络层失败，五个平台都用这个码（上游 5xx、408 / 429、不带 `invalid_grant` 的 401 / 403 / 404、上游返回 `invalid_client` 等客户端配置类错误、网络错误、超时、Gemini 换码后探测 project_id 失败等；拒码、会话过期、state 不符与 Grok 明确的授权拒绝不在此列，见上面各行）。`metadata.platform` 为平台；OpenAI / Grok 换码层原本给的 502 原因码（`OPENAI_OAUTH_TOKEN_EXCHANGE_FAILED`、`OPENAI_OAUTH_REQUEST_FAILED`、`OPENAI_OAUTH_PROXY_REQUIRED`、`OPENAI_OAUTH_CLIENT_INIT_FAILED`、`GROK_OAUTH_TOKEN_EXCHANGE_FAILED`、`GROK_OAUTH_REQUEST_FAILED`、`GROK_OAUTH_CLIENT_INIT_FAILED`、`GROK_OAUTH_INVALID_TOKEN_RESPONSE`）放在 `metadata.cause_reason`，仅供排查，其他平台没有这个键；message 带脱敏后的原因。Claude / OpenAI / Gemini / Antigravity 的会话可能还在，可换新授权码重试，不行就重新生成授权链接；Grok 的会话在换码前已消费，要重新生成授权链接 |
| 503 | STEP_UP_UNAVAILABLE、SERVICE_UNAVAILABLE、CAPTCHA_PROVIDER_CONFLICT | 暂时性错误，保留会话，稍后重试 |
| 503 | ACCOUNT_STATS_UNAVAILABLE | `POST /api/v1/admin/accounts/today-stats/batch`：请求的账号（按工作区收窄、去重后）今日统计全部查询失败，结果不缓存。列表里这些账号的今日统计显示“—”，稍后重试；只有部分账号失败时仍返回 200，`data.failed_account_ids` 列出失败的账号（按账号 ID 升序去重，与请求里的顺序无关，请当集合按 ID 匹配；`stats` 里它们补 0，不要当成“今天没有请求”），`data.degraded=true` 表示批量 SQL 失败、走了逐个查询。`/admin/me` 的 capabilities 含 `accounts.today_stats_failures` 时才有这两个字段与此码。请求体带 `group_id`（capabilities 含 `accounts.today_stats_group`）时只统计该分组的用量、不做逐个回退：分组查询失败即整体返回此码，不会退回不分组的数字，成功时 `failed_account_ids` 为空、`degraded=false` |

`TOKEN_REVOKED` 表示会话已被撤销（退出登录、在其他设备上撤销、撤销全部会话、刷新令牌重放、改密），不要再用同一 refresh token 续期。

## 幂等键（Idempotency-Key）

写接口可带请求头 `Idempotency-Key`（1–128 个可见 ASCII 字符，建议 UUID）。同一个 key、同一请求体在 24 小时内重发，服务端不再执行，返回首次成功的响应快照。快照里的运行时字段（如 current_concurrency）可能已过时，`password`、`access_token`、`refresh_token`、`code` 等键会被替换为 `***`，收到重放后需要最新状态请再 GET 一次。不同 key 之间、不同操作者之间互不重放。

响应头：

- `X-Idempotency-Replayed: true`：本次是重放，没有再次执行。
- `X-Idempotency-Expires-At`（RFC3339，UTC）：这个 key 的记录保留到何时；过期后同一个 key 会被当作新请求重新执行，只能靠读回或账本核对。
- `X-Idempotency-Degraded: result-committed`：写入已执行成功，但结果没能记进幂等存储；本次响应可信，之后同一个 key 重发可能返回 `IDEMPOTENCY_IN_PROGRESS`，按读回核对处理。
- `Retry-After`：只出现在 `IDEMPOTENCY_IN_PROGRESS` 与 `IDEMPOTENCY_RETRY_BACKOFF` 上。

`/admin/me` 的 capabilities 含 `admin.idempotent_writes` 时，下面这些接口接受可选的 key（不带 key 时与以前完全一样，Web 不带）：

| 接口 | scope | 带 key 时的执行上限 |
|---|---|---|
| `POST /api/v1/admin/users` | admin.users.create | 30 秒 |
| `PUT /api/v1/admin/users/:id` | admin.users.update | 30 秒 |
| `POST /api/v1/admin/groups` | admin.groups.create | 30 秒 |
| `PUT /api/v1/admin/groups/:id` | admin.groups.update | 30 秒 |
| `DELETE /api/v1/admin/groups/:id` | admin.groups.delete | 30 秒 |
| `PUT /api/v1/admin/accounts/:id` | admin.accounts.update | 30 秒 |
| `POST /api/v1/admin/accounts/:id/schedulable` | admin.accounts.schedulable | 30 秒 |
| `POST /api/v1/admin/accounts/bulk-update` | admin.accounts.bulk_update | 60 秒 |
| `POST /api/v1/admin/accounts/:id/refresh` | admin.accounts.refresh | 60 秒 |
| `POST /api/v1/admin/accounts/batch-refresh` | admin.accounts.batch_refresh | 120 秒 |

带 key 的请求一经受理，客户端断开也会执行完并保存结果（受上表时限约束，到时未完成的部分按失败处理），之后用同一个 key 重发即可拿回结果；不带 key 的请求仍跟随客户端连接，没有额外时限。`POST /api/v1/admin/users` 与 `PUT /api/v1/admin/users/:id` 的指纹不含 `password` 的值（改用户只记是否设置了新密码）：同一个 key 只改密码重发，得到的是首次创建 / 首次修改的重放；同一个 key 去掉或加上密码则是 409 `IDEMPOTENCY_KEY_CONFLICT`。业务错误（如混合渠道 409、`GROUP_EXISTS`）不会被重放，原样返回，5 秒退避后可用同一个 key 重试。老服务端会忽略这个头，没有 `admin.idempotent_writes` 时超时后不要自动重发，改为读回核对。

`POST /api/v1/admin/users` 的初始余额（显式 `balance`，或省略时的站点 `default_balance`）大于 0 时会写一条充值与调整记录（`source` 为 `admin_create_user` 或 `default_balance`），与新用户在同一事务里。带 key 时响应头 `X-Admin-Adjustment-Action-ID` 给出这条记录的 action_id（同一个 key 与请求体得到同一个值，重放也带），超时后可用 `GET /api/v1/admin/user-adjustments?action_id=…` 核对；不带 key 时没有这个头。这个头在执行之前按 key 与请求体算出，带 key 的请求总会有，不表示一定写了记录：初始余额为 0 或负数（含省略 `balance` 而站点 `default_balance` 为 0）时不写记录，按它查到空列表是正常的，以响应里新用户的 `balance` 是否大于 0 判断该不该有记录；超时后没拿到响应时，先用同一个 key 重发拿回结果，或按邮箱查到新用户后再看余额；同 key 换请求体得到 409 `IDEMPOTENCY_KEY_CONFLICT` 等错误时，这个头对应的操作没有执行，不要拿它核对。`/admin/me` 的 capabilities 含 `users.balance_ledger` 时才有这个行为。

`POST /api/v1/admin/announcements`（新建公告，scope `admin.announcements.create`）在 `/admin/me` 的 capabilities 含 `announcements.idempotent_create` 时接受可选的 key，规则与上表相同：不带 key 时照常执行（Web 不带），关闭 `idempotency.observe_only` 后也不会返回 `IDEMPOTENCY_KEY_REQUIRED`；带 key 时执行上限 30 秒，同 key 同请求体重发返回首次创建的那条公告（重放快照，期间若被编辑过需再 GET），不会再建一条、也不会再弹一次窗。指纹即请求体（title、content、status、notify_mode、targeting、starts_at、ends_at），业务校验失败（如 `ANNOUNCEMENT_TITLE_INVALID`）后改了内容必须换新 key。老服务端会忽略这个头，没有该能力时超时后不要自动重发，先按标题搜最近几条核对。

用户端 `POST /api/v1/keys`（新建 API Key，scope `user.api_keys.create`）同样接受可选的 key：不带 key 时照常执行（Web 不带），关闭 `idempotency.observe_only` 后也不会返回 `IDEMPOTENCY_KEY_REQUIRED`；带 key 时重放、冲突与存储不可用的处理同上，没有执行上限。

## 首页聚合各段的 error

`GET /api/v1/admin/dashboard/home` 只要鉴权与参数通过就返回 HTTP 200，每段各自带 `status`（ok / disabled / error / timeout）；不是 ok 时 `error` 为下列值之一，该段没有 `data`：

| status | error | 客户端处理 |
|---|---|---|
| disabled | OPS_DISABLED | ops、alerts 段：显示“运维监控未开启”，处理方式同上表的 OPS_DISABLED |
| disabled | CHANNEL_MONITOR_DISABLED | channels 段：显示“渠道监控未开启” |
| timeout | SECTION_TIMEOUT | 该段超过自己的时限，其余段照常显示；下次刷新自动重试（带 error / timeout 段的响应不进服务端缓存） |
| error | INTERNAL_ERROR | 该段的数据源出错且没有稳定 reason（原始错误只写服务端日志）；下次刷新重试 |
| error | 其他值 | 与对应独立接口的 reason 相同，如 OPS_REPO_UNAVAILABLE、OPS_PREAGG_NOT_READY、SERVICE_UNAVAILABLE |

## 出站推送（A11）

`/admin/me` 的 capabilities 含 `push.notifications` 时可用。推送目标只属于注册它的管理员（只读管理令牌也能管理自己的设备，但只能注册 iOS 设备），vendor 不能访问；只推给角色为 admin、状态为 active 的用户。

- `POST /api/v1/admin/push/devices`：iOS `{"platform":"ios","apns_token":"<hex>","bundle_id":"com.example.app","environment":"production","device_name":"…","locale":"zh-Hans","timezone":"Asia/Shanghai","site_tag":"…","subscription":{…}}`；Bark `{"platform":"bark","endpoint":"https://api.day.app","token":"<device key>"}`；ntfy `{"platform":"ntfy","endpoint":"https://ntfy.sh","token":"<topic>","secret":"<access token，可省略>"}`；Webhook `{"platform":"webhook","endpoint":"https://…","secret":"<HMAC 密钥，可省略>"}`。新建返回 201，同一令牌再次注册返回 200（同一个 id，`created=false`；设备被重新启用，省略 `subscription` 时保留原订阅）。另一位管理员注册同一令牌时，设备转给他，订阅与历史重置。iOS 的 `environment` 可省略：省略时设备的 `environment` 为空串，发送时使用站点的 `apns_environment`（未设置为 production）。返回值不含令牌、secret 和完整 endpoint，只有 `token_hint`（末 4 位）与 `endpoint_host`。
- `GET /api/v1/admin/push/devices`、`DELETE /api/v1/admin/push/devices/:id`（App 退出登录时调用；会话被吊销不会自动删除设备）、`PUT /api/v1/admin/push/devices/:id/subscription`、`POST /api/v1/admin/push/devices/:id/test`（同步发一条 `push.test`，成功返回 `{"delivered":true,"provider_status":200}`，成功也会重新启用被停用的设备）。
- 订阅：`{"events":[…],"min_severity":"P1","group_ids":[],"platforms":[],"quiet_hours":{"start":"23:00","end":"07:00"}}`。省略 `events` 时默认 `ops_alert.fired` 与 `account.error`，`[]` 表示暂停推送；`min_severity` 只作用于告警；`group_ids` / `platforms` 只过滤带分组 / 平台信息的事件（全站告警照常推送）；`account.test_finished` 只推给发起测试的管理员，不受这两项过滤；免打扰按设备 `timezone` 计算（未填按 UTC），时段内的事件直接跳过、不会补推，start 等于 end 视为不设免打扰。
- `GET` / `PUT /api/v1/admin/push/config`：`apns_enabled`、`apns_team_id`、`apns_key_id`、`apns_private_key`（只写，GET 只给 `apns_private_key_configured`）、`apns_topic`（设备没带 `bundle_id` 时用）、`apns_environment`（设备没带 `environment` 时用），另有只读的 `encryption_key_configured`、`ready`。PUT 只改传了的字段，`apns_private_key` 传空串表示保留；PUT 需要 step-up，只限站长。APNs 只适用于用同一 Apple Team 的 p8 私钥构建的 App。

事件与去重：后台每 15 秒扫描最近 10 分钟的事件（多实例时同一时刻只有一个实例在扫），每台设备每个事件只推一次。`ops_alert.fired` / `ops_alert.resolved`（被静默的告警不推，与告警邮件同口径）；`account.error`（同一账号按 error_at 每 30 分钟最多一条）；`account.test_finished`（只推 `POST /api/v1/admin/accounts/:id/test-runs` 发起的异步测试）。所有实例停机超过 10 分钟期间的事件不会补推；失败的投递 30 秒、2 分钟后重试，共 3 次。

推送内容只带可跳转的 ID 和简短文案（按设备 `locale` 选中文或英文），不含账号名、用户数据、凭据或上游原始错误：

- APNs：`{"aps":{"alert":{"title":"[P1] 错误率","body":"当前 12.3（阈值 > 5） · 近 5 分钟"},"thread-id":"ops-alert","interruption-level":"time-sensitive","sound":"default"},"type":"ops_alert.fired","site":"api.example.com","severity":"P1","alert_event_id":881,"rule_id":3}`。`account.error` 带 `account_id`、`error_kind`；`account.test_finished` 带 `account_id`、`run_id`、`status`。`site` 为注册时的 `site_tag`，没有时为 `server.frontend_url` 的主机名。
- Webhook：`POST` JSON，字段为 `type`、`title`、`body`、`occurred_at`、`site`、`severity`、`delivery_id` 加上述 ID；请求头 `X-Sub2-Event`、`X-Sub2-Delivery`（同一投递重试时不变，可用于去重）、`X-Sub2-Timestamp`（Unix 秒），设置了 secret 时再带 `X-Sub2-Signature: sha256=<hex>`，值为 `HMAC-SHA256(secret, X-Sub2-Timestamp + "." + 原始请求体)`。2xx 视为成功，410 停用设备，408 / 429 / 5xx 重试，不跟随重定向。
- Bark：`POST {endpoint}/push`，JSON 含 `device_key`、`title`、`body`、`group`、`level`。ntfy：以 JSON 方式发布到 `{endpoint}/`，含 `topic`、`title`、`message`、`priority`（P0→5、P1→4、P2→3、P3→2，其他事件为 3）、`tags`，有 secret 时带 `Authorization: Bearer`。
- Bark / ntfy / Webhook 发送时在连接层再次校验目标 IP，且不走代理；没开 `security.push_targets.allow_private_hosts` 时解析到本机、内网或链路本地地址会被拒绝（`last_error` 为 blocked by SSRF policy）。设备的令牌、secret 与 endpoint 在配置了固定 `TOTP_ENCRYPTION_KEY` 时加密保存（`credentials_encrypted=true`），否则明文保存；更换密钥后已加密的设备会被停用为 `secret_undecryptable`，重新注册即可。
