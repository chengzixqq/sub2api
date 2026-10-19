package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// Overview and throughput are polled on rolling windows; flooring the end to
// opsDashboardBucket lets concurrent pollers share one short-lived entry
// (same bucket as dashboard/rates).
const opsDashboardBucket = dashboardRatesBucket

var (
	opsDashboardOverviewCache = newSnapshotCache(opsDashboardBucket)
	opsThroughputTrendCache   = newSnapshotCache(opsDashboardBucket)
)

// GetDashboardOverview returns vNext ops dashboard overview (raw path).
// GET /api/v1/admin/ops/dashboard/overview
func (h *OpsHandler) GetDashboardOverview(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	filter, err := parseOpsDashboardFilterBucketed(c, opsDashboardBucket)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	ctx := c.Request.Context()
	key := scopedUsageCacheKey(ctx, mustMarshalDashboardCacheKey(filter))
	entry, hit, err := opsDashboardOverviewCache.GetOrLoadContext(ctx, key, func(work context.Context) (any, error) {
		query := *filter // the service resolves QueryMode in place
		return h.opsService.GetDashboardOverview(work, &query)
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	data, err := snapshotPayloadAs[*service.OpsDashboardOverview](entry.Payload)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))
	response.Success(c, data)
}

// GetDashboardThroughputTrend returns throughput time series (raw path).
// GET /api/v1/admin/ops/dashboard/throughput-trend
func (h *OpsHandler) GetDashboardThroughputTrend(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	filter, err := parseOpsDashboardFilterBucketed(c, opsDashboardBucket)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	bucketSeconds := pickThroughputBucketSeconds(filter.EndTime.Sub(filter.StartTime))
	ctx := c.Request.Context()
	key := scopedUsageCacheKey(ctx, mustMarshalDashboardCacheKey(struct {
		Filter        *service.OpsDashboardFilter
		BucketSeconds int
	}{filter, bucketSeconds}))
	entry, hit, err := opsThroughputTrendCache.GetOrLoadContext(ctx, key, func(work context.Context) (any, error) {
		query := *filter
		return h.opsService.GetThroughputTrend(work, &query, bucketSeconds)
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	data, err := snapshotPayloadAs[*service.OpsThroughputTrendResponse](entry.Payload)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))
	response.Success(c, data)
}

