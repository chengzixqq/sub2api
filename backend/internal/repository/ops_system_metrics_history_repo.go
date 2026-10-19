package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.OpsSystemMetricsHistoryRepository = (*opsRepository)(nil)

// ListSystemMetricsHistory averages site-wide minute snapshots into buckets
// anchored at start; bucket n covers (start+(n-1)*b, start+n*b], so the
// window is (start, end] and the newest snapshot is always included.
func (r *opsRepository) ListSystemMetricsHistory(ctx context.Context, start, end time.Time, bucketSeconds int) ([]*service.OpsSystemMetricsHistoryPoint, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if bucketSeconds <= 0 {
		return nil, fmt.Errorf("bucket_seconds must be positive")
	}

	q := `
SELECT
  MAX(created_at),
  COUNT(*),
  AVG(cpu_usage_percent),
  AVG(memory_usage_percent),
  ROUND(AVG(memory_used_mb))::bigint,
  ROUND(AVG(concurrency_queue_depth))::bigint,
  ROUND(AVG(goroutine_count))::bigint,
  BOOL_AND(db_ok),
  BOOL_AND(redis_ok)
FROM (
  SELECT
    created_at, cpu_usage_percent, memory_usage_percent, memory_used_mb,
    concurrency_queue_depth, goroutine_count, db_ok, redis_ok,
    CEIL(EXTRACT(EPOCH FROM (created_at - $1::timestamptz)) / $3::double precision) AS bucket
  FROM ops_system_metrics
  WHERE window_minutes = 1
    AND platform IS NULL
    AND group_id IS NULL
    AND created_at > $1::timestamptz
    AND created_at <= $2::timestamptz
) s
GROUP BY bucket
ORDER BY bucket`

	rows, err := r.db.QueryContext(ctx, q, start.UTC(), end.UTC(), bucketSeconds)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]*service.OpsSystemMetricsHistoryPoint, 0, 64)
	for rows.Next() {
		var p service.OpsSystemMetricsHistoryPoint
		var cpu, memPct sql.NullFloat64
		var memUsed, queueDepth, goroutines sql.NullInt64
		var dbOK, redisOK sql.NullBool
		if err := rows.Scan(&p.CreatedAt, &p.SampleCount, &cpu, &memPct, &memUsed, &queueDepth, &goroutines, &dbOK, &redisOK); err != nil {
			return nil, err
		}
		p.CreatedAt = p.CreatedAt.UTC()
		if cpu.Valid {
			v := cpu.Float64
			p.CPUUsagePercent = &v
		}
		if memPct.Valid {
			v := memPct.Float64
			p.MemoryUsagePercent = &v
		}
		if memUsed.Valid {
			v := memUsed.Int64
			p.MemoryUsedMB = &v
		}
		if queueDepth.Valid {
			v := int(queueDepth.Int64)
			p.ConcurrencyQueueDepth = &v
		}
		if goroutines.Valid {
			v := int(goroutines.Int64)
			p.GoroutineCount = &v
		}
		if dbOK.Valid {
			v := dbOK.Bool
			p.DBOK = &v
		}
		if redisOK.Valid {
			v := redisOK.Bool
			p.RedisOK = &v
		}
		out = append(out, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
