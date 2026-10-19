package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

// userRankingFiltersOnlyRepo predates sorted rankings: it only knows the
// default (actual_cost) order.
type userRankingFiltersOnlyRepo struct {
	UsageLogRepository
	calls   int
	filters usagestats.UsageLogFilters
}

func (r *userRankingFiltersOnlyRepo) GetUserSpendingRankingWithFilters(_ context.Context, _, _ time.Time, _ int, filters usagestats.UsageLogFilters) (*usagestats.UserSpendingRankingResponse, error) {
	r.calls++
	r.filters = filters
	return &usagestats.UserSpendingRankingResponse{TotalActualCost: 5}, nil
}

type userRankingSortedRepo struct {
	UsageLogRepository
	sortBy  string
	limit   int
	filters usagestats.UsageLogFilters
}

func (r *userRankingSortedRepo) GetUserSpendingRankingSorted(_ context.Context, _, _ time.Time, limit int, sortBy string, filters usagestats.UsageLogFilters) (*usagestats.UserSpendingRankingResponse, error) {
	r.sortBy, r.limit, r.filters = sortBy, limit, filters
	return &usagestats.UserSpendingRankingResponse{TotalUsers: 3}, nil
}

func TestDashboardServiceUserSpendingRankingSorted_Fallback(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	filters := usagestats.UsageLogFilters{GroupID: 9, Model: "m"}

	legacy := &userRankingFiltersOnlyRepo{}
	svc := NewDashboardService(legacy, nil, nil, nil)
	for _, sortBy := range []string{"", usagestats.UserRankingSortActualCost} {
		got, err := svc.GetUserSpendingRankingSorted(ctx, start, end, 12, sortBy, filters)
		require.NoError(t, err, sortBy)
		require.Equal(t, 5.0, got.TotalActualCost)
	}
	require.Equal(t, 2, legacy.calls)
	require.Equal(t, filters, legacy.filters)

	_, err := svc.GetUserSpendingRankingSorted(ctx, start, end, 12, usagestats.UserRankingSortRequests, filters)
	require.Error(t, err, "a repository without sorted rankings cannot honour other orders")
	require.Equal(t, 2, legacy.calls)

	sorted := &userRankingSortedRepo{}
	svc = NewDashboardService(sorted, nil, nil, nil)
	got, err := svc.GetUserSpendingRankingSorted(ctx, start, end, 7, usagestats.UserRankingSortTokens, filters)
	require.NoError(t, err)
	require.Equal(t, int64(3), got.TotalUsers)
	require.Equal(t, usagestats.UserRankingSortTokens, sorted.sortBy)
	require.Equal(t, 7, sorted.limit)
	require.Equal(t, filters, sorted.filters)
}
