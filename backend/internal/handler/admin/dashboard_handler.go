package admin

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// DashboardHandler handles admin dashboard statistics
type DashboardHandler struct {
	dashboardService   *service.DashboardService
	aggregationService *service.DashboardAggregationService
	startTime          time.Time // Server start time for uptime calculation
	home               dashboardHomeBuilder
}

// NewDashboardHandler creates a new admin dashboard handler
func NewDashboardHandler(dashboardService *service.DashboardService, aggregationService *service.DashboardAggregationService) *DashboardHandler {
	return &DashboardHandler{
		dashboardService:   dashboardService,
		aggregationService: aggregationService,
		startTime:          time.Now(),
	}
}

// parseTimeRange parses start_date, end_date query parameters
// Uses user's timezone if provided, otherwise falls back to server timezone
func parseTimeRange(c *gin.Context) (time.Time, time.Time) {
	userTZ := c.Query("timezone") // Get user's timezone from request
	now := timezone.NowInUserLocation(userTZ)
	start := timezone.StartOfDayInUserLocation(now.AddDate(0, 0, -7), userTZ)
	end := timezone.StartOfDayInUserLocation(now.AddDate(0, 0, 1), userTZ)
	r, err := usagequery.ParseRange(c.Request.URL.Query(), &start, &end)
	if err != nil {
		response.BadRequest(c, err.Error())
		c.Abort()
		return time.Time{}, time.Time{}
	}
	c.Set("usage_query_range", r)
	return *r.Start, *r.End
}

func dashboardQueryMetadata(c *gin.Context) usagequery.Metadata {
	r, _ := c.Get("usage_query_range")
	if queryRange, ok := r.(usagequery.Range); ok {
		return queryRange.Metadata()
	}
	return usagequery.Metadata{GeneratedAt: time.Now().UTC()}
}

// parseOptionalBoolDashboardFilter reads an optional boolean filter. A blank
// value means not set; an invalid value yields a message naming the parameter.
func parseOptionalBoolDashboardFilter(c *gin.Context, name string) (*bool, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, invalidDashboardParam(name, "use true or false")
	}
	return &value, nil
}

// GetStats handles getting dashboard statistics
// GET /api/v1/admin/dashboard/stats
func (h *DashboardHandler) GetStats(c *gin.Context) {
	stats, err := h.dashboardService.GetDashboardStats(c.Request.Context())
	if err != nil {
		response.Error(c, 500, "Failed to get dashboard statistics")
		return
	}

	// Calculate uptime in seconds
	uptime := int64(time.Since(h.startTime).Seconds())

	body := gin.H{
		// 用户统计
		"total_users":     stats.TotalUsers,
		"today_new_users": stats.TodayNewUsers,
		"active_users":    stats.ActiveUsers,

		// API Key 统计
		"total_api_keys":  stats.TotalAPIKeys,
		"active_api_keys": stats.ActiveAPIKeys,

		// 账户统计
		"total_accounts":     stats.TotalAccounts,
		"normal_accounts":    stats.NormalAccounts,
		"error_accounts":     stats.ErrorAccounts,
		"ratelimit_accounts": stats.RateLimitAccounts,
		"overload_accounts":  stats.OverloadAccounts,

		// 累计 Token 使用统计
		"total_requests":              stats.TotalRequests,
		"total_input_tokens":          stats.TotalInputTokens,
		"total_output_tokens":         stats.TotalOutputTokens,
		"total_cache_creation_tokens": stats.TotalCacheCreationTokens,
		"total_cache_read_tokens":     stats.TotalCacheReadTokens,
		"total_tokens":                stats.TotalTokens,
		"total_cost":                  stats.TotalCost,        // 标准计费
		"total_actual_cost":           stats.TotalActualCost,  // 实际扣除
		"total_account_cost":          stats.TotalAccountCost, // 账号成本

		// 今日 Token 使用统计
		"today_requests":              stats.TodayRequests,
		"today_input_tokens":          stats.TodayInputTokens,
		"today_output_tokens":         stats.TodayOutputTokens,
		"today_cache_creation_tokens": stats.TodayCacheCreationTokens,
		"today_cache_read_tokens":     stats.TodayCacheReadTokens,
		"today_tokens":                stats.TodayTokens,
		"today_cost":                  stats.TodayCost,        // 今日标准计费
		"today_actual_cost":           stats.TodayActualCost,  // 今日实际扣除
		"today_account_cost":          stats.TodayAccountCost, // 今日账号成本

		// 系统运行统计
		"average_duration_ms": stats.AverageDurationMs,
		"uptime":              uptime,

		// 性能指标
		"rpm":           stats.Rpm,
		"rpm_available": stats.RPMAvailable,
		"tpm":           stats.Tpm,

		// 预聚合新鲜度
		"hourly_active_users": stats.HourlyActiveUsers,
		"stats_updated_at":    stats.StatsUpdatedAt,
		"stats_stale":         stats.StatsStale,
	}
	// uptime ticks every second; leave it out so unchanged stats revalidate.
	basis := maps.Clone(body)
	delete(basis, "uptime")
	writeSuccessWithETag(c, basis, body)
}