// GetDashboardLatencyHistogram returns the latency distribution histogram (success requests).
// GET /api/v1/admin/ops/dashboard/latency-histogram
func (h *OpsHandler) GetDashboardLatencyHistogram(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	filter, err := parseOpsDashboardFilter(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	data, err := h.opsService.GetLatencyHistogram(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, data)
}

// GetDashboardErrorTrend returns error counts time series (raw path).
// GET /api/v1/admin/ops/dashboard/error-trend
func (h *OpsHandler) GetDashboardErrorTrend(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	filter, err := parseOpsDashboardFilter(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	bucketSeconds := pickThroughputBucketSeconds(filter.EndTime.Sub(filter.StartTime))
	data, err := h.opsService.GetErrorTrend(c.Request.Context(), filter, bucketSeconds)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, data)
}

// GetDashboardErrorDistribution returns error distribution by status code (raw path).
// GET /api/v1/admin/ops/dashboard/error-distribution
func (h *OpsHandler) GetDashboardErrorDistribution(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	filter, err := parseOpsDashboardFilter(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	data, err := h.opsService.GetErrorDistribution(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, data)
}

// GetDashboardOpenAITokenStats returns token efficiency stats for all platforms, grouped by model.
// The endpoint name is retained for compatibility; platform and group_id optionally narrow the scope.
// GET /api/v1/admin/ops/dashboard/openai-token-stats
func (h *OpsHandler) GetDashboardOpenAITokenStats(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	filter, err := parseOpsOpenAITokenStatsFilter(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	data, err := h.opsService.GetOpenAITokenStats(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, data)
}

// Ops filter errors are returned to clients verbatim, so they keep the
// existing capitalized messages.
var (
	errInvalidOpsModelSource = errors.New("Invalid model_source, use requested/upstream") //nolint:staticcheck // ST1005: client-facing message
	errInvalidOpsGroupID     = errors.New("Invalid group_id")                             //nolint:staticcheck // ST1005: client-facing message
)

// parseOpsDashboardFilter reads the filter shared by the ops dashboard
// endpoints: time range, platform, group_id, mode and model / model_source.
func parseOpsDashboardFilter(c *gin.Context) (*service.OpsDashboardFilter, error) {
	return parseOpsDashboardFilterAt(c, opsNow())
}

// parseOpsDashboardFilterBucketed floors the end of a relative window to
// bucket so polling clients share one cache entry; explicit ranges are kept.
func parseOpsDashboardFilterBucketed(c *gin.Context, bucket time.Duration) (*service.OpsDashboardFilter, error) {
	return parseOpsDashboardFilterAt(c, opsNow().Truncate(bucket))
}

func parseOpsDashboardFilterAt(c *gin.Context, end time.Time) (*service.OpsDashboardFilter, error) {
	startTime, endTime, err := parseOpsTimeRangeAt(c, "1h", end)
	if err != nil {
		return nil, err
	}

	filter := &service.OpsDashboardFilter{
		StartTime: startTime,
		EndTime:   endTime,
		Platform:  strings.TrimSpace(c.Query("platform")),
		QueryMode: parseOpsQueryMode(c),
	}
	if v := strings.TrimSpace(c.Query("group_id")); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			return nil, errInvalidOpsGroupID
		}
		filter.GroupID = &id
	}

	filter.Model, filter.ModelSource, err = parseOpsModelFilter(c)
	if err != nil {
		return nil, err
	}
	return filter, nil
}

// parseOpsModelFilter reads model (exact match) and model_source (requested |
// upstream, default requested). model_source is validated even without model;
// without model both results are empty so the query stays unfiltered.
func parseOpsModelFilter(c *gin.Context) (model string, source string, err error) {
	source = strings.TrimSpace(c.Query("model_source"))
	switch source {
	case "":
		source = usagestats.ModelSourceRequested
	case usagestats.ModelSourceRequested, usagestats.ModelSourceUpstream:
	default:
		return "", "", errInvalidOpsModelSource
	}
	model = strings.TrimSpace(c.Query("model"))
	if model == "" {
		return "", "", nil
	}
	return model, source, nil
}

func parseOpsOpenAITokenStatsFilter(c *gin.Context) (*service.OpsOpenAITokenStatsFilter, error) {
	if c == nil {
		return nil, fmt.Errorf("invalid request")
	}

	timeRange, start, end, err := resolveOpsOpenAITokenStatsWindow(c)
	if err != nil {
		return nil, err
	}

	filter := &service.OpsOpenAITokenStatsFilter{
		TimeRange: timeRange,
		StartTime: start,
		EndTime:   end,
		Platform:  strings.TrimSpace(c.Query("platform")),
	}

	if v := strings.TrimSpace(c.Query("group_id")); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid group_id")
		}
		filter.GroupID = &id
	}

	model, modelSource, err := parseOpsModelFilter(c)
	if err != nil {
		return nil, err
	}
	filter.Model, filter.ModelSource = model, modelSource

	topNRaw := strings.TrimSpace(c.Query("top_n"))
	pageRaw := strings.TrimSpace(c.Query("page"))
	pageSizeRaw := strings.TrimSpace(c.Query("page_size"))
	if topNRaw != "" && (pageRaw != "" || pageSizeRaw != "") {
		return nil, fmt.Errorf("invalid query: top_n cannot be used with page/page_size")
	}

	if topNRaw != "" {
		topN, err := strconv.Atoi(topNRaw)
		if err != nil || topN < 1 || topN > 100 {
			return nil, fmt.Errorf("invalid top_n")
		}
		filter.TopN = topN
		return filter, nil
	}

	filter.Page = 1
	filter.PageSize = 20
	if pageRaw != "" {
		page, err := strconv.Atoi(pageRaw)
		if err != nil || page < 1 {
			return nil, fmt.Errorf("invalid page")
		}
		filter.Page = page
	}
	if pageSizeRaw != "" {
		pageSize, err := strconv.Atoi(pageSizeRaw)
		if err != nil || pageSize < 1 || pageSize > 100 {
			return nil, fmt.Errorf("invalid page_size")
		}
		filter.PageSize = pageSize
	}
	return filter, nil
}

func parseOpsOpenAITokenStatsDuration(v string) (time.Duration, bool) {
	switch strings.TrimSpace(v) {
	case "30m":
		return 30 * time.Minute, true
	case "1h":
		return time.Hour, true
	case "1d":
		return 24 * time.Hour, true
	case "15d":
		return 15 * 24 * time.Hour, true
	case "30d":
		return 30 * 24 * time.Hour, true
	default:
		return 0, false
	}
}

func pickThroughputBucketSeconds(window time.Duration) int {
	// Keep buckets predictable and avoid huge responses.
	switch {
	case window <= 2*time.Hour:
		return 60
	case window <= 24*time.Hour:
		return 300
	default:
		return 3600
	}
}

func parseOpsQueryMode(c *gin.Context) service.OpsQueryMode {
	if c == nil {
		return ""
	}
	raw := strings.TrimSpace(c.Query("mode"))
	if raw == "" {
		// Empty means "use server default" (DB setting ops_query_mode_default).
		return ""
	}
	return service.ParseOpsQueryMode(raw)
}
