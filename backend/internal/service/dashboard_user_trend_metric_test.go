package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

// userTrendFiltersOnlyRepo predates metric-aware filtered user trends: it only
// knows the tokens ranking.
type userTrendFiltersOnlyRepo struct {
	UsageLogRepository
	t       *testing.T
	calls   int
	filters usagestats.UsageLogFilters
}

func (r *userTrendFiltersOnlyRepo) GetUserUsageTrendWithFilters(_ context.Context, _, _ time.Time, _ string, _ int, filters usagestats.UsageLogFilters) ([]usagestats.UserUsageTrendPoint, error) {
	r.calls++
	r.filters = filters
	return []usagestats.UserUsageTrendPoint{{UserID: 1}}, nil
}

func (r *userTrendFiltersOnlyRepo) GetUserUsageTrend(context.Context, time.Time, time.Time, string, int, string) ([]usagestats.UserUsageTrendPoint, error) {
	r.t.Fatal("a filtered user trend must never fall back to the unfiltered query")
	return nil, nil
}

type userTrendByMetricRepo struct {
	UsageLogRepository
	metric      string
	granularity string
	limit       int
	filters     usagestats.UsageLogFilters
}

func (r *userTrendByMetricRepo) GetUserUsageTrendWithFiltersByMetric(_ context.Context, _, _ time.Time, granularity string, limit int, metric string, filters usagestats.UsageLogFilters) ([]usagestats.UserUsageTrendPoint, error) {
	r.granularity, r.limit, r.metric, r.filters = granularity, limit, metric, filters
	return []usagestats.UserUsageTrendPoint{{UserID: 2}}, nil
}

func TestDashboardService_GetUserUsageTrendWithFiltersByMetric(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	filters := usagestats.UsageLogFilters{GroupID: 9, Model: "m"}

	t.Run("passes metric and filters through", func(t *testing.T) {
		repo := &userTrendByMetricRepo{}
		svc := NewDashboardService(repo, nil, nil, nil)
		got, err := svc.GetUserUsageTrendWithFiltersByMetric(ctx, start, end, "hour", 12, "actual_cost", filters)
		require.NoError(t, err)
		require.Equal(t, []usagestats.UserUsageTrendPoint{{UserID: 2}}, got)
		require.Equal(t, "actual_cost", repo.metric)
		require.Equal(t, "hour", repo.granularity)
		require.Equal(t, 12, repo.limit)
		require.Equal(t, filters, repo.filters)
	})

	t.Run("legacy repository keeps filters for the tokens ranking", func(t *testing.T) {
		repo := &userTrendFiltersOnlyRepo{t: t}
		svc := NewDashboardService(repo, nil, nil, nil)
		for _, metric := range []string{"", "tokens"} {
			got, err := svc.GetUserUsageTrendWithFiltersByMetric(ctx, start, end, "day", 12, metric, filters)
			require.NoError(t, err, metric)
			require.Equal(t, []usagestats.UserUsageTrendPoint{{UserID: 1}}, got)
		}
		require.Equal(t, 2, repo.calls)
		require.Equal(t, filters, repo.filters)

		_, err := svc.GetUserUsageTrendWithFiltersByMetric(ctx, start, end, "day", 12, "actual_cost", filters)
		require.Error(t, err, "a repository without the metric-aware query cannot rank by actual_cost")
		require.Equal(t, 2, repo.calls)
	})
}
