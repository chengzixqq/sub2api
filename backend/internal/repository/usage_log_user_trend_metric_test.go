package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

// DashboardService finds the metric-aware filtered user trend by duck typing,
// so a signature drift would not break the build; it would only make
// actual_cost rankings fail at runtime. Pin the match here.
func TestUsageLogRepositoryProvidesFilteredUserTrendByMetric(t *testing.T) {
	repo := NewUsageLogRepository(nil, nil)
	_, ok := repo.(interface {
		GetUserUsageTrendWithFiltersByMetric(context.Context, time.Time, time.Time, string, int, string, usagestats.UsageLogFilters) ([]usagestats.UserUsageTrendPoint, error)
	})
	require.True(t, ok)
}
