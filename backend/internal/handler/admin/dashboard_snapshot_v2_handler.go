package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

var dashboardSnapshotV2Cache = newSnapshotCache(30 * time.Second)

type dashboardSnapshotV2Stats struct {
	usagestats.DashboardStats
	Uptime int64 `json:"uptime"`
}

type dashboardSnapshotV2Response struct {
	Query       usagequery.Metadata `json:"query"`
	GeneratedAt string              `json:"generated_at"`

	StartDate   string `json:"start_date"`
	EndDate     string `json:"end_date"`
	Granularity string `json:"granularity"`

	Stats      *dashboardSnapshotV2Stats        `json:"stats,omitempty"`
	Trend      []usagestats.TrendDataPoint      `json:"trend,omitempty"`
	Models     []usagestats.ModelStat           `json:"models,omitempty"`
	Groups     []usagestats.GroupStat           `json:"groups,omitempty"`
	UsersTrend []usagestats.UserUsageTrendPoint `json:"users_trend,omitempty"`
}

type dashboardSnapshotV2Filters struct {
	UserID                int64
	APIKeyID              int64
	AccountID             int64
	GroupID               int64
	Model                 string
	BillingMode           string
	RequestID             string
	RequestType           *int16
	Stream                *bool
	NativeCompactionV2    *bool
	BillingType           *int8
	UpstreamModelMismatch *bool
}

type dashboardSnapshotV2CacheKey struct {
	StartTime             string `json:"start_time"`
	EndTime               string `json:"end_time"`
	Granularity           string `json:"granularity"`
	UserID                int64  `json:"user_id"`
	APIKeyID              int64  `json:"api_key_id"`
	AccountID             int64  `json:"account_id"`
	GroupID               int64  `json:"group_id"`
	Model                 string `json:"model"`
	BillingMode           string `json:"billing_mode"`
	RequestID             string `json:"request_id"`
	RequestType           *int16 `json:"request_type"`
	Stream                *bool  `json:"stream"`
	NativeCompactionV2    *bool  `json:"native_compaction_v2"`
	BillingType           *int8  `json:"billing_type"`
	UpstreamModelMismatch *bool  `json:"upstream_model_mismatch"`
	IncludeStats          bool   `json:"include_stats"`
	IncludeTrend          bool   `json:"include_trend"`
	IncludeModels         bool   `json:"include_models"`
	IncludeGroups         bool   `json:"include_groups"`
	IncludeUsersTrend     bool   `json:"include_users_trend"`
	UsersTrendLimit       int    `json:"users_trend_limit"`
}

func (h *DashboardHandler) GetSnapshotV2(c *gin.Context) {
	startTime, endTime := parseTimeRange(c)
	if c.IsAborted() {
		return
	}
	granularity := strings.TrimSpace(c.DefaultQuery("granularity", "day"))
	if granularity != "hour" {
		granularity = "day"
	}

	includeStats := parseBoolQueryWithDefault(c.Query("include_stats"), true)
	includeTrend := parseBoolQueryWithDefault(c.Query("include_trend"), true)
	includeModels := parseBoolQueryWithDefault(c.Query("include_model_stats"), true)
	includeGroups := parseBoolQueryWithDefault(c.Query("include_group_stats"), false)
	includeUsersTrend := parseBoolQueryWithDefault(c.Query("include_users_trend"), false)
	usersTrendLimit := 12
	if raw := strings.TrimSpace(c.Query("users_trend_limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 50 {
			usersTrendLimit = parsed
		}
	}

	filters, err := parseDashboardSnapshotV2Filters(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	keyRaw, _ := json.Marshal(dashboardSnapshotV2CacheKey{
		StartTime:             startTime.UTC().Format(time.RFC3339Nano),
		EndTime:               endTime.UTC().Format(time.RFC3339Nano),
		Granularity:           granularity,
		UserID:                filters.UserID,
		APIKeyID:              filters.APIKeyID,
		AccountID:             filters.AccountID,
		GroupID:               filters.GroupID,
		Model:                 filters.Model,
		BillingMode:           filters.BillingMode,
		RequestID:             filters.RequestID,
		RequestType:           filters.RequestType,
		Stream:                filters.Stream,
		NativeCompactionV2:    filters.NativeCompactionV2,
		BillingType:           filters.BillingType,
		UpstreamModelMismatch: filters.UpstreamModelMismatch,
		IncludeStats:          includeStats,
		IncludeTrend:          includeTrend,
		IncludeModels:         includeModels,
		IncludeGroups:         includeGroups,
		IncludeUsersTrend:     includeUsersTrend,
		UsersTrendLimit:       usersTrendLimit,
	})
	ctx := queryContext(c)
	cacheKey := scopedUsageCacheKey(ctx, string(keyRaw))

	cached, hit, err := dashboardSnapshotV2Cache.GetOrLoadContext(ctx, cacheKey, func(work context.Context) (any, error) {
		return h.buildSnapshotV2Response(
			work,
			startTime,
			endTime,
			granularity,
			filters,
			includeStats,
			includeTrend,
			includeModels,
			includeGroups,
			includeUsersTrend,
			usersTrendLimit,
		)
	})
	if err != nil {
		response.Error(c, 500, err.Error())
		return
	}
	if cached.ETag != "" {
		c.Header("ETag", cached.ETag)
		c.Header("Vary", "If-None-Match")
		if ifNoneMatchMatched(c.GetHeader("If-None-Match"), cached.ETag) {
			c.Status(http.StatusNotModified)
			return
		}
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))
	response.Success(c, cached.Payload)
}

