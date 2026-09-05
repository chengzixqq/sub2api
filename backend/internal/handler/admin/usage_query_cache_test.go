package admin

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type usageStatsCacheScopeProbe struct {
	service.UsageLogRepository
	calls atomic.Int32
}

func (r *usageStatsCacheScopeProbe) GetStatsWithFilters(ctx context.Context, _ usagestats.UsageLogFilters) (*usagestats.UsageStats, error) {
	r.calls.Add(1)
	totalRequests := int64(1)
	if accountIDs, restricted := service.UsageAccountScopeFrom(ctx); restricted && len(accountIDs) > 0 {
		totalRequests = accountIDs[0]
	}
	return &usagestats.UsageStats{TotalRequests: totalRequests}, nil
}

func TestUsageStatsCacheKey_StableAndDistinct(t *testing.T) {
	start := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 31, 0, 0, 0, 0, time.UTC)
	base := usagestats.UsageLogFilters{StartTime: &start, EndTime: &end, Model: "claude-3"}

	k1 := usageStatsCacheKey(base)
	k2 := usageStatsCacheKey(base)
	require.NotEmpty(t, k1)
	require.Equal(t, k1, k2, "same filters must produce same key")

	other := base
	other.Model = "gpt-4o"
	require.NotEqual(t, k1, usageStatsCacheKey(other), "different model must change key")

	withUser := base
	withUser.UserID = 7
	require.NotEqual(t, k1, usageStatsCacheKey(withUser), "different user must change key")
}

func TestUsageStatsCache_RestrictedScopesBypassSharedEntries(t *testing.T) {
	previousCache := usageStatsCache
	usageStatsCache = newSnapshotCache(30 * time.Second)
	t.Cleanup(func() { usageStatsCache = previousCache })

	repo := &usageStatsCacheScopeProbe{}
	usageSvc := service.NewUsageService(repo, nil, nil, nil)
	handler := NewUsageHandler(usageSvc, nil, nil, nil)
	filters := usagestats.UsageLogFilters{Model: "gpt-5"}

	admin, hit, err := handler.getStatsCached(context.Background(), filters)
	require.NoError(t, err)
	require.False(t, hit)
	require.Equal(t, int64(1), admin.TotalRequests)

	adminCached, hit, err := handler.getStatsCached(context.Background(), filters)
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, int64(1), adminCached.TotalRequests)

	vendorA, hit, err := handler.getStatsCached(service.WithUsageAccountScope(context.Background(), []int64{101}), filters)
	require.NoError(t, err)
	require.False(t, hit)
	require.Equal(t, int64(101), vendorA.TotalRequests)

	vendorB, hit, err := handler.getStatsCached(service.WithUsageAccountScope(context.Background(), []int64{202}), filters)
	require.NoError(t, err)
	require.False(t, hit)
	require.Equal(t, int64(202), vendorB.TotalRequests)
	require.Equal(t, int32(3), repo.calls.Load())

	adminAgain, hit, err := handler.getStatsCached(context.Background(), filters)
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, int64(1), adminAgain.TotalRequests)
	require.Equal(t, int32(3), repo.calls.Load())
}
