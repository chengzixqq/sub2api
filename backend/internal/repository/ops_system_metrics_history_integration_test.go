//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpsRepositoryListSystemMetricsHistory_BucketsAndAverages(t *testing.T) {
	ctx := context.Background()
	// A fixed past window that no collector writes to.
	start := time.Date(2011, 7, 13, 3, 0, 0, 0, time.UTC)
	end := start.Add(10 * time.Minute)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM ops_system_metrics WHERE created_at >= $1 AND created_at <= $2", start.Add(-time.Hour), end.Add(time.Hour))
	})

	insert := func(at time.Time, windowMinutes int, platform any, groupID any, cpu float64, dbOK bool) {
		t.Helper()
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO ops_system_metrics
			(created_at, window_minutes, platform, group_id, cpu_usage_percent, memory_usage_percent, memory_used_mb,
			 goroutine_count, concurrency_queue_depth, db_ok, redis_ok)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			at, windowMinutes, platform, groupID, cpu, cpu/2, int64(cpu*10), int(cpu)+100, int(cpu)/10, dbOK, true)
		require.NoError(t, err)
	}
	for i := 1; i <= 10; i++ {
		insert(start.Add(time.Duration(i)*time.Minute), 1, nil, nil, float64(i*10), i != 3)
	}
	// Noise that must not be counted: on the open left edge, per-platform, per-group, 5-minute window, after end.
	insert(start, 1, nil, nil, 999, false)
	insert(start.Add(2*time.Minute), 1, "anthropic", nil, 999, false)
	insert(start.Add(2*time.Minute), 1, nil, int64(42), 999, false)
	insert(start.Add(5*time.Minute), 5, nil, nil, 999, false)
	insert(end.Add(time.Second), 1, nil, nil, 999, false)

	repo := NewOpsRepository(integrationDB).(*opsRepository)

	points, err := repo.ListSystemMetricsHistory(ctx, start, end, 60)
	require.NoError(t, err)
	require.Len(t, points, 10)
	for i, p := range points {
		minute := i + 1
		require.True(t, p.CreatedAt.Equal(start.Add(time.Duration(minute)*time.Minute)), "point %d at %s", i, p.CreatedAt)
		require.Equal(t, 1, p.SampleCount)
		require.NotNil(t, p.CPUUsagePercent)
		require.InDelta(t, float64(minute*10), *p.CPUUsagePercent, 1e-9)
		require.InDelta(t, float64(minute*5), *p.MemoryUsagePercent, 1e-9)
		require.Equal(t, int64(minute*100), *p.MemoryUsedMB)
		require.Equal(t, minute*10+100, *p.GoroutineCount)
		require.Equal(t, minute, *p.ConcurrencyQueueDepth)
		require.Equal(t, minute != 3, *p.DBOK)
		require.True(t, *p.RedisOK)
	}

	points, err = repo.ListSystemMetricsHistory(ctx, start, end, 300)
	require.NoError(t, err)
	require.Len(t, points, 2)
	require.True(t, points[0].CreatedAt.Equal(start.Add(5*time.Minute)))
	require.True(t, points[1].CreatedAt.Equal(end))
	require.Equal(t, 5, points[0].SampleCount)
	require.Equal(t, 5, points[1].SampleCount)
	require.InDelta(t, 30.0, *points[0].CPUUsagePercent, 1e-9)
	require.InDelta(t, 80.0, *points[1].CPUUsagePercent, 1e-9)
	require.Equal(t, 130, *points[0].GoroutineCount)
	require.Equal(t, int64(300), *points[0].MemoryUsedMB)
	require.False(t, *points[0].DBOK, "one failed probe marks the whole bucket")
	require.True(t, *points[1].DBOK)

	points, err = repo.ListSystemMetricsHistory(ctx, end.Add(time.Hour), end.Add(2*time.Hour), 60)
	require.NoError(t, err)
	require.NotNil(t, points)
	require.Empty(t, points)
}
