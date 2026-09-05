package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type dashboardUsageRepoCacheProbe struct {
	service.UsageLogRepository
	trendCalls      atomic.Int32
	usersTrendCalls atomic.Int32
}

func (r *dashboardUsageRepoCacheProbe) GetUsageTrendWithFilters(
	ctx context.Context,
	startTime, endTime time.Time,
	granularity string,
	userID, apiKeyID, accountID, groupID int64,
	model string,
	requestType *int16,
	stream *bool,
	billingType *int8,
) ([]usagestats.TrendDataPoint, error) {
	return r.trendResult(ctx, nil)
}

func (r *dashboardUsageRepoCacheProbe) GetUsageTrendWithUsageFilters(
	ctx context.Context,
	startTime, endTime time.Time,
	granularity string,
	filters usagestats.UsageLogFilters,
) ([]usagestats.TrendDataPoint, error) {
	return r.trendResult(ctx, filters.UpstreamModelMismatch)
}

func (r *dashboardUsageRepoCacheProbe) trendResult(ctx context.Context, upstreamModelMismatch *bool) ([]usagestats.TrendDataPoint, error) {
	r.trendCalls.Add(1)
	requests := int64(1)
	if accountIDs, restricted := service.UsageAccountScopeFrom(ctx); restricted && len(accountIDs) > 0 {
		requests = accountIDs[0]
	}
	totalTokens := int64(2)
	if upstreamModelMismatch != nil {
		if *upstreamModelMismatch {
			totalTokens = 20
		} else {
			totalTokens = 10
		}
	}
	return []usagestats.TrendDataPoint{{
		Date:        "2026-03-11",
		Requests:    requests,
		TotalTokens: totalTokens,
		Cost:        3,
		ActualCost:  4,
	}}, nil
}

func (r *dashboardUsageRepoCacheProbe) GetUserUsageTrend(
	ctx context.Context,
	startTime, endTime time.Time,
	granularity string,
	limit int,
) ([]usagestats.UserUsageTrendPoint, error) {
	r.usersTrendCalls.Add(1)
	return []usagestats.UserUsageTrendPoint{{
		Date:       "2026-03-11",
		UserID:     1,
		Email:      "cache@test.dev",
		Requests:   2,
		Tokens:     20,
		Cost:       2,
		ActualCost: 1,
	}}, nil
}

func resetDashboardReadCachesForTest() {
	dashboardTrendCache = newSnapshotCache(30 * time.Second)
	dashboardUsersTrendCache = newSnapshotCache(30 * time.Second)
	dashboardAPIKeysTrendCache = newSnapshotCache(30 * time.Second)
	dashboardModelStatsCache = newSnapshotCache(30 * time.Second)
	dashboardGroupStatsCache = newSnapshotCache(30 * time.Second)
	dashboardSnapshotV2Cache = newSnapshotCache(30 * time.Second)
}

func TestDashboardHandler_GetUsageTrend_UsesCache(t *testing.T) {
	t.Cleanup(resetDashboardReadCachesForTest)
	resetDashboardReadCachesForTest()

	gin.SetMode(gin.TestMode)
	repo := &dashboardUsageRepoCacheProbe{}
	dashboardSvc := service.NewDashboardService(repo, nil, nil, nil)
	handler := NewDashboardHandler(dashboardSvc, nil)
	router := gin.New()
	router.GET("/admin/dashboard/trend", handler.GetUsageTrend)

	req1 := httptest.NewRequest(http.MethodGet, "/admin/dashboard/trend?start_date=2026-03-01&end_date=2026-03-07&granularity=day", nil)
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)
	require.Equal(t, http.StatusOK, rec1.Code)
	require.Equal(t, "miss", rec1.Header().Get("X-Snapshot-Cache"))

	req2 := httptest.NewRequest(http.MethodGet, "/admin/dashboard/trend?start_date=2026-03-01&end_date=2026-03-07&granularity=day", nil)
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusOK, rec2.Code)
	require.Equal(t, "hit", rec2.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, int32(1), repo.trendCalls.Load())
}

