package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

// SetRollingRPMCache attaches the shared successful-upstream request counter.
// It is configured during composition before the service starts serving traffic.
func (s *DashboardService) SetRollingRPMCache(cache RollingRPMCache) {
	s.rollingRPM = cache
}

// WithRuntimeRPM copies cached statistics and observes the same rolling window
// used by account management. Failure is explicit and never substitutes the
// historical five-minute billing-log average for a live request count.
func (s *DashboardService) WithRuntimeRPM(ctx context.Context, stats *usagestats.DashboardStats) *usagestats.DashboardStats {
	if stats == nil {
		return nil
	}
	current := *stats
	available := false
	current.Rpm = 0
	current.RPMAvailable = &available
	if s.rollingRPM == nil {
		return &current
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var count int
	if ids, restricted := UsageAccountScopeFrom(ctx); restricted {
		if len(ids) > 0 {
			counts, err := s.rollingRPM.GetRollingRPMBatch(ctx, ids)
			if err != nil {
				return &current
			}
			seen := make(map[int64]struct{}, len(ids))
			for _, id := range ids {
				if _, duplicate := seen[id]; duplicate {
					continue
				}
				seen[id] = struct{}{}
				rpm, ok := counts[id]
				if !ok {
					return &current
				}
				count += rpm
			}
		}
	} else {
		var err error
		count, err = s.rollingRPM.GetTotalRollingRPM(ctx)
		if err != nil {
			return &current
		}
	}
	available = true
	current.Rpm = int64(count)
	return &current
}
