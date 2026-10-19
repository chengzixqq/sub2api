package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

type yesterdayTrendRepo struct {
	UsageLogRepository
	calls       int
	start, end  time.Time
	granularity string
	zone        string
	points      []usagestats.TrendDataPoint
}

func (r *yesterdayTrendRepo) GetUsageTrendWithUsageFilters(ctx context.Context, start, end time.Time, granularity string, _ usagestats.UsageLogFilters) ([]usagestats.TrendDataPoint, error) {
	r.calls++
	r.start, r.end, r.granularity = start, end, granularity
	r.zone = usagequery.OptionsFrom(ctx).Timezone
	return r.points, nil
}

func TestDashboardYesterdaySameTimeCutoff(t *testing.T) {
	loc := timezone.Location()
	now := time.Date(2026, 10, 18, 9, 45, 0, 0, loc)
	todayStart := time.Date(2026, 10, 18, 0, 0, 0, 0, loc)
	yesterdayStart := todayStart.AddDate(0, 0, -1)
	cost := 2.5

	t.Run("aggregation watermark is the cutoff", func(t *testing.T) {
		repo := &yesterdayTrendRepo{points: []usagestats.TrendDataPoint{
			{Requests: 10, ActualCost: 1.25, AccountCost: &cost},
			{Requests: 5, ActualCost: 0.75},
		}}
		svc := &DashboardService{usageRepo: repo, aggEnabled: true}
		ctx := usagequery.WithOptions(context.Background(), usagequery.Options{Identity: "admin:1", Timezone: "America/New_York"})
		stats := &usagestats.DashboardStats{StatsUpdatedAt: time.Date(2026, 10, 18, 9, 40, 0, 0, loc).UTC().Format(time.RFC3339)}

		got, err := svc.GetYesterdaySameTime(ctx, stats, now)
		require.NoError(t, err)
		require.Equal(t, 1, repo.calls)
		require.True(t, repo.start.Equal(yesterdayStart))
		require.True(t, repo.end.Equal(time.Date(2026, 10, 17, 9, 40, 0, 0, loc)))
		require.Equal(t, "day", repo.granularity)
		require.Equal(t, "", repo.zone, "the server zone keeps the hourly pre-aggregation path")
		require.Equal(t, int64(15), got.Requests)
		require.Equal(t, 2.0, got.ActualCost)
		require.Equal(t, 2.5, got.AccountCost)
		require.True(t, got.Until.Equal(time.Date(2026, 10, 17, 9, 40, 0, 0, loc)))
	})

	t.Run("live statistics use now", func(t *testing.T) {
		repo := &yesterdayTrendRepo{}
		svc := &DashboardService{usageRepo: repo, aggEnabled: false}
		stats := &usagestats.DashboardStats{StatsUpdatedAt: time.Date(2026, 10, 18, 9, 0, 0, 0, loc).UTC().Format(time.RFC3339)}
		got, err := svc.GetYesterdaySameTime(context.Background(), stats, now)
		require.NoError(t, err)
		require.True(t, repo.end.Equal(now.AddDate(0, 0, -1)))
		require.True(t, got.Until.Equal(now.AddDate(0, 0, -1)))
	})

	t.Run("a watermark stuck before today clamps to midnight", func(t *testing.T) {
		repo := &yesterdayTrendRepo{}
		svc := &DashboardService{usageRepo: repo, aggEnabled: true}
		stats := &usagestats.DashboardStats{StatsUpdatedAt: time.Date(2026, 10, 17, 22, 0, 0, 0, loc).UTC().Format(time.RFC3339)}
		got, err := svc.GetYesterdaySameTime(context.Background(), stats, now)
		require.NoError(t, err)
		require.Zero(t, repo.calls)
		require.Zero(t, got.Requests)
		require.Zero(t, got.ActualCost)
		require.True(t, got.Until.Equal(yesterdayStart))
	})

	t.Run("a watermark ahead of now clamps to now", func(t *testing.T) {
		repo := &yesterdayTrendRepo{}
		svc := &DashboardService{usageRepo: repo, aggEnabled: true}
		stats := &usagestats.DashboardStats{StatsUpdatedAt: now.Add(time.Hour).UTC().Format(time.RFC3339)}
		_, err := svc.GetYesterdaySameTime(context.Background(), stats, now)
		require.NoError(t, err)
		require.True(t, repo.end.Equal(now.AddDate(0, 0, -1)))
	})
}
