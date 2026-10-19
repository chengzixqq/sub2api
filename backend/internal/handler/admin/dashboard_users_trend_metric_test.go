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

// dashboardUserTrendMetricRepo records which users-trend query the handler
// reached: the filtered metric-aware one or the unfiltered one.
type dashboardUserTrendMetricRepo struct {
	service.UsageLogRepository
	filteredCalls    atomic.Int32
	unfilteredCalls  atomic.Int32
	metric           string
	unfilteredMetric string
	filters          usagestats.UsageLogFilters
}

func (r *dashboardUserTrendMetricRepo) GetUserUsageTrendWithFiltersByMetric(_ context.Context, _, _ time.Time, _ string, _ int, metric string, filters usagestats.UsageLogFilters) ([]usagestats.UserUsageTrendPoint, error) {
	r.filteredCalls.Add(1)
	r.metric = metric
	r.filters = filters
	return []usagestats.UserUsageTrendPoint{{Date: "2026-03-01", UserID: 3, ActualCost: 9}}, nil
}

func (r *dashboardUserTrendMetricRepo) GetUserUsageTrend(_ context.Context, _, _ time.Time, _ string, _ int, metric string) ([]usagestats.UserUsageTrendPoint, error) {
	r.unfilteredCalls.Add(1)
	r.unfilteredMetric = metric
	return []usagestats.UserUsageTrendPoint{}, nil
}

func getUsersTrend(t *testing.T, router *gin.Engine, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	path := "/admin/dashboard/users-trend?start_date=2026-03-01&end_date=2026-03-07&limit=5"
	if query != "" {
		path += "&" + query
	}
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return rec
}

func TestDashboardUserUsageTrend_FilteredHonorsMetricAndSeparatesCache(t *testing.T) {
	t.Cleanup(resetDashboardReadCachesForTest)
	resetDashboardReadCachesForTest()

	gin.SetMode(gin.TestMode)
	repo := &dashboardUserTrendMetricRepo{}
	handler := NewDashboardHandler(service.NewDashboardService(repo, nil, nil, nil), nil)
	router := gin.New()
	router.GET("/admin/dashboard/users-trend", handler.GetUserUsageTrend)

	rec := getUsersTrend(t, router, "group_id=7")
	require.Equal(t, "miss", rec.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, "tokens", repo.metric, "filtered requests default to the tokens ranking")
	require.Equal(t, int64(7), repo.filters.GroupID)
	require.Equal(t, int32(1), repo.filteredCalls.Load())

	rec = getUsersTrend(t, router, "group_id=7&metric=actual_cost")
	require.Equal(t, "miss", rec.Header().Get("X-Snapshot-Cache"), "actual_cost must not reuse the tokens entry")
	require.Equal(t, "actual_cost", repo.metric)
	require.Equal(t, int64(7), repo.filters.GroupID)
	require.Equal(t, int32(2), repo.filteredCalls.Load())

	rec = getUsersTrend(t, router, "group_id=7&metric=actual_cost")
	require.Equal(t, "hit", rec.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, int32(2), repo.filteredCalls.Load())

	rec = getUsersTrend(t, router, "group_id=7&metric=tokens")
	require.Equal(t, "hit", rec.Header().Get("X-Snapshot-Cache"), "explicit tokens shares the default entry")
	require.Equal(t, int32(2), repo.filteredCalls.Load())

	rec = getUsersTrend(t, router, "group_id=7&metric=bogus")
	require.Equal(t, "hit", rec.Header().Get("X-Snapshot-Cache"), "an unknown metric still falls back to tokens")
	require.Equal(t, int32(2), repo.filteredCalls.Load())

	// Requests without filters keep using the unfiltered query.
	rec = getUsersTrend(t, router, "metric=actual_cost")
	require.Equal(t, "miss", rec.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, int32(1), repo.unfilteredCalls.Load())
	require.Equal(t, "actual_cost", repo.unfilteredMetric)
	require.Equal(t, int32(2), repo.filteredCalls.Load())
}
