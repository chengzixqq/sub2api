package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
	"github.com/stretchr/testify/require"
)

var userDashboardSegmentColumns = []string{
	"platform", "total_requests", "total_input_tokens", "total_output_tokens",
	"total_cache_creation_tokens", "total_cache_read_tokens", "total_cost", "total_actual_cost", "total_duration_ms", "duration_count",
	"successful_requests",
}

var apiKeyDashboardSegmentColumns = []string{
	"total_requests", "total_input_tokens", "total_output_tokens", "total_cache_creation_tokens",
	"total_cache_read_tokens", "total_cost", "total_actual_cost", "total_duration_ms", "duration_count",
}

// requirePlainDashboardSegmentQuery keeps each segment an ordinary GROUP BY over
// one side of today's start, which retains parallel partial aggregation.
func requirePlainDashboardSegmentQuery(t *testing.T, query, boundary string) {
	t.Helper()
	require.Contains(t, query, boundary)
	require.Contains(t, query, "GROUP BY")
	require.NotContains(t, query, "GROUPING SETS")
	require.NotContains(t, query, " OVER ")
}

func TestUserDashboardStats_PreservesGrandTotalAndSuccessfulPlatformCounts(t *testing.T) {
	db, mock, captured := newCapturingSQLMock(t)
	repo := &usageLogRepository{sql: db}
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{"total", "active"}).AddRow(3, 2))
	// History before today's start.
	mock.ExpectQuery("").WithArgs(int64(42), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows(userDashboardSegmentColumns).
		AddRow("openai", 1, 10, 5, 2, 1, "0.6", "0.3", 101, 1, 1))
	// Combining every cohort keeps billed failures and logs with no platform.
	mock.ExpectQuery("").WithArgs(int64(42), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows(userDashboardSegmentColumns).
		AddRow("openai", 2, 20, 10, 4, 2, "1.2", "0.6", 200, 1, 0).
		AddRow(nil, 1, 10, 5, 2, 1, "0.6", "0.3", 0, 0, 1))
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
	require.Equal(t, int64(36), stats.ByPlatform[0].TodayTokens)
	require.Equal(t, 0.9, stats.ByPlatform[0].TotalActualCost)
	require.Equal(t, 0.6, stats.ByPlatform[0].TodayActualCost)
	require.Equal(t, int64(2), stats.Rpm)
	require.Equal(t, int64(60), stats.Tpm)
	require.Len(t, *captured, 4)
	requirePlainDashboardSegmentQuery(t, (*captured)[1], "ul.created_at < $2")
	requirePlainDashboardSegmentQuery(t, (*captured)[2], "ul.created_at >= $2")
}

func TestUserDashboardStats_EmptyHistoryRetainsKeyCounts(t *testing.T) {
	db, mock, _ := newCapturingSQLMock(t)
	repo := &usageLogRepository{sql: db}
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{"total", "active"}).AddRow(2, 1))
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows(userDashboardSegmentColumns))
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows(userDashboardSegmentColumns))
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
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows(userDashboardSegmentColumns).
		AddRow("anthropic", 1, 0, 0, 0, 0, "0.20", "0.20", 101, 1, 1))
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows(userDashboardSegmentColumns).
		AddRow("openai", 1, 0, 0, 0, 0, "0.10", "0.10", 202, 1, 1).
		AddRow("anthropic", 1, 0, 0, 0, 0, "0.20", "0.20", 0, 0, 1))
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{"requests", "tokens"}).AddRow(0, 0))
	stats, err := repo.GetUserDashboardStats(context.Background(), 42)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
	require.Equal(t, 0.5, stats.TotalCost)
	require.Equal(t, 0.5, stats.TotalActualCost)
	require.Equal(t, 0.3, stats.TodayCost)
	require.Equal(t, 0.3, stats.TodayActualCost)
	require.Equal(t, 151.5, stats.AverageDurationMs, "AVG must add both segments' non-NULL duration sums and counts")
	require.Len(t, stats.ByPlatform, 2)
	require.Equal(t, "anthropic", stats.ByPlatform[0].Platform, "platforms sort by the combined exact cost")
	require.Equal(t, 0.4, stats.ByPlatform[0].TotalActualCost)
	require.Equal(t, int64(2), stats.ByPlatform[0].TotalRequests)
	require.Equal(t, "openai", stats.ByPlatform[1].Platform)
}

