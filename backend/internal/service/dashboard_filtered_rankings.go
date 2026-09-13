package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

func (s *DashboardService) GetUserUsageTrendWithFilters(ctx context.Context, start, end time.Time, granularity string, limit int, filters usagestats.UsageLogFilters) ([]usagestats.UserUsageTrendPoint, error) {
	if repo, ok := s.usageRepo.(interface {
		GetUserUsageTrendWithFilters(context.Context, time.Time, time.Time, string, int, usagestats.UsageLogFilters) ([]usagestats.UserUsageTrendPoint, error)
	}); ok {
		return repo.GetUserUsageTrendWithFilters(ctx, start, end, granularity, limit, filters)
	}
	return s.GetUserUsageTrend(ctx, start, end, granularity, limit)
}
func (s *DashboardService) GetAPIKeyUsageTrendWithFilters(ctx context.Context, start, end time.Time, granularity string, limit int, filters usagestats.UsageLogFilters) ([]usagestats.APIKeyUsageTrendPoint, error) {
	if repo, ok := s.usageRepo.(interface {
		GetAPIKeyUsageTrendWithFilters(context.Context, time.Time, time.Time, string, int, usagestats.UsageLogFilters) ([]usagestats.APIKeyUsageTrendPoint, error)
	}); ok {
		return repo.GetAPIKeyUsageTrendWithFilters(ctx, start, end, granularity, limit, filters)
	}
	return s.GetAPIKeyUsageTrend(ctx, start, end, granularity, limit)
}
func (s *DashboardService) GetUserSpendingRankingWithFilters(ctx context.Context, start, end time.Time, limit int, filters usagestats.UsageLogFilters) (*usagestats.UserSpendingRankingResponse, error) {
	if repo, ok := s.usageRepo.(interface {
		GetUserSpendingRankingWithFilters(context.Context, time.Time, time.Time, int, usagestats.UsageLogFilters) (*usagestats.UserSpendingRankingResponse, error)
	}); ok {
		return repo.GetUserSpendingRankingWithFilters(ctx, start, end, limit, filters)
	}
	return s.GetUserSpendingRanking(ctx, start, end, limit)
}
