package repository

import (
	"context"
	"database/sql/driver"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

const (
	opsRequestedUsageModelExpr  = "COALESCE(NULLIF(TRIM(ul.requested_model), ''), ul.model)"
	opsUpstreamUsageModelExpr   = "COALESCE(NULLIF(TRIM(ul.upstream_model), ''), ul.model)"
	opsRequestedErrorModelExpr  = "COALESCE(NULLIF(TRIM(requested_model), ''), model)"
	opsUpstreamErrorModelExpr   = "COALESCE(NULLIF(TRIM(upstream_model), ''), NULLIF(TRIM(requested_model), ''), model)"
	opsModelFilterTestModelName = "claude-opus-4"
)

func TestBuildOpsUsageWhere_ModelFilter(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	_, where, args, next := buildUsageWhere(&service.OpsDashboardFilter{}, start, end, 1)
	require.Equal(t, "WHERE ul.created_at >= $1 AND ul.created_at < $2", where)
	require.Len(t, args, 2)
	require.Equal(t, 3, next)

	_, where, args, next = buildUsageWhere(&service.OpsDashboardFilter{Model: opsModelFilterTestModelName, ModelSource: "requested"}, start, end, 1)
	require.True(t, strings.HasSuffix(where, " AND "+opsRequestedUsageModelExpr+" = $3"), where)
	require.Equal(t, opsModelFilterTestModelName, args[len(args)-1])
	require.Equal(t, 4, next)

	_, where, _, _ = buildUsageWhere(&service.OpsDashboardFilter{Model: opsModelFilterTestModelName, ModelSource: "upstream"}, start, end, 1)
	require.True(t, strings.HasSuffix(where, " AND "+opsUpstreamUsageModelExpr+" = $3"), where)

	groupID := int64(7)
	join, where, args, next := buildUsageWhere(&service.OpsDashboardFilter{GroupID: &groupID, Platform: "openai", Model: opsModelFilterTestModelName}, start, end, 1)
	require.NotEmpty(t, join)
	require.True(t, strings.HasSuffix(where, " AND "+opsRequestedUsageModelExpr+" = $5"), where)
	require.Equal(t, []any{start, end, groupID, "openai", opsModelFilterTestModelName}, args)
	require.Equal(t, 6, next)

	_, where, _, _ = buildUsageWhere(&service.OpsDashboardFilter{Model: opsModelFilterTestModelName, ModelSource: "mapping"}, start, end, 1)
	require.True(t, strings.HasSuffix(where, " AND "+opsRequestedUsageModelExpr+" = $3"), "mapping never reaches SQL: "+where)
}

func TestBuildOpsErrorWhere_ModelFilter(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	where, args, next := buildErrorWhere(&service.OpsDashboardFilter{}, start, end, 1)
	require.Equal(t, "WHERE created_at >= $1 AND created_at < $2 AND is_count_tokens = FALSE", where)
	require.Len(t, args, 2)
	require.Equal(t, 3, next)

	where, args, next = buildErrorWhere(&service.OpsDashboardFilter{Model: opsModelFilterTestModelName}, start, end, 1)
	require.Equal(t, "WHERE created_at >= $1 AND created_at < $2 AND is_count_tokens = FALSE AND "+opsRequestedErrorModelExpr+" = $3", where)
	require.Equal(t, []any{start, end, opsModelFilterTestModelName}, args)
	require.Equal(t, 4, next)

	where, _, _ = buildErrorWhere(&service.OpsDashboardFilter{Model: opsModelFilterTestModelName, ModelSource: "upstream"}, start, end, 1)
	require.True(t, strings.HasSuffix(where, " AND "+opsUpstreamErrorModelExpr+" = $3"), where)
}

func TestOpsRepositoryGetOpenAITokenStats_ModelFilter(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &opsRepository{db: db}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	filter := &service.OpsOpenAITokenStatsFilter{
		TimeRange: "1h", StartTime: start, EndTime: end,
		Model: "  " + opsModelFilterTestModelName + " ", ModelSource: "upstream", TopN: 20,
	}

	where := "WHERE ul.created_at >= $1 AND ul.created_at < $2 AND " + opsUpstreamUsageModelExpr + " = $3"
	args := []driver.Value{start, end, opsModelFilterTestModelName}
	queryPrefix := regexp.QuoteMeta(where) + `\s+GROUP BY ul.model\s+\)`
	mock.ExpectQuery(queryPrefix + `\s+SELECT COUNT\(\*\) FROM stats`).
		WithArgs(args...).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(queryPrefix + `\s+SELECT model,[\s\S]+LIMIT \$4`).
		WithArgs(append(args, 20)...).
		WillReturnRows(sqlmock.NewRows([]string{
			"model", "request_count", "avg_tokens_per_sec", "avg_first_token_ms",
			"total_output_tokens", "avg_duration_ms", "requests_with_first_token",
		}).AddRow(opsModelFilterTestModelName, int64(2), 10.0, 100.0, int64(20), int64(1000), int64(2)))

	resp, err := repo.GetOpenAITokenStats(context.Background(), filter)
	require.NoError(t, err)
	require.Equal(t, int64(1), resp.Total)
	require.Len(t, resp.Items, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpsThroughputTrend_ModelFilterSkipsBreakdown(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &opsRepository{db: db}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Minute)
	filter := &service.OpsDashboardFilter{StartTime: start, EndTime: end, Model: opsModelFilterTestModelName, ModelSource: "requested"}

	mock.ExpectQuery(regexp.QuoteMeta(opsRequestedUsageModelExpr+" = $3")+`[\s\S]+`+regexp.QuoteMeta(opsRequestedErrorModelExpr+" = $6")).
		WithArgs(start, end, opsModelFilterTestModelName, start, end, opsModelFilterTestModelName).
		WillReturnRows(sqlmock.NewRows([]string{"bucket", "request_count", "token_consumed", "switch_count"}))

	resp, err := repo.GetThroughputTrend(context.Background(), filter, 60)
	require.NoError(t, err)
	require.Nil(t, resp.ByPlatform)
	require.Nil(t, resp.TopGroups)
	require.NotEmpty(t, resp.Points)
	require.NoError(t, mock.ExpectationsWereMet())

	platformFilter := *filter
	platformFilter.Platform = "openai"
	mock.ExpectQuery(`FROM usage_logs ul`).
		WillReturnRows(sqlmock.NewRows([]string{"bucket", "request_count", "token_consumed", "switch_count"}))
	resp, err = repo.GetThroughputTrend(context.Background(), &platformFilter, 60)
	require.NoError(t, err)
	require.Nil(t, resp.TopGroups)
	require.NoError(t, mock.ExpectationsWereMet())
}