func TestAPIKeyDashboardStats_CombinesHistoryAndToday(t *testing.T) {
	db, mock, captured := newCapturingSQLMock(t)
	repo := &usageLogRepository{sql: db}
	mock.ExpectQuery("").WithArgs(int64(9), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows(apiKeyDashboardSegmentColumns).
		AddRow(3, 30, 15, 6, 3, "0.20", "0.10", 300, 2))
	mock.ExpectQuery("").WithArgs(int64(9), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows(apiKeyDashboardSegmentColumns).
		AddRow(2, 20, 10, 4, 2, "0.10", "0.20", 0, 0))
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{"requests", "tokens"}).AddRow(5, 100))
	stats, err := repo.GetAPIKeyDashboardStats(context.Background(), 9)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
	require.Equal(t, int64(1), stats.TotalAPIKeys)
	require.Equal(t, int64(1), stats.ActiveAPIKeys)
	require.Equal(t, int64(5), stats.TotalRequests)
	require.Equal(t, int64(90), stats.TotalTokens)
	require.Equal(t, 0.3, stats.TotalCost)
	require.Equal(t, 0.3, stats.TotalActualCost)
	require.Equal(t, 150.0, stats.AverageDurationMs)
	require.Equal(t, int64(2), stats.TodayRequests)
	require.Equal(t, int64(36), stats.TodayTokens)
	require.Equal(t, 0.1, stats.TodayCost)
	require.Equal(t, 0.2, stats.TodayActualCost)
	require.Equal(t, int64(1), stats.Rpm)
	require.Equal(t, int64(20), stats.Tpm)
	require.Nil(t, stats.ByPlatform)
	require.Len(t, *captured, 3)
	require.Contains(t, (*captured)[0], "created_at < $2")
	require.Contains(t, (*captured)[1], "created_at >= $2")
}

func TestDashboardHistoryCache_ReusesHistoryWithoutMutatingIt(t *testing.T) {
	db, mock, captured := newCapturingSQLMock(t)
	repo := &usageLogRepository{sql: db, dashboardHistoryCache: usagequery.NewDetachedCache(time.Minute, time.Minute)}
	keys := func() { mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{"total", "active"}).AddRow(1, 1)) }
	today := func() {
		mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows(userDashboardSegmentColumns).
			AddRow("openai", 1, 1, 0, 0, 0, "0.10", "0.10", 10, 1, 1))
	}
	perf := func() {
		mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{"requests", "tokens"}).AddRow(0, 0))
	}
	keys()
	mock.ExpectQuery("").WithArgs(int64(7), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows(userDashboardSegmentColumns).
		AddRow("openai", 2, 2, 0, 0, 0, "0.20", "0.20", 30, 1, 2))
	today()
	perf()
	// The API key with the same numeric ID must not reuse the user's history.
	mock.ExpectQuery("").WithArgs(int64(7), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows(apiKeyDashboardSegmentColumns).
		AddRow(4, 4, 0, 0, 0, "0.40", "0.40", 0, 0))
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows(apiKeyDashboardSegmentColumns).AddRow(0, 0, 0, 0, 0, "0", "0", 0, 0))
	perf()
	keys()
	today()
	perf()

	first, err := repo.GetUserDashboardStats(context.Background(), 7)
	require.NoError(t, err)
	keyStats, err := repo.GetAPIKeyDashboardStats(context.Background(), 7)
	require.NoError(t, err)
	first.ByPlatform[0].TotalRequests = 999
	second, err := repo.GetUserDashboardStats(context.Background(), 7)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())

	require.Equal(t, int64(4), keyStats.TotalRequests)
	require.Equal(t, int64(3), second.TotalRequests)
	require.Equal(t, 0.3, second.TotalActualCost)
	require.Equal(t, 20.0, second.AverageDurationMs)
	require.Equal(t, int64(3), second.ByPlatform[0].TotalRequests, "merging must not change the cached history")
	require.Equal(t, 0.3, second.ByPlatform[0].TotalActualCost)
	history := 0
	for _, query := range *captured {
		if strings.Contains(query, "created_at < $2") {
			history++
		}
	}
	require.Equal(t, 2, history, "one history scan for the user and one for the API key")

	day := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	require.NotEqual(t, dashboardHistoryCacheKey("user", 7, day), dashboardHistoryCacheKey("api_key", 7, day))
	require.NotEqual(t, dashboardHistoryCacheKey("user", 7, day), dashboardHistoryCacheKey("user", 7, day.AddDate(0, 0, 1)))
}
