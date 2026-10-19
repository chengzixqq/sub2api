package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

var requestTypeStatsColumns = []string{"effective_request_type", "requests", "total_tokens", "cost", "actual_cost", "account_cost"}

// DashboardService finds the grouped query by duck typing, so a signature
// drift would not break the build; it would only make the endpoint fail at
// runtime. Pin the match here.
func TestUsageLogRepositoryProvidesRequestTypeStats(t *testing.T) {
	repo := NewUsageLogRepository(nil, nil)
	_, ok := repo.(interface {
		GetRequestTypeStatsWithUsageFilters(context.Context, time.Time, time.Time, usagestats.UsageLogFilters) ([]usagestats.RequestTypeStat, error)
	})
	require.True(t, ok)
}

// The bucket must be the legacy-aware CASE, grouped by its own alias: grouping
// by a column named request_type would silently split by the raw column and
// leave every pre-request_type row in "unknown".
func TestUsageLogRepositoryGetRequestTypeStatsSQLShape(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	end := start.Add(72 * time.Hour)

	shape := `(?s)CASE WHEN request_type IN \(1, 2, 3, 4, 5\) THEN request_type WHEN openai_ws_mode THEN 3 WHEN stream THEN 2 ELSE 1 END AS effective_request_type` +
		`.*FROM usage_logs\s+WHERE created_at >= \$1 AND created_at < \$2` +
		`.*GROUP BY effective_request_type\s+ORDER BY requests DESC, effective_request_type ASC`
	mock.ExpectQuery(shape).
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows(requestTypeStatsColumns).
			AddRow(int64(2), int64(9), int64(900), 3.0, 2.5, 1.5).
			AddRow(int64(4), int64(2), int64(20), 0.2, 0.1, 0.05).
			AddRow(int64(5), int64(1), int64(10), 0.1, 0.1, 0.1))

	got, err := repo.GetRequestTypeStatsWithUsageFilters(context.Background(), start, end, UsageLogFilters{})
	require.NoError(t, err)
	require.Equal(t, []usagestats.RequestTypeStat{
		{RequestType: "stream", Requests: 9, TotalTokens: 900, Cost: 3, ActualCost: 2.5, AccountCost: 1.5},
		{RequestType: "cyber", Requests: 2, TotalTokens: 20, Cost: 0.2, ActualCost: 0.1, AccountCost: 0.05},
		{RequestType: "live", Requests: 1, TotalTokens: 10, Cost: 0.1, ActualCost: 0.1, AccountCost: 0.1},
	}, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetRequestTypeStatsDoesNotGroupByRawColumn(t *testing.T) {
	require.NotRegexp(t, regexp.MustCompile(`GROUP BY\s+request_type`), usageLogRequestTypeStatsQuery("WHERE TRUE"))
	require.NotRegexp(t, regexp.MustCompile(`AS request_type\b`), usageLogRequestTypeStatsQuery("WHERE TRUE"))
}

func TestUsageLogRepositoryGetRequestTypeStatsAppliesAccountScopeAndFilters(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	ctx := service.WithUsageAccountScope(context.Background(), []int64{11, 12})

	mock.ExpectQuery(`(?s)account_id = ANY\(\$4\).*COALESCE\(NULLIF\(TRIM\(upstream_model\), ''\), model\) = \$5.*GROUP BY effective_request_type`).
		WithArgs(start, end, int64(3), pq.Array([]int64{11, 12}), "gpt-5").
		WillReturnRows(sqlmock.NewRows(requestTypeStatsColumns))

	got, err := repo.GetRequestTypeStatsWithUsageFilters(ctx, start, end, UsageLogFilters{GroupID: 3, Model: "gpt-5", ModelFilterSource: usagestats.ModelSourceUpstream})
	require.NoError(t, err)
	require.NotNil(t, got, "no rows must still be an empty slice so the JSON is []")
	require.Empty(t, got)
	require.NoError(t, mock.ExpectationsWereMet())
}