func TestDashboardHandler_GetUserUsageTrend_UsesCache(t *testing.T) {
	t.Cleanup(resetDashboardReadCachesForTest)
	resetDashboardReadCachesForTest()

	gin.SetMode(gin.TestMode)
	repo := &dashboardUsageRepoCacheProbe{}
	dashboardSvc := service.NewDashboardService(repo, nil, nil, nil)
	handler := NewDashboardHandler(dashboardSvc, nil)
	router := gin.New()
	router.GET("/admin/dashboard/users-trend", handler.GetUserUsageTrend)

	req1 := httptest.NewRequest(http.MethodGet, "/admin/dashboard/users-trend?start_date=2026-03-01&end_date=2026-03-07&granularity=day&limit=8", nil)
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)
	require.Equal(t, http.StatusOK, rec1.Code)
	require.Equal(t, "miss", rec1.Header().Get("X-Snapshot-Cache"))

	req2 := httptest.NewRequest(http.MethodGet, "/admin/dashboard/users-trend?start_date=2026-03-01&end_date=2026-03-07&granularity=day&limit=8", nil)
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusOK, rec2.Code)
	require.Equal(t, "hit", rec2.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, int32(1), repo.usersTrendCalls.Load())
}

func TestDashboardUsageTrendCache_RestrictedScopesBypassSharedEntries(t *testing.T) {
	t.Cleanup(resetDashboardReadCachesForTest)
	resetDashboardReadCachesForTest()

	repo := &dashboardUsageRepoCacheProbe{}
	dashboardSvc := service.NewDashboardService(repo, nil, nil, nil)
	handler := NewDashboardHandler(dashboardSvc, nil)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	admin, hit, err := handler.getUsageTrendCached(context.Background(), start, end, "hour", 0, 0, 0, 0, "", nil, nil, nil, nil, nil)
	require.NoError(t, err)
	require.False(t, hit)
	require.Equal(t, int64(1), admin[0].Requests)

	adminCached, hit, err := handler.getUsageTrendCached(context.Background(), start, end, "hour", 0, 0, 0, 0, "", nil, nil, nil, nil, nil)
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, int64(1), adminCached[0].Requests)

	mismatch := true
	vendorA, hit, err := handler.getUsageTrendCached(service.WithUsageAccountScope(context.Background(), []int64{101}), start, end, "hour", 0, 0, 0, 0, "", nil, nil, nil, nil, &mismatch)
	require.NoError(t, err)
	require.False(t, hit)
	require.Equal(t, int64(101), vendorA[0].Requests)
	require.Equal(t, int64(20), vendorA[0].TotalTokens)

	mismatch = false
	vendorAWithoutMismatch, hit, err := handler.getUsageTrendCached(service.WithUsageAccountScope(context.Background(), []int64{101}), start, end, "hour", 0, 0, 0, 0, "", nil, nil, nil, nil, &mismatch)
	require.NoError(t, err)
	require.False(t, hit)
	require.Equal(t, int64(101), vendorAWithoutMismatch[0].Requests)
	require.Equal(t, int64(10), vendorAWithoutMismatch[0].TotalTokens)

	vendorB, hit, err := handler.getUsageTrendCached(service.WithUsageAccountScope(context.Background(), []int64{202}), start, end, "hour", 0, 0, 0, 0, "", nil, nil, nil, nil, nil)
	require.NoError(t, err)
	require.False(t, hit)
	require.Equal(t, int64(202), vendorB[0].Requests)
	require.Equal(t, int32(4), repo.trendCalls.Load())

	adminAgain, hit, err := handler.getUsageTrendCached(context.Background(), start, end, "hour", 0, 0, 0, 0, "", nil, nil, nil, nil, nil)
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, int64(1), adminAgain[0].Requests)
	require.Equal(t, int32(4), repo.trendCalls.Load())
}