func (h *DashboardHandler) buildSnapshotV2Response(
	ctx context.Context,
	startTime, endTime time.Time,
	granularity string,
	filters *dashboardSnapshotV2Filters,
	includeStats, includeTrend, includeModels, includeGroups, includeUsersTrend bool,
	usersTrendLimit int,
) (*dashboardSnapshotV2Response, error) {
	resp := &dashboardSnapshotV2Response{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Query:       (usagequery.Range{Start: &startTime, End: &endTime, Timezone: startTime.Location().String()}).Metadata(),
		StartDate:   startTime.Format("2006-01-02"),
		EndDate:     endTime.Add(-time.Nanosecond).Format("2006-01-02"),
		Granularity: granularity,
	}

	if includeStats {
		stats, err := h.dashboardService.GetDashboardStatsFresh(ctx)
		if err != nil {
			return nil, errors.New("failed to get dashboard statistics")
		}
		resp.Stats = &dashboardSnapshotV2Stats{
			DashboardStats: *stats,
			Uptime:         int64(time.Since(h.startTime).Seconds()),
		}
	}

	if includeTrend {
		trend, err := h.dashboardService.GetUsageTrendWithUsageFilters(ctx, startTime, endTime, granularity, filters.usageFilters())
		if err != nil {
			return nil, errors.New("failed to get usage trend")
		}
		resp.Trend = trend
	}

	if includeModels {
		models, err := h.dashboardService.GetModelStatsWithUsageFiltersBySource(ctx, startTime, endTime, filters.usageFilters(), usagestats.ModelSourceRequested)
		if err != nil {
			return nil, errors.New("failed to get model statistics")
		}
		resp.Models = models
	}

	if includeGroups {
		groups, err := h.dashboardService.GetGroupStatsWithUsageFilters(ctx, startTime, endTime, filters.usageFilters())
		if err != nil {
			return nil, errors.New("failed to get group statistics")
		}
		resp.Groups = groups
	}

	if includeUsersTrend {
		usersTrend, err := h.dashboardService.GetUserUsageTrendWithFilters(ctx, startTime, endTime, granularity, usersTrendLimit, filters.usageFilters())
		if err != nil {
			return nil, errors.New("failed to get user usage trend")
		}
		resp.UsersTrend = usersTrend
	}

	return resp, nil
}

func parseDashboardSnapshotV2Filters(c *gin.Context) (*dashboardSnapshotV2Filters, error) {
	filters := &dashboardSnapshotV2Filters{
		Model:       strings.TrimSpace(c.Query("model")),
		BillingMode: strings.TrimSpace(c.Query("billing_mode")),
		RequestID:   strings.TrimSpace(c.Query("request_id")),
	}
	if filters.BillingMode != "" && !service.BillingMode(filters.BillingMode).IsValidUsageFilter() {
		return nil, errors.New("invalid billing_mode")
	}

	if userIDStr := strings.TrimSpace(c.Query("user_id")); userIDStr != "" {
		id, err := strconv.ParseInt(userIDStr, 10, 64)
		if err != nil {
			return nil, err
		}
		filters.UserID = id
	}
	if apiKeyIDStr := strings.TrimSpace(c.Query("api_key_id")); apiKeyIDStr != "" {
		id, err := strconv.ParseInt(apiKeyIDStr, 10, 64)
		if err != nil {
			return nil, err
		}
		filters.APIKeyID = id
	}
	if accountIDStr := strings.TrimSpace(c.Query("account_id")); accountIDStr != "" {
		id, err := strconv.ParseInt(accountIDStr, 10, 64)
		if err != nil {
			return nil, err
		}
		filters.AccountID = id
	}
	if groupIDStr := strings.TrimSpace(c.Query("group_id")); groupIDStr != "" {
		id, err := strconv.ParseInt(groupIDStr, 10, 64)
		if err != nil {
			return nil, err
		}
		filters.GroupID = id
	}

	if requestTypeStr := strings.TrimSpace(c.Query("request_type")); requestTypeStr != "" {
		parsed, err := service.ParseUsageRequestType(requestTypeStr)
		if err != nil {
			return nil, err
		}
		value := int16(parsed)
		filters.RequestType = &value
	} else if streamStr := strings.TrimSpace(c.Query("stream")); streamStr != "" {
		streamVal, err := strconv.ParseBool(streamStr)
		if err != nil {
			return nil, err
		}
		filters.Stream = &streamVal
	}

	if nativeCompactionV2Str := strings.TrimSpace(c.Query("native_compaction_v2")); nativeCompactionV2Str != "" {
		value, err := strconv.ParseBool(nativeCompactionV2Str)
		if err != nil {
			return nil, err
		}
		filters.NativeCompactionV2 = &value
	}

	if billingTypeStr := strings.TrimSpace(c.Query("billing_type")); billingTypeStr != "" {
		v, err := strconv.ParseInt(billingTypeStr, 10, 8)
		if err != nil {
			return nil, err
		}
		bt := int8(v)
		filters.BillingType = &bt
	}

	if mismatchStr := strings.TrimSpace(c.Query("upstream_model_mismatch")); mismatchStr != "" {
		value, err := strconv.ParseBool(mismatchStr)
		if err != nil {
			return nil, err
		}
		filters.UpstreamModelMismatch = &value
	}

	return filters, nil
}

func (f *dashboardSnapshotV2Filters) usageFilters() usagestats.UsageLogFilters {
	return usagestats.UsageLogFilters{UserID: f.UserID, APIKeyID: f.APIKeyID, AccountID: f.AccountID, GroupID: f.GroupID, Model: f.Model, ModelFilterSource: usagestats.ModelSourceRequested, RequestID: f.RequestID, BillingMode: f.BillingMode, RequestType: f.RequestType, Stream: f.Stream, NativeCompactionV2: f.NativeCompactionV2, BillingType: f.BillingType, UpstreamModelMismatch: f.UpstreamModelMismatch}
}
