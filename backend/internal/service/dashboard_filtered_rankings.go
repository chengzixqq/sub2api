package service

import (
	"context"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

func (s *DashboardService) GetUserUsageTrendWithFilters(ctx context.Context, start, end time.Time, granularity string, limit int, filters usagestats.UsageLogFilters) ([]usagestats.UserUsageTrendPoint, error) {
	if repo, ok := s.usageRepo.(interface {
		GetUserUsageTrendWithFilters(context.Context, time.Time, time.Time, string, int, usagestats.UsageLogFilters) ([]usagestats.UserUsageTrendPoint, error)
	}); ok {
		return repo.GetUserUsageTrendWithFilters(ctx, start, end, granularity, limit, filters)
	}
	return s.GetUserUsageTrend(ctx, start, end, granularity, limit, "tokens")
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

// GetUserSpendingRankingSorted ranks users by sortBy (actual_cost, requests or
// tokens). Every real usage repository implements the sorted query; a
// repository without it (test doubles) only serves the default actual_cost
// order through GetUserSpendingRankingWithFilters and errors on other orders.
func (s *DashboardService) GetUserSpendingRankingSorted(ctx context.Context, start, end time.Time, limit int, sortBy string, filters usagestats.UsageLogFilters) (*usagestats.UserSpendingRankingResponse, error) {
	if repo, ok := s.usageRepo.(interface {
		GetUserSpendingRankingSorted(context.Context, time.Time, time.Time, int, string, usagestats.UsageLogFilters) (*usagestats.UserSpendingRankingResponse, error)
	}); ok {
		return repo.GetUserSpendingRankingSorted(ctx, start, end, limit, sortBy, filters)
	}
	if sortBy == "" || sortBy == usagestats.UserRankingSortActualCost {
		return s.GetUserSpendingRankingWithFilters(ctx, start, end, limit, filters)
	}
	return nil, errors.New("sorted user spending ranking is not supported by the usage repository")
}

// GetAccountSpendingRanking ranks accounts by actual charged cost. The usage
// repository must support it; there is no slower fallback.
func (s *DashboardService) GetAccountSpendingRanking(ctx context.Context, params usagestats.AccountSpendingRankingParams) (*usagestats.AccountSpendingRankingResponse, error) {
	repo, ok := s.usageRepo.(interface {
		GetAccountSpendingRanking(context.Context, usagestats.AccountSpendingRankingParams) (*usagestats.AccountSpendingRankingResponse, error)
	})
	if !ok {
		return nil, errors.New("account spending ranking is not supported by the usage repository")
	}
	return repo.GetAccountSpendingRanking(ctx, params)
}
