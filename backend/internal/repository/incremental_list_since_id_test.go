package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageLogRepositoryListWithFiltersSinceID(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	sinceID := int64(100)
	// Sort, page and exact-total requests are all overridden: incremental reads
	// are always the newest first page without COUNT.
	filters := usagestats.UsageLogFilters{SinceID: &sinceID, ExactTotal: true}
	params := pagination.PaginationParams{Page: 3, PageSize: 20, SortBy: "created_at", SortOrder: "asc"}

	mock.ExpectQuery("SELECT .* FROM usage_logs WHERE id > \\$1 ORDER BY id DESC LIMIT \\$2 OFFSET \\$3").
		WithArgs(sinceID, 21, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	logs, page, err := repo.ListWithFilters(context.Background(), params, filters)
	require.NoError(t, err)
	require.Empty(t, logs)
	require.Equal(t, 1, page.Page)
	require.Equal(t, int64(0), page.Total)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryListWithFiltersSinceIDKeepsOtherFilters(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	sinceID := int64(7)
	filters := usagestats.UsageLogFilters{AccountID: 3, SinceID: &sinceID}

	mock.ExpectQuery("SELECT .* FROM usage_logs WHERE account_id = \\$1 AND id > \\$2 ORDER BY id DESC LIMIT \\$3 OFFSET \\$4").
		WithArgs(int64(3), sinceID, 6, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	_, _, err := repo.ListWithFilters(context.Background(), pagination.PaginationParams{Page: 1, PageSize: 5}, filters)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBuildOpsErrorLogsWhere_SinceID(t *testing.T) {
	where, args := buildOpsErrorLogsWhere(&service.OpsErrorLogFilter{})
	require.NotContains(t, where, "e.id >")

	sinceID := int64(42)
	where, args = buildOpsErrorLogsWhere(&service.OpsErrorLogFilter{SinceID: &sinceID})
	require.True(t, strings.HasSuffix(where, " AND e.id > $"+itoa(len(args))), where)
	require.Equal(t, sinceID, args[len(args)-1])
}

func TestOpsRepoListErrorLogs_SinceIDSkipsCount(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &opsRepository{db: db}
	sinceID := int64(9)

	// No COUNT is expected; sqlmock fails on any unexpected query.
	mock.ExpectQuery("FROM ops_error_logs e .* AND e.id > \\$1\\s+ORDER BY e.id DESC\\s+LIMIT \\$2 OFFSET \\$3").
		WithArgs(sinceID, 11, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	list, err := repo.ListErrorLogs(context.Background(), &service.OpsErrorLogFilter{
		SinceID: &sinceID, Page: 1, PageSize: 10, SortBy: "status_code", SortOrder: "asc",
	})
	require.NoError(t, err)
	require.Equal(t, 1, list.Page)
	require.Equal(t, 10, list.PageSize)
	require.False(t, list.HasMore)
	require.Empty(t, list.Errors)
	require.NoError(t, mock.ExpectationsWereMet())
}