type DashboardAggregationBackfillRequest struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// BackfillAggregation handles triggering aggregation backfill
// POST /api/v1/admin/dashboard/aggregation/backfill
func (h *DashboardHandler) BackfillAggregation(c *gin.Context) {
	if h.aggregationService == nil {
		response.InternalError(c, "Aggregation service not available")
		return
	}

	var req DashboardAggregationBackfillRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	start, err := time.Parse(time.RFC3339, req.Start)
	if err != nil {
		response.BadRequest(c, "Invalid start time")
		return
	}
	end, err := time.Parse(time.RFC3339, req.End)
	if err != nil {
		response.BadRequest(c, "Invalid end time")
		return
	}

	if err := h.aggregationService.TriggerBackfill(start, end); err != nil {
		if errors.Is(err, service.ErrDashboardBackfillDisabled) {
			response.Forbidden(c, "Backfill is disabled")
			return
		}
		if errors.Is(err, service.ErrDashboardBackfillTooLarge) {
			response.BadRequest(c, "Backfill range too large")
			return
		}
		response.InternalError(c, "Failed to trigger backfill")
		return
	}

	response.Success(c, gin.H{
		"status": "accepted",
	})
}

// GetRealtimeMetrics handles getting real-time system metrics
// GET /api/v1/admin/dashboard/realtime
func (h *DashboardHandler) GetRealtimeMetrics(c *gin.Context) {
	// Return mock data for now
	response.Success(c, gin.H{
		"active_requests":       0,
		"requests_per_minute":   0,
		"average_response_time": 0,
		"error_rate":            0.0,
	})
}

// GetUsageTrend handles getting usage trend data
// GET /api/v1/admin/dashboard/trend
// Query params: start_date, end_date (YYYY-MM-DD), granularity (day/hour), user_id, api_key_id, model, account_id, group_id, request_type, stream, billing_type
func (h *DashboardHandler) GetUsageTrend(c *gin.Context) {
	startTime, endTime := parseTimeRange(c)
	if c.IsAborted() {
		return
	}
	filters, err := parseDashboardSnapshotV2Filters(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	granularity := c.DefaultQuery("granularity", "day")
	trend, hit, err := h.getUsageTrendCached(queryContext(c), startTime, endTime, granularity, filters.UserID, filters.APIKeyID, filters.AccountID, filters.GroupID, filters.Model, filters.RequestType, filters.Stream, filters.NativeCompactionV2, filters.BillingType, filters.UpstreamModelMismatch, filters.usageFilters())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))
	response.Success(c, gin.H{
		"trend": trend, "granularity": granularity,
		"start_date": startTime.Format("2006-01-02"), "end_date": endTime.Add(-time.Nanosecond).Format("2006-01-02"),
		"query": dashboardQueryMetadata(c),
	})
}

// GetModelStats handles getting model usage statistics
// GET /api/v1/admin/dashboard/models
// Query params: start_date, end_date (YYYY-MM-DD), user_id, api_key_id, account_id, group_id, request_type, stream, billing_type
func (h *DashboardHandler) GetModelStats(c *gin.Context) {
	startTime, endTime := parseTimeRange(c)
	if c.IsAborted() {
		return
	}
	filters, err := parseDashboardSnapshotV2Filters(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	modelSource := strings.TrimSpace(c.DefaultQuery("model_source", usagestats.ModelSourceRequested))
	if !usagestats.IsValidModelSource(modelSource) {
		response.BadRequest(c, "Invalid model_source, use requested/upstream/mapping")
		return
	}
	stats, hit, err := h.getModelStatsCached(queryContext(c), startTime, endTime, filters.UserID, filters.APIKeyID, filters.AccountID, filters.GroupID, modelSource, filters.RequestType, filters.Stream, filters.NativeCompactionV2, filters.BillingType, filters.UpstreamModelMismatch, filters.usageFilters())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))
	response.Success(c, gin.H{
		"models":     stats,
		"start_date": startTime.Format("2006-01-02"), "end_date": endTime.Add(-time.Nanosecond).Format("2006-01-02"),
		"query": dashboardQueryMetadata(c),
	})
}

