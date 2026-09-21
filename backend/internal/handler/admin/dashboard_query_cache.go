package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

var (
	dashboardTrendCache        = newSnapshotCache(30 * time.Second)
	dashboardModelStatsCache   = newSnapshotCache(30 * time.Second)
	dashboardGroupStatsCache   = newSnapshotCache(30 * time.Second)
	dashboardUsersTrendCache   = newSnapshotCache(30 * time.Second)
	dashboardAPIKeysTrendCache = newSnapshotCache(30 * time.Second)
)

func cacheStatusValue(hit bool) string {
	if hit {
		return "hit"
	}
	return "miss"
}

func mustMarshalDashboardCacheKey(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func snapshotPayloadAs[T any](payload any) (T, error) {
	typed, ok := payload.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("unexpected cache payload type %T", payload)
	}
	return typed, nil
}

func loadDashboardQuery[T any](ctx context.Context, cache *snapshotCache, start, end time.Time, filters usagestats.UsageLogFilters, dimension string, load func(context.Context) (T, error)) (T, bool, error) {
	filters.StartTime, filters.EndTime = &start, &end
	key := scopedUsageCacheKey(ctx, mustMarshalDashboardCacheKey(struct {
		Filters   usagestats.UsageLogFilters
		Dimension string
	}{filters, dimension}))
	entry, hit, err := cache.GetOrLoadContext(ctx, key, func(work context.Context) (any, error) { return load(work) })
	if err != nil {
		var zero T
		return zero, hit, err
	}
	value, err := snapshotPayloadAs[T](entry.Payload)
	return value, hit, err
}

func (h *DashboardHandler) getUsageTrendCached(ctx context.Context, startTime, endTime time.Time, granularity string, userID, apiKeyID, accountID, groupID int64, model string, requestType *int16, stream *bool, nativeCompactionV2 *bool, billingType *int8, upstreamModelMismatch *bool, complete ...usagestats.UsageLogFilters) ([]usagestats.TrendDataPoint, bool, error) {
	filters := usagestats.UsageLogFilters{UserID: userID, APIKeyID: apiKeyID, AccountID: accountID, GroupID: groupID, Model: model, ModelFilterSource: usagestats.ModelSourceRequested, RequestType: requestType, Stream: stream, NativeCompactionV2: nativeCompactionV2, BillingType: billingType, UpstreamModelMismatch: upstreamModelMismatch}
	if len(complete) > 0 {
		filters = complete[0]
	}
	return loadDashboardQuery(ctx, dashboardTrendCache, startTime, endTime, filters, granularity, func(work context.Context) ([]usagestats.TrendDataPoint, error) {
		return h.dashboardService.GetUsageTrendWithUsageFilters(work, startTime, endTime, granularity, filters)
	})
}

func (h *DashboardHandler) getModelStatsCached(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, accountID, groupID int64, modelSource string, requestType *int16, stream *bool, nativeCompactionV2 *bool, billingType *int8, upstreamModelMismatch *bool, complete ...usagestats.UsageLogFilters) ([]usagestats.ModelStat, bool, error) {
	filters := usagestats.UsageLogFilters{UserID: userID, APIKeyID: apiKeyID, AccountID: accountID, GroupID: groupID, ModelFilterSource: usagestats.ModelSourceRequested, RequestType: requestType, Stream: stream, NativeCompactionV2: nativeCompactionV2, BillingType: billingType, UpstreamModelMismatch: upstreamModelMismatch}
	if len(complete) > 0 {
		filters = complete[0]
	}
	return loadDashboardQuery(ctx, dashboardModelStatsCache, startTime, endTime, filters, usagestats.NormalizeModelSource(modelSource), func(work context.Context) ([]usagestats.ModelStat, error) {
		return h.dashboardService.GetModelStatsWithUsageFiltersBySource(work, startTime, endTime, filters, modelSource)
	})
}

func (h *DashboardHandler) getGroupStatsCached(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, accountID, groupID int64, requestType *int16, stream *bool, nativeCompactionV2 *bool, billingType *int8, upstreamModelMismatch *bool, complete ...usagestats.UsageLogFilters) ([]usagestats.GroupStat, bool, error) {
	filters := usagestats.UsageLogFilters{UserID: userID, APIKeyID: apiKeyID, AccountID: accountID, GroupID: groupID, ModelFilterSource: usagestats.ModelSourceRequested, RequestType: requestType, Stream: stream, NativeCompactionV2: nativeCompactionV2, BillingType: billingType, UpstreamModelMismatch: upstreamModelMismatch}
	if len(complete) > 0 {
		filters = complete[0]
	}
	return loadDashboardQuery(ctx, dashboardGroupStatsCache, startTime, endTime, filters, "", func(work context.Context) ([]usagestats.GroupStat, error) {
		return h.dashboardService.GetGroupStatsWithUsageFilters(work, startTime, endTime, filters)
	})
}

func (h *DashboardHandler) getAPIKeyUsageTrendCached(ctx context.Context, startTime, endTime time.Time, granularity string, limit int, complete ...usagestats.UsageLogFilters) ([]usagestats.APIKeyUsageTrendPoint, bool, error) {
	filters := usagestats.UsageLogFilters{}
	if len(complete) > 0 {
		filters = complete[0]
	}
	return loadDashboardQuery(ctx, dashboardAPIKeysTrendCache, startTime, endTime, filters, fmt.Sprintf("%s:%d", granularity, limit), func(work context.Context) ([]usagestats.APIKeyUsageTrendPoint, error) {
		return h.dashboardService.GetAPIKeyUsageTrendWithFilters(work, startTime, endTime, granularity, limit, filters)
	})
}

func (h *DashboardHandler) getUserUsageTrendCached(ctx context.Context, startTime, endTime time.Time, granularity string, limit int, complete ...usagestats.UsageLogFilters) ([]usagestats.UserUsageTrendPoint, bool, error) {
	filters := usagestats.UsageLogFilters{}
	if len(complete) > 0 {
		filters = complete[0]
	}
	return loadDashboardQuery(ctx, dashboardUsersTrendCache, startTime, endTime, filters, fmt.Sprintf("%s:%d", granularity, limit), func(work context.Context) ([]usagestats.UserUsageTrendPoint, error) {
		return h.dashboardService.GetUserUsageTrendWithFilters(work, startTime, endTime, granularity, limit, filters)
	})
}
