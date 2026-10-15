package service

import (
	"context"
	"slices"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

// SetScopedRollingRPMCache is configured during composition, before requests.
func (s *UsageService) SetScopedRollingRPMCache(cache ScopedRollingRPMCache) {
	s.rollingRPM = cache
}

// WithUserRuntimeRPM refreshes only the authenticated user's live display rate.
func (s *UsageService) WithUserRuntimeRPM(ctx context.Context, stats *usagestats.UserDashboardStats, userID int64) *usagestats.UserDashboardStats {
	var read func(context.Context) (int, error)
	if s.rollingRPM != nil && userID > 0 {
		read = func(ctx context.Context) (int, error) { return s.rollingRPM.GetUserRollingRPM(ctx, userID) }
	}
	return withScopedRuntimeRPM(ctx, stats, read)
}

// WithAPIKeyRuntimeRPM uses the API key identity already checked by the caller.
func (s *UsageService) WithAPIKeyRuntimeRPM(ctx context.Context, stats *usagestats.UserDashboardStats, apiKeyID int64) *usagestats.UserDashboardStats {
	var read func(context.Context) (int, error)
	if s.rollingRPM != nil && apiKeyID > 0 {
		read = func(ctx context.Context) (int, error) { return s.rollingRPM.GetAPIKeyRollingRPM(ctx, apiKeyID) }
	}
	return withScopedRuntimeRPM(ctx, stats, read)
}

func withScopedRuntimeRPM(ctx context.Context, stats *usagestats.UserDashboardStats, read func(context.Context) (int, error)) *usagestats.UserDashboardStats {
	if stats == nil {
		return nil
	}
	current := *stats
	current.ByPlatform = slices.Clone(stats.ByPlatform)
	available := false
	current.RPMAvailable = &available
	current.Rpm = 0
	if read == nil {
		return &current
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	count, err := read(ctx)
	if err != nil {
		return &current
	}
	current.Rpm = int64(count)
	available = true
	return &current
}