// GetGroupStats handles getting group usage statistics
// GET /api/v1/admin/dashboard/groups
// Query params: start_date, end_date (YYYY-MM-DD), user_id, api_key_id, account_id, group_id, request_type, stream, billing_type
func (h *DashboardHandler) GetGroupStats(c *gin.Context) {
	startTime, endTime := parseTimeRange(c)
	if c.IsAborted() {
		return
	}
	filters, err := parseDashboardSnapshotV2Filters(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	stats, hit, err := h.getGroupStatsCached(queryContext(c), startTime, endTime, filters.UserID, filters.APIKeyID, filters.AccountID, filters.GroupID, filters.RequestType, filters.Stream, filters.NativeCompactionV2, filters.BillingType, filters.UpstreamModelMismatch, filters.usageFilters())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))
	response.Success(c, gin.H{
		"groups":     stats,
		"start_date": startTime.Format("2006-01-02"), "end_date": endTime.Add(-time.Nanosecond).Format("2006-01-02"),
		"query": dashboardQueryMetadata(c),
	})
}

// GetAPIKeyUsageTrend handles getting API key usage trend data
// GET /api/v1/admin/dashboard/api-keys-trend
// Query params: start_date, end_date (YYYY-MM-DD), granularity (day/hour), limit (default 5)
func (h *DashboardHandler) GetAPIKeyUsageTrend(c *gin.Context) {
	startTime, endTime := parseTimeRange(c)
	if c.IsAborted() {
		return
	}
	granularity := c.DefaultQuery("granularity", "day")
	limitStr := c.DefaultQuery("limit", "5")
	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit <= 0 {
		limit = 5
	}

	filters, err := parseDashboardSnapshotV2Filters(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	trend, hit, err := h.getAPIKeyUsageTrendCached(queryContext(c), startTime, endTime, granularity, limit, filters.usageFilters())
	if err != nil {
		response.Error(c, 500, "Failed to get API key usage trend")
		return
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))

	response.Success(c, gin.H{
		"query":       dashboardQueryMetadata(c),
		"trend":       trend,
		"start_date":  startTime.Format("2006-01-02"),
		"end_date":    endTime.Add(-time.Nanosecond).Format("2006-01-02"),
		"granularity": granularity,
	})
}

// GetUserUsageTrend handles getting user usage trend data
// GET /api/v1/admin/dashboard/users-trend
// Query params: start_date, end_date (YYYY-MM-DD), granularity (day/hour), limit (default 12),
// metric (tokens|actual_cost, default tokens; picks the top users with or without
// the shared dashboard filters such as group_id and model)
func (h *DashboardHandler) GetUserUsageTrend(c *gin.Context) {
	startTime, endTime := parseTimeRange(c)
	if c.IsAborted() {
		return
	}
	granularity := c.DefaultQuery("granularity", "day")
	limitStr := c.DefaultQuery("limit", "12")
	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit <= 0 {
		limit = 12
	}

	filters, err := parseDashboardSnapshotV2Filters(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	metric := c.DefaultQuery("metric", "tokens")
	if metric != "tokens" && metric != "actual_cost" {
		metric = "tokens"
	}
	usageFilters := filters.usageFilters()
	var trend []usagestats.UserUsageTrendPoint
	var hit bool
	if usageFilters.UserID != 0 || usageFilters.APIKeyID != 0 || usageFilters.AccountID != 0 || usageFilters.GroupID != 0 || usageFilters.Model != "" || usageFilters.BillingMode != "" || usageFilters.RequestID != "" || usageFilters.RequestType != nil || usageFilters.Stream != nil || usageFilters.NativeCompactionV2 != nil || usageFilters.BillingType != nil || usageFilters.UpstreamModelMismatch != nil {
		trend, hit, err = h.getUserUsageTrendCachedWithFilters(queryContext(c), startTime, endTime, granularity, limit, metric, usageFilters)
	} else {
		trend, hit, err = h.getUserUsageTrendCached(queryContext(c), startTime, endTime, granularity, limit, metric)
	}
	if err != nil {
		response.Error(c, 500, "Failed to get user usage trend")
		return
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))

	response.Success(c, gin.H{
		"query":       dashboardQueryMetadata(c),
		"trend":       trend,
		"start_date":  startTime.Format("2006-01-02"),
		"end_date":    endTime.Add(-time.Nanosecond).Format("2006-01-02"),
		"granularity": granularity,
	})
}

// BatchUsersUsageRequest represents the request body for batch user usage stats
type BatchUsersUsageRequest struct {
	UserIDs []int64 `json:"user_ids" binding:"required"`
}

