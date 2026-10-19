package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

// GetRequestTypeStatsWithUsageFilters totals usage per effective request type
// (dashboard/request-types). The usage repository must support it; there is
// no slower fallback.
func (s *DashboardService) GetRequestTypeStatsWithUsageFilters(ctx context.Context, start, end time.Time, filters usagestats.UsageLogFilters) ([]usagestats.RequestTypeStat, error) {
	repo, ok := s.usageRepo.(interface {
		GetRequestTypeStatsWithUsageFilters(context.Context, time.Time, time.Time, usagestats.UsageLogFilters) ([]usagestats.RequestTypeStat, error)
	})
	if !ok {
		return nil, errors.New("request type stats are not supported by the usage repository")
	}
	stats, err := repo.GetRequestTypeStatsWithUsageFilters(ctx, start, end, filters)
	if err != nil {
		return nil, fmt.Errorf("get request type stats with usage filters: %w", err)
	}
	return stats, nil
}
