package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAdminUsageModelSource(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	for _, path := range []string{"/admin/usage", "/admin/usage/stats"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path+"?model=claude-opus-4-20250514&model_source=upstream", nil))
		require.Equal(t, http.StatusOK, rec.Code, path)
		rec = httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path+"?model_source=x", nil))
		require.Equal(t, http.StatusBadRequest, rec.Code, path)
		require.Contains(t, rec.Body.String(), "Invalid model_source, use requested/upstream/mapping")
	}
	require.Equal(t, usagestats.ModelSourceUpstream, repo.listFilters.ModelFilterSource)
	require.Equal(t, usagestats.ModelSourceUpstream, repo.statsFilters.ModelFilterSource)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/usage?model=m", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, usagestats.ModelSourceRequested, repo.listFilters.ModelFilterSource, "default stays requested")
}

type dashboardModelSourceRepo struct {
	dashboardUsageRepoCapture
	trendSource   string
	groupSource   string
	rankingSource string
	usersSource   string
}

func (s *dashboardModelSourceRepo) GetUsageTrendWithUsageFilters(_ context.Context, _, _ time.Time, _ string, f usagestats.UsageLogFilters) ([]usagestats.TrendDataPoint, error) {
	s.trendSource = f.ModelFilterSource
	return []usagestats.TrendDataPoint{}, nil
}

func (s *dashboardModelSourceRepo) GetGroupStatsWithUsageFilters(_ context.Context, _, _ time.Time, f usagestats.UsageLogFilters) ([]usagestats.GroupStat, error) {
	s.groupSource = f.ModelFilterSource
	return []usagestats.GroupStat{}, nil
}

func (s *dashboardModelSourceRepo) GetUserSpendingRankingWithFilters(_ context.Context, _, _ time.Time, _ int, f usagestats.UsageLogFilters) (*usagestats.UserSpendingRankingResponse, error) {
	s.rankingSource = f.ModelFilterSource
	return &usagestats.UserSpendingRankingResponse{}, nil
}

func (s *dashboardModelSourceRepo) GetUserUsageTrendWithFilters(_ context.Context, _, _ time.Time, _ string, _ int, f usagestats.UsageLogFilters) ([]usagestats.UserUsageTrendPoint, error) {
	s.usersSource = f.ModelFilterSource
	return []usagestats.UserUsageTrendPoint{}, nil
}

func TestDashboardModelSource(t *testing.T) {
	dashboardTrendCache = newSnapshotCache(time.Minute)
	dashboardGroupStatsCache = newSnapshotCache(time.Minute)
	dashboardUsersRankingCache = newSnapshotCache(time.Minute)
	repo := &dashboardModelSourceRepo{}
	handler := NewDashboardHandler(service.NewDashboardService(repo, nil, nil, nil), nil)
	router := newDashboardRequestTypeTestRouter(&repo.dashboardUsageRepoCapture)
	router.GET("/ms/trend", handler.GetUsageTrend)
	router.GET("/ms/groups", handler.GetGroupStats)
	router.GET("/ms/users-ranking", handler.GetUserSpendingRanking)
	router.GET("/ms/users-trend", handler.GetUserUsageTrend)

	for _, path := range []string{"/ms/trend", "/ms/groups", "/ms/users-ranking", "/ms/users-trend"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path+"?model=a+->+b&model_source=mapping", nil))
		require.Equal(t, http.StatusOK, rec.Code, path+": "+rec.Body.String())
		rec = httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path+"?model_source=bogus", nil))
		require.Equal(t, http.StatusBadRequest, rec.Code, path)
	}
	require.Equal(t, usagestats.ModelSourceMapping, repo.trendSource)
	require.Equal(t, usagestats.ModelSourceMapping, repo.groupSource)
	require.Equal(t, usagestats.ModelSourceMapping, repo.rankingSource)
	require.Equal(t, usagestats.ModelSourceMapping, repo.usersSource)
}
