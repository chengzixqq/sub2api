package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

var userDashboardAggregateColumns = []string{
	"platform", "total_requests", "total_input_tokens", "total_output_tokens",
	"total_cache_creation_tokens", "total_cache_read_tokens", "total_cost", "total_actual_cost", "total_duration_ms", "duration_count",
	"today_requests", "today_input_tokens", "today_output_tokens", "today_cache_creation_tokens", "today_cache_read_tokens",
	"today_cost", "today_actual_cost", "successful_requests", "today_successful_requests",
}

func TestUserDashboardStats_PreservesGrandTotalAndSuccessfulPlatformCounts(t *testing.T) {
	db, mock, captured := newCapturingSQLMock(t)
	repo := &usageLogRepository{sql: db}
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{"total", "active"}).AddRow(3, 2))
	mock.ExpectQuery("").WithArgs(int64(42), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows(userDashboardAggregateColumns).
		// Combining every cohort keeps billed failures and logs with no platform.
		AddRow("openai", 3, 30, 15, 6, 3, "1.8", "0.9", 301, 2, 2, 20, 10, 4, 2, "1.2", "0.6", 1, 0).
		AddRow(nil, 1, 10, 5, 2, 1, "0.6", "0.3", 0, 0, 1, 10, 5, 2, 1, "0.6", "0.3", 1, 1))
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{"requests", "tokens"}).AddRow(10, 300))
	stats, err := repo.GetUserDashboardStats(context.Background(), 42)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
	require.Equal(t, int64(3), stats.TotalAPIKeys)
	require.Equal(t, int64(2), stats.ActiveAPIKeys)
	require.Equal(t, int64(4), stats.TotalRequests)
	require.Equal(t, int64(72), stats.TotalTokens)
	require.Equal(t, 2.4, stats.TotalCost)
	require.Equal(t, 1.2, stats.TotalActualCost)
	require.Equal(t, 150.5, stats.AverageDurationMs)
	require.Equal(t, int64(3), stats.TodayRequests)
	require.Equal(t, int64(54), stats.TodayTokens)
	require.Len(t, stats.ByPlatform, 1)
	require.Equal(t, "openai", stats.ByPlatform[0].Platform)
	require.Equal(t, int64(1), stats.ByPlatform[0].TotalRequests)
	require.Equal(t, int64(0), stats.ByPlatform[0].TodayRequests)
	require.Equal(t, int64(54), stats.ByPlatform[0].TotalTokens)
	require.Equal(t, 0.9, stats.ByPlatform[0].TotalActualCost)
	require.Equal(t, int64(2), stats.Rpm)
	require.Equal(t, int64(60), stats.Tpm)
	require.Len(t, *captured, 3)
}

func TestUserDashboardStats_EmptyHistoryRetainsKeyCounts(t *testing.T) {
	db, mock, _ := newCapturingSQLMock(t)
	repo := &usageLogRepository{sql: db}
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{"total", "active"}).AddRow(2, 1))
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows(userDashboardAggregateColumns))
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{"requests", "tokens"}).AddRow(0, 0))
	stats, err := repo.GetUserDashboardStats(context.Background(), 42)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
	require.Equal(t, int64(2), stats.TotalAPIKeys)
	require.Equal(t, int64(1), stats.ActiveAPIKeys)
	require.Zero(t, stats.TotalRequests)
	require.Zero(t, stats.TodayRequests)
	require.Nil(t, stats.ByPlatform)
}

func TestUserDashboardStats_CombinesNumericCostsBeforeFloatConversion(t *testing.T) {
	db, mock, _ := newCapturingSQLMock(t)
	repo := &usageLogRepository{sql: db}
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{"total", "active"}).AddRow(1, 1))
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows(userDashboardAggregateColumns).
		AddRow("openai", 1, 0, 0, 0, 0, "0.10", "0.10", 101, 1, 1, 0, 0, 0, 0, "0.10", "0.10", 1, 1).
		AddRow("anthropic", 2, 0, 0, 0, 0, "0.20", "0.20", 202, 1, 2, 0, 0, 0, 0, "0.20", "0.20", 2, 2))
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{"requests", "tokens"}).AddRow(0, 0))
	stats, err := repo.GetUserDashboardStats(context.Background(), 42)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
	require.Equal(t, 0.3, stats.TotalCost)
	require.Equal(t, 0.3, stats.TotalActualCost)
	require.Equal(t, 0.3, stats.TodayCost)
	require.Equal(t, 0.3, stats.TodayActualCost)
	require.Equal(t, 151.5, stats.AverageDurationMs, "AVG must use non-NULL duration count, not total requests")
}