var dashboardUsersRankingCache = newSnapshotCache(30 * time.Second)
var dashboardBatchUsersUsageCache = newSnapshotCache(30 * time.Second)
var dashboardBatchAPIKeysUsageCache = newSnapshotCache(30 * time.Second)

func parseRankingLimit(raw string) int {
	limit, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || limit <= 0 {
		return 12
	}
	if limit > 50 {
		return 50
	}
	return limit
}

// GetUserSpendingRanking handles getting user spending ranking data.
// GET /api/v1/admin/dashboard/users-ranking
// Query params: shared dashboard filters, limit (default 12, max 50),
// sort_by (actual_cost | requests | tokens, default actual_cost)
func (h *DashboardHandler) GetUserSpendingRanking(c *gin.Context) {
	startTime, endTime := parseTimeRange(c)
	if c.IsAborted() {
		return
	}
	limit := parseRankingLimit(c.DefaultQuery("limit", "12"))
	filters, err := parseDashboardSnapshotV2Filters(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	sortBy, err := parseUserRankingSort(c)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	ranking, hit, err := loadDashboardQuery(queryContext(c), dashboardUsersRankingCache, startTime, endTime, filters.usageFilters(), strconv.Itoa(limit)+":"+sortBy, func(work context.Context) (*usagestats.UserSpendingRankingResponse, error) {
		return h.dashboardService.GetUserSpendingRankingSorted(work, startTime, endTime, limit, sortBy, filters.usageFilters())
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))
	response.Success(c, gin.H{"ranking": ranking.Ranking, "total_users": ranking.TotalUsers, "total_actual_cost": ranking.TotalActualCost, "total_cost": ranking.TotalCost, "total_account_cost": ranking.TotalAccountCost, "total_requests": ranking.TotalRequests, "total_tokens": ranking.TotalTokens, "start_date": startTime.Format("2006-01-02"), "end_date": endTime.Add(-time.Nanosecond).Format("2006-01-02"), "query": dashboardQueryMetadata(c)})
}

var dashboardAccountsRankingCache = newSnapshotCache(30 * time.Second)

const (
	accountsRankingDefaultLimit = 10
	accountsRankingMaxLimit     = 50
)

// GetAccountSpendingRanking ranks accounts by actual charged cost; the window
// defaults to today in the requested timezone.
// GET /api/v1/admin/dashboard/accounts-ranking
func (h *DashboardHandler) GetAccountSpendingRanking(c *gin.Context) {
	userTZ := c.Query("timezone")
	todayStart := timezone.StartOfDayInUserLocation(timezone.NowInUserLocation(userTZ), userTZ)
	todayEnd := todayStart.AddDate(0, 0, 1)
	queryRange, err := usagequery.ParseRange(c.Request.URL.Query(), &todayStart, &todayEnd)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	c.Set("usage_query_range", queryRange)
	startTime, endTime := *queryRange.Start, *queryRange.End

	limit := accountsRankingDefaultLimit
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			response.BadRequest(c, "limit must be a positive integer")
			return
		}
		limit = min(parsed, accountsRankingMaxLimit)
	}
	var groupID int64
	if raw := strings.TrimSpace(c.Query("group_id")); raw != "" {
		groupID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || groupID <= 0 {
			response.BadRequest(c, "invalid group_id")
			return
		}
	}
	platform := strings.TrimSpace(c.Query("platform"))

	params := usagestats.AccountSpendingRankingParams{Start: startTime, End: endTime, Limit: limit, Platform: platform, GroupID: groupID}
	filters := usagestats.UsageLogFilters{GroupID: groupID}
	dimension := "accounts:" + strconv.Itoa(limit) + ":" + platform
	ranking, hit, err := loadDashboardQuery(queryContext(c), dashboardAccountsRankingCache, startTime, endTime, filters, dimension, func(work context.Context) (*usagestats.AccountSpendingRankingResponse, error) {
		return h.dashboardService.GetAccountSpendingRanking(work, params)
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))
	response.Success(c, gin.H{
		"ranking":           ranking.Ranking,
		"total_actual_cost": ranking.TotalActualCost,
		"start_date":        startTime.Format("2006-01-02"),
		"end_date":          endTime.Add(-time.Nanosecond).Format("2006-01-02"),
		"query":             dashboardQueryMetadata(c),
	})
}

