package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/quotaview"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// 单平台限额接口（后端适配清单 E07）：只改 / 删 / 恢复一个平台，原有整表 PUT 不变。
//   PUT    /admin/users/:id/platform-quotas/:platform
//   DELETE /admin/users/:id/platform-quotas/:platform
//   POST   /admin/users/:id/platform-quotas/:platform/restore

// maxQuotaItemBodyBytes 单平台 PUT 请求体上限：三个数字字段，1KB 足够。
const maxQuotaItemBodyBytes = 1 << 10

// UpsertUserPlatformQuotaRequest 是 PUT /admin/users/:id/platform-quotas/:platform 的请求体。
// 字段缺失或为 null 表示该档不限；三档都为空等于删除该平台的限额。
type UpsertUserPlatformQuotaRequest struct {
	DailyLimitUSD   *float64 `json:"daily_limit_usd"`
	WeeklyLimitUSD  *float64 `json:"weekly_limit_usd"`
	MonthlyLimitUSD *float64 `json:"monthly_limit_usd"`
}

// maxQuotaLimitUSD 限额上限（不含）：user_platform_quotas 的限额列是 decimal(20,10)，
// 只能存绝对值小于 1e10 的数，超出时数据库报 numeric field overflow，接口会变成 500。
const maxQuotaLimitUSD = 1e10

// validateQuotaLimitFields 校验三档限额，返回空串表示合法，否则返回 400 文案（整表 PUT 与单平台 PUT 共用）。
//
//	nil / not set → 无限额（完全放行）
//	0            → 完全禁用（任何请求都会被拒绝，因为 usage >= 0 恒成立）
//	> 0          → USD 限额上限（必须小于 maxQuotaLimitUSD）
//
// 拦截 NaN / ±Inf：+Inf 进入 DB 后 cache check 中 usage >= limit 永不成立，limit 等同失效。
func validateQuotaLimitFields(daily, weekly, monthly *float64) string {
	for _, f := range []struct {
		name string
		val  *float64
	}{
		{"daily_limit_usd", daily},
		{"weekly_limit_usd", weekly},
		{"monthly_limit_usd", monthly},
	} {
		if f.val == nil {
			continue
		}
		v := *f.val
		if v < 0 {
			return f.name + " must be >= 0"
		}
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return f.name + " must be a finite number"
		}
		if v >= maxQuotaLimitUSD {
			return fmt.Sprintf("%s must be less than %.0f", f.name, maxQuotaLimitUSD)
		}
	}
	return ""
}

// parseUserPlatformQuotaPath 校验仓储是否可用并解析 :id / :platform，失败时已写好错误响应。
func (h *UserHandler) parseUserPlatformQuotaPath(c *gin.Context) (int64, string, bool) {
	if h.userPlatformQuotaRepo == nil {
		response.ErrorFrom(c, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "platform quota service not available"))
		return 0, "", false
	}
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_USER_ID", "Invalid user ID"))
		return 0, "", false
	}
	platform := c.Param("platform")
	if !service.IsAllowedQuotaPlatform(platform) {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_PLATFORM", "invalid platform: "+platform))
		return 0, "", false
	}
	return userID, platform, true
}

// decodeUpsertUserPlatformQuotaRequest 严格解码：必须是一个 JSON 对象、不允许未知字段与尾随数据。
// 字段名拼错（如 daily_limit）若被静默忽略，会变成三档全空而把该平台的限额删掉。
func decodeUpsertUserPlatformQuotaRequest(c *gin.Context) (UpsertUserPlatformQuotaRequest, error) {
	var req UpsertUserPlatformQuotaRequest
	if c.Request.Body == nil || c.Request.Body == http.NoBody {
		return req, errors.New("request body must be a JSON object")
	}
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxQuotaItemBodyBytes))
	if err != nil {
		return req, fmt.Errorf("read request body: %w", err)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return req, errors.New("request body must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, err
	}
	if dec.More() {
		return req, errors.New("request body must contain a single JSON object")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return req, errors.New("request body must contain a single JSON object")
	}
	return req, nil
}

// UpsertUserPlatformQuota PUT /admin/users/:id/platform-quotas/:platform
// 替换这一个平台的三档限额（缺失 = 不限），三档全空等于删除；其他平台不动。
func (h *UserHandler) UpsertUserPlatformQuota(c *gin.Context) {
	userID, platform, ok := h.parseUserPlatformQuotaPath(c)
	if !ok {
		return
	}
	req, err := decodeUpsertUserPlatformQuotaRequest(c)
	if err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("VALIDATION_ERROR", "Invalid request: "+err.Error()))
		return
	}
	if msg := validateQuotaLimitFields(req.DailyLimitUSD, req.WeeklyLimitUSD, req.MonthlyLimitUSD); msg != "" {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_QUOTA_LIMIT", msg))
		return
	}

	ctx := c.Request.Context()
	if _, err := h.adminService.GetUser(ctx, userID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	rec := service.UserPlatformQuotaRecord{
		UserID:          userID,
		Platform:        platform,
		DailyLimitUSD:   req.DailyLimitUSD,
		WeeklyLimitUSD:  req.WeeklyLimitUSD,
		MonthlyLimitUSD: req.MonthlyLimitUSD,
	}
	current, previous, err := h.userPlatformQuotaRepo.UpsertOneForUser(ctx, userID, rec)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	change := map[string]any{"platform": platform}
	if current != nil {
		change["daily_limit_usd"] = current.DailyLimitUSD
		change["weekly_limit_usd"] = current.WeeklyLimitUSD
		change["monthly_limit_usd"] = current.MonthlyLimitUSD
	} else if previous != nil {
		change["removed"] = true
	}
	addQuotaAuditBefore(change, previous)
	slog.Info("admin.quota_updated",
		"actor_admin_id", getAdminIDFromContext(c),
		"target_user_id", userID,
		"mode", "single",
		"platform", platform,
		"before_snapshot_available", true,
		"changes", []map[string]any{change})

	h.invalidateUserPlatformQuotaCache(c, userID, platform, "UpsertOneForUser")

	now := time.Now().UTC()
	response.Success(c, gin.H{
		"platform_quota": quotaItemView(current, now),
		"previous":       quotaItemView(previous, now),
	})
}

