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
| 404 | OPS_DISABLED | 显示“运维监控未开启” |
| 400 | TOTP_INVALID_CODE、TURNSTILE_VERIFICATION_FAILED、TENCENT_CAPTCHA_VERIFICATION_FAILED、ALIYUN_CAPTCHA_VERIFICATION_FAILED | 重新输入 |
| 400 | TOTP_LOGIN_SESSION_EXPIRED | 登录第二步超时，回到密码页重新登录 |
| 429 | RATE_LIMITED、TOTP_TOO_MANY_ATTEMPTS | 稍后再试（RATE_LIMITED 按 `retry_after` 秒） |
| 503 | STEP_UP_UNAVAILABLE、SERVICE_UNAVAILABLE、CAPTCHA_PROVIDER_CONFLICT | 暂时性错误，保留会话，稍后重试 |

`TOKEN_REVOKED` 表示会话已被撤销（退出登录、在其他设备上撤销、撤销全部会话、刷新令牌重放、改密），不要再用同一 refresh token 续期。
