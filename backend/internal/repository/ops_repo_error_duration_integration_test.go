//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsErrorLog_DurationAndFirstTokenRoundTrip(t *testing.T) {
	ctx := context.Background()
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE ops_error_logs RESTART IDENTITY CASCADE")
	repo, ok := NewOpsRepository(integrationDB).(*opsRepository)
	require.True(t, ok)

	duration, ttft := int64(3200), int64(1200)
	timedID, err := repo.InsertErrorLog(ctx, &service.OpsInsertErrorLogInput{
		RequestID: "rid-timed", ErrorPhase: "upstream", ErrorType: "upstream_error", Severity: "error",
		StatusCode: 502, CreatedAt: time.Now(), DurationMs: &duration, TimeToFirstTokenMs: &ttft,
	})
	require.NoError(t, err)
	_, err = repo.BatchInsertErrorLogs(ctx, []*service.OpsInsertErrorLogInput{{
		RequestID: "rid-batch", ErrorPhase: "upstream", ErrorType: "upstream_error", Severity: "error",
		StatusCode: 502, CreatedAt: time.Now(), DurationMs: &duration,
	}})
	require.NoError(t, err)
	legacyID, err := repo.InsertErrorLog(ctx, &service.OpsInsertErrorLogInput{
		RequestID: "rid-legacy", ErrorPhase: "upstream", ErrorType: "upstream_error", Severity: "error",
		StatusCode: 502, CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	detail, err := repo.GetErrorLogByID(ctx, timedID)
	require.NoError(t, err)
	require.Equal(t, duration, *detail.DurationMs)
	require.Equal(t, ttft, *detail.TimeToFirstTokenMs)

	start := time.Now().Add(-time.Hour)
	list, err := repo.ListErrorLogs(ctx, &service.OpsErrorLogFilter{StartTime: &start, Page: 1, PageSize: 10, View: "all"})
	require.NoError(t, err)
	byRequest := map[string]*service.OpsErrorLog{}
	for _, item := range list.Errors {
		byRequest[item.RequestID] = item
	}
	require.Equal(t, duration, *byRequest["rid-timed"].DurationMs)
	require.Equal(t, ttft, *byRequest["rid-timed"].TimeToFirstTokenMs)
	require.Equal(t, duration, *byRequest["rid-batch"].DurationMs, "the batch insert path writes duration too")
	require.Nil(t, byRequest["rid-legacy"].DurationMs)
	require.Nil(t, byRequest["rid-legacy"].TimeToFirstTokenMs)

	legacy, err := repo.GetErrorLogByID(ctx, legacyID)
	require.NoError(t, err)
	require.Nil(t, legacy.DurationMs)
}