// DeleteUserPlatformQuota DELETE /admin/users/:id/platform-quotas/:platform
// 软删这一个平台的限额；没有活跃限额时 404 USER_PLATFORM_QUOTA_NOT_FOUND。
func (h *UserHandler) DeleteUserPlatformQuota(c *gin.Context) {
	userID, platform, ok := h.parseUserPlatformQuotaPath(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if _, err := h.adminService.GetUser(ctx, userID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	previous, err := h.userPlatformQuotaRepo.SoftDeleteOne(ctx, userID, platform)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	change := map[string]any{"platform": platform, "removed": true}
	addQuotaAuditBefore(change, previous)
	slog.Info("admin.quota_updated",
		"actor_admin_id", getAdminIDFromContext(c),
		"target_user_id", userID,
		"mode", "single",
		"platform", platform,
		"before_snapshot_available", true,
		"changes", []map[string]any{change})

	h.invalidateUserPlatformQuotaCache(c, userID, platform, "SoftDeleteOne")

	response.Success(c, gin.H{
		"removed":  true,
		"previous": quotaItemView(previous, time.Now().UTC()),
	})
}

// RestoreUserPlatformQuota POST /admin/users/:id/platform-quotas/:platform/restore
// 恢复该平台最近一次软删的限额（含当时的用量），用于撤销“取消限额”。
// 已有活跃限额 409 USER_PLATFORM_QUOTA_ACTIVE_EXISTS；没有可恢复的记录 404 USER_PLATFORM_QUOTA_NOT_FOUND。
func (h *UserHandler) RestoreUserPlatformQuota(c *gin.Context) {
	userID, platform, ok := h.parseUserPlatformQuotaPath(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if _, err := h.adminService.GetUser(ctx, userID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	restored, err := h.userPlatformQuotaRepo.RestoreLatestDeleted(ctx, userID, platform)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	audit := []any{
		"actor_admin_id", getAdminIDFromContext(c),
		"target_user_id", userID,
		"platform", platform,
	}
	if restored != nil {
		audit = append(audit,
			"daily_limit_usd", restored.DailyLimitUSD,
			"weekly_limit_usd", restored.WeeklyLimitUSD,
			"monthly_limit_usd", restored.MonthlyLimitUSD)
	}
	slog.Info("admin.quota_restored", audit...)

	h.invalidateUserPlatformQuotaCache(c, userID, platform, "RestoreLatestDeleted")

	response.Success(c, gin.H{
		"platform_quota": quotaItemView(restored, time.Now().UTC()),
		"restored":       true,
	})
}

// invalidateUserPlatformQuotaCache 只失效这一个平台的限额缓存；失败按既有 ALERT 文案记日志，不影响响应。
func (h *UserHandler) invalidateUserPlatformQuotaCache(c *gin.Context, userID int64, platform, op string) {
	if h.billingCache == nil {
		return
	}
	if err := h.billingCache.DeleteUserPlatformQuotaCache(c.Request.Context(), userID, platform); err != nil {
		slog.Error("ALERT: quota cache invalidation failed after "+op+"; limit 生效可能延迟至 sentinel TTL(最长 1h),需人工确认或重试失效",
			"user_id", userID, "platform", platform, "err", err)
	}
}

// addQuotaAuditBefore 给审计条目补上写入前的三档限额（与整表 PUT 的 changes 结构一致）。
func addQuotaAuditBefore(change map[string]any, previous *service.UserPlatformQuotaRecord) {
	if previous == nil {
		return
	}
	change["before_daily_limit_usd"] = previous.DailyLimitUSD
	change["before_weekly_limit_usd"] = previous.WeeklyLimitUSD
	change["before_monthly_limit_usd"] = previous.MonthlyLimitUSD
}

// quotaItemView 生成与 GET platform-quotas 单项相同的结构（含 window_start）；nil 输出 JSON null。
func quotaItemView(rec *service.UserPlatformQuotaRecord, now time.Time) any {
	if rec == nil {
		return nil
	}
	return quotaview.LazyZeroQuotaForResponse(*rec, now, true)
}
