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

// dashboardRequestTypeRepo records what dashboard/request-types asked the
// usage repository for.
type dashboardRequestTypeRepo struct {
	service.UsageLogRepository
	calls   atomic.Int32
	start   time.Time
	end     time.Time
	filters usagestats.UsageLogFilters
	result  []usagestats.RequestTypeStat
}

func (r *dashboardRequestTypeRepo) GetRequestTypeStatsWithUsageFilters(_ context.Context, start, end time.Time, filters usagestats.UsageLogFilters) ([]usagestats.RequestTypeStat, error) {
	r.calls.Add(1)
	r.start, r.end, r.filters = start, end, filters
	return r.result, nil
}

func newRequestTypeRouter(repo *dashboardRequestTypeRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	handler := NewDashboardHandler(service.NewDashboardService(repo, nil, nil, nil), nil)
	router := gin.New()
	router.GET("/admin/dashboard/request-types", handler.GetRequestTypeStats)
	return router
}

func getRequestTypes(router *gin.Engine, query string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/dashboard/request-types?"+query, nil))
	return rec
}

func TestDashboardRequestTypes_PassesFiltersAndCaches(t *testing.T) {
	t.Cleanup(resetDashboardReadCachesForTest)
	resetDashboardReadCachesForTest()
	repo := &dashboardRequestTypeRepo{result: []usagestats.RequestTypeStat{
		{RequestType: "stream", Requests: 3, TotalTokens: 30, Cost: 0.3, ActualCost: 0.2, AccountCost: 0.1},
	}}
	router := newRequestTypeRouter(repo)
	query := "start_date=2026-10-10&end_date=2026-10-12&timezone=UTC&group_id=3&account_id=5&model=m&model_source=upstream"

	rec := getRequestTypes(router, query)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "miss", rec.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), repo.start.UTC())
	require.Equal(t, time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC), repo.end.UTC())
	require.Equal(t, int64(3), repo.filters.GroupID)
	require.Equal(t, int64(5), repo.filters.AccountID)
	require.Equal(t, "m", repo.filters.Model)
	require.Equal(t, usagestats.ModelSourceUpstream, repo.filters.ModelFilterSource)
	require.Nil(t, repo.filters.RequestType)
	require.Nil(t, repo.filters.Stream)
	body := rec.Body.String()
	require.Contains(t, body, `"request_types":[{"request_type":"stream","requests":3,"total_tokens":30,"cost":0.3,"actual_cost":0.2,"account_cost":0.1}]`)
	require.Contains(t, body, `"start_date":"2026-10-10"`)
	require.Contains(t, body, `"end_date":"2026-10-12"`)
	require.Contains(t, body, `"query":`)

	rec = getRequestTypes(router, query)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "hit", rec.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, int32(1), repo.calls.Load())

	rec = getRequestTypes(router, "start_date=2026-10-10&end_date=2026-10-12&timezone=UTC&group_id=3&account_id=5&model=m&model_source=requested")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "miss", rec.Header().Get("X-Snapshot-Cache"), "model_source is part of the cache key")
	require.Equal(t, int32(2), repo.calls.Load())
}

// The response is already split by request type, so filtering by one makes no
// sense; the caller gets a 400 instead of a silently ignored filter.
func TestDashboardRequestTypes_RejectsUnsupportedAndInvalidParams(t *testing.T) {
	t.Cleanup(resetDashboardReadCachesForTest)
	resetDashboardReadCachesForTest()
	repo := &dashboardRequestTypeRepo{result: []usagestats.RequestTypeStat{}}
	router := newRequestTypeRouter(repo)

	for _, query := range []string{
		"request_type=sync",
		"stream=true",
		"stream=false",
		"request_type=bogus",
		"group_id=abc",
		"model_source=bad",
		"start_date=bad",
		"billing_type=x",
	} {
		rec := getRequestTypes(router, query)
		require.Equal(t, http.StatusBadRequest, rec.Code, "%s: %s", query, rec.Body.String())
	}
	require.Zero(t, repo.calls.Load())

	rec := getRequestTypes(router, "request_type=%20&stream=")
	require.Equal(t, http.StatusOK, rec.Code, "blank request_type / stream mean not set: %s", rec.Body.String())
}

func TestDashboardRequestTypes_EmptyResultIsArray(t *testing.T) {
	t.Cleanup(resetDashboardReadCachesForTest)
	resetDashboardReadCachesForTest()
	repo := &dashboardRequestTypeRepo{result: []usagestats.RequestTypeStat{}}
	router := newRequestTypeRouter(repo)

	rec := getRequestTypes(router, "start_date=2026-10-10&end_date=2026-10-12")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"request_types":[]`)
}

// A usage repository without the grouped query (only test doubles) is an
// error, not an empty chart.
func TestDashboardRequestTypes_UnsupportedRepositoryErrors(t *testing.T) {
	t.Cleanup(resetDashboardReadCachesForTest)
	resetDashboardReadCachesForTest()
	gin.SetMode(gin.TestMode)
	handler := NewDashboardHandler(service.NewDashboardService(&dashboardUserTrendMetricRepo{}, nil, nil, nil), nil)
	router := gin.New()
	router.GET("/admin/dashboard/request-types", handler.GetRequestTypeStats)

	rec := getRequestTypes(router, "start_date=2026-10-10&end_date=2026-10-12")
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
}
