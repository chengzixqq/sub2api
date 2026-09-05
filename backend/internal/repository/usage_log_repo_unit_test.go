//go:build unit

package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestSafeDateFormat(t *testing.T) {
	tests := []struct {
		name        string
		granularity string
		expected    string
	}{
		// 合法值
		{"hour", "hour", "YYYY-MM-DD HH24:00"},
		{"day", "day", "YYYY-MM-DD"},
		{"week", "week", "IYYY-IW"},
		{"month", "month", "YYYY-MM"},

		// 非法值回退到默认
		{"空字符串", "", "YYYY-MM-DD"},
		{"未知粒度 year", "year", "YYYY-MM-DD"},
		{"未知粒度 minute", "minute", "YYYY-MM-DD"},

		// 恶意字符串
		{"SQL 注入尝试", "'; DROP TABLE users; --", "YYYY-MM-DD"},
		{"带引号", "day'", "YYYY-MM-DD"},
		{"带括号", "day)", "YYYY-MM-DD"},
		{"Unicode", "日", "YYYY-MM-DD"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := safeDateFormat(tc.granularity)
			require.Equal(t, tc.expected, got, "safeDateFormat(%q)", tc.granularity)
		})
	}
}

func TestBuildUsageLogBatchInsertQuery_UsesConflictDoNothing(t *testing.T) {
	log := &service.UsageLog{
		UserID:       1,
		APIKeyID:     2,
		AccountID:    3,
		RequestID:    "req-batch-no-update",
		Model:        "gpt-5",
		InputTokens:  10,
		OutputTokens: 5,
		TotalCost:    1.2,
		ActualCost:   1.2,
		CreatedAt:    time.Now().UTC(),
	}
	prepared := prepareUsageLogInsert(log)

	query, _ := buildUsageLogBatchInsertQuery([]string{usageLogBatchKey(log.RequestID, log.APIKeyID)}, map[string]usageLogInsertPrepared{
		usageLogBatchKey(log.RequestID, log.APIKeyID): prepared,
	})

	require.Contains(t, query, "ON CONFLICT (request_id, api_key_id) DO NOTHING")
	require.NotContains(t, strings.ToUpper(query), "DO UPDATE")
}

func TestGetBatchAPIKeyUsageStats_RetriesSharedMemoryExhaustionWithoutParallelWorkers(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	repo := newUsageLogRepositoryWithSQL(nil, db)
	queryPattern := `(?s)SELECT\s+api_key_id,.*FROM usage_logs.*GROUP BY api_key_id`
	startTime := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	endTime := startTime.AddDate(0, 1, 0)

	mock.ExpectQuery(queryPattern).
		WithArgs(sqlmock.AnyArg(), startTime, endTime, sqlmock.AnyArg()).
		WillReturnError(&pq.Error{
			Code:    "53100",
			Message: `could not resize shared memory segment "/PostgreSQL.1" to 25223168 bytes: No space left on device`,
		})
	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL max_parallel_workers_per_gather = 0`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(queryPattern).
		WithArgs(sqlmock.AnyArg(), startTime, endTime, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"api_key_id", "total_cost", "today_cost"}).
			AddRow(int64(7), 12.5, 1.25))
	mock.ExpectCommit()

	stats, err := repo.GetBatchAPIKeyUsageStats(context.Background(), []int64{7}, startTime, endTime)
	require.NoError(t, err)
	require.Equal(t, 12.5, stats[7].TotalActualCost)
	require.Equal(t, 1.25, stats[7].TodayActualCost)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestIsDynamicSharedMemoryExhaustion_DoesNotRetryOtherDiskFullErrors(t *testing.T) {
	require.False(t, isDynamicSharedMemoryExhaustion(&pq.Error{
		Code:    "53100",
		Message: "could not write to file pg_wal/xlogtemp: No space left on device",
	}))
	require.True(t, isDynamicSharedMemoryExhaustion(&pq.Error{
		Code:    "53100",
		Message: `could not resize shared memory segment "/PostgreSQL.1" to 25223168 bytes: No space left on device`,
	}))
}