// GetBatchUsersUsage handles getting usage stats for multiple users
// POST /api/v1/admin/dashboard/users-usage
func (h *DashboardHandler) GetBatchUsersUsage(c *gin.Context) {
	var req BatchUsersUsageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	userIDs := normalizeInt64IDList(req.UserIDs)
	if len(userIDs) == 0 {
		response.Success(c, gin.H{"stats": map[string]any{}})
		return
	}
	if len(userIDs) > service.MaxBatchUsageIDs {
		response.ErrorFrom(c, service.ErrTooManyUsageUserIDs)
		return
	}

	// cacheKey 必须包含当日日期，否则跨午夜后 30s 内会复用昨天的 "today_*" 结果。
	keyRaw, _ := json.Marshal(struct {
		V       int     `json:"v"`
		Day     string  `json:"day"`
		UserIDs []int64 `json:"user_ids"`
	}{
		V:       2, // bump 当响应结构变化（如加入 by_platform 时）
		Day:     timezone.Today().Format("2006-01-02"),
		UserIDs: userIDs,
	})
	ctx := queryContext(c)
	cacheKey := scopedUsageCacheKey(ctx, string(keyRaw))
	entry, hit, err := dashboardBatchUsersUsageCache.GetOrLoadContext(ctx, cacheKey, func(work context.Context) (any, error) {
		stats, err := h.dashboardService.GetBatchUserUsageStats(work, userIDs, time.Time{}, time.Time{})
		if err != nil {
			return nil, err
		}
		return gin.H{"stats": stats}, nil
	})
	if err != nil {
		response.Error(c, 500, "Failed to get user usage stats")
		return
	}

	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))
	response.Success(c, entry.Payload)
}

// BatchAPIKeysUsageRequest represents the request body for batch api key usage stats
type BatchAPIKeysUsageRequest struct {
	APIKeyIDs []int64 `json:"api_key_ids" binding:"required"`
}

// GetBatchAPIKeysUsage handles getting usage stats for multiple API keys
// POST /api/v1/admin/dashboard/api-keys-usage
func (h *DashboardHandler) GetBatchAPIKeysUsage(c *gin.Context) {
	var req BatchAPIKeysUsageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	apiKeyIDs := normalizeInt64IDList(req.APIKeyIDs)
	if len(apiKeyIDs) == 0 {
		response.Success(c, gin.H{"stats": map[string]any{}})
		return
	}
	if len(apiKeyIDs) > service.MaxBatchUsageIDs {
		response.ErrorFrom(c, service.ErrTooManyUsageAPIKeyIDs)
		return
	}

	keyRaw, _ := json.Marshal(struct {
		Day       string  `json:"day"`
		APIKeyIDs []int64 `json:"api_key_ids"`
	}{
		Day:       timezone.Today().Format("2006-01-02"),
		APIKeyIDs: apiKeyIDs,
	})
	ctx := queryContext(c)
	cacheKey := scopedUsageCacheKey(ctx, string(keyRaw))
	entry, hit, err := dashboardBatchAPIKeysUsageCache.GetOrLoadContext(ctx, cacheKey, func(work context.Context) (any, error) {
		stats, err := h.dashboardService.GetBatchAPIKeyUsageStats(work, apiKeyIDs, time.Time{}, time.Time{})
		if err != nil {
			return nil, err
		}
		return gin.H{"stats": stats}, nil
	})
	if err != nil {
		response.Error(c, 500, "Failed to get API key usage stats")
		return
	}

	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))
	response.Success(c, entry.Payload)
}

// GetUserBreakdown handles getting per-user usage breakdown within a dimension.
// GET /api/v1/admin/dashboard/user-breakdown
// Query params: start_date, end_date (or start_time, end_time, timezone), the
// shared dashboard filters (user_id, api_key_id, account_id, group_id, model,
// model_source, request_type, stream, native_compaction_v2, billing_type,
// billing_mode, request_id, upstream_model_mismatch), requested_model,
// endpoint, endpoint_type, sort_by, limit (1-200, default 50).
// Any malformed filter or limit returns 400 naming the parameter.
func (h *DashboardHandler) GetUserBreakdown(c *gin.Context) {
	startTime, endTime := parseTimeRange(c)
	if c.IsAborted() {
		return
	}

	dim, limit, err := parseUserBreakdownQuery(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	stats, err := h.dashboardService.GetUserBreakdownStats(
		c.Request.Context(), startTime, endTime, dim, limit,
	)
	if err != nil {
		response.Error(c, 500, "Failed to get user breakdown stats")
		return
	}

	response.Success(c, gin.H{
		"query":      dashboardQueryMetadata(c),
		"users":      stats,
		"start_date": startTime.Format("2006-01-02"),
		"end_date":   endTime.Add(-time.Nanosecond).Format("2006-01-02"),
	})
}
