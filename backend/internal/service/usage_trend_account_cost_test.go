package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

type trendAccountCostRepo struct {
	UsageLogRepository
}

func (trendAccountCostRepo) trend() []usagestats.TrendDataPoint {
	cost := 51.2
	return []usagestats.TrendDataPoint{{Date: "2026-10-18", ActualCost: 128.45, AccountCost: &cost}}
}

func (r trendAccountCostRepo) GetUsageTrendWithUsageFilters(context.Context, time.Time, time.Time, string, usagestats.UsageLogFilters) ([]usagestats.TrendDataPoint, error) {
	return r.trend(), nil
}

func (r trendAccountCostRepo) GetUserUsageTrendByUserID(context.Context, int64, time.Time, time.Time, string) ([]usagestats.TrendDataPoint, error) {
	return r.trend(), nil
}

func TestUsageServiceTrendsStripAccountCost(t *testing.T) {
	svc := &UsageService{usageRepo: trendAccountCostRepo{}}

	trend, err := svc.GetUsageTrendWithFilters(context.Background(), time.Now(), time.Now(), "day", usagestats.UsageLogFilters{UserID: 1})
	require.NoError(t, err)
	require.Len(t, trend, 1)
	require.Nil(t, trend[0].AccountCost, "end users must not see account cost")
	require.InDelta(t, 128.45, trend[0].ActualCost, 1e-9)

	trend, err = svc.GetUserUsageTrendByUserID(context.Background(), 1, time.Now(), time.Now(), "day")
	require.NoError(t, err)
	require.Nil(t, trend[0].AccountCost)
}
