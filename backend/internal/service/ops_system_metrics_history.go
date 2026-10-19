package service

import (
	"context"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	// opsSystemMetricsHistoryWindowMinutes is the collector's snapshot window;
	// history reads only these site-wide rows.
	opsSystemMetricsHistoryWindowMinutes = 1

	opsSystemMetricsHistoryDefaultPoints = 60
	opsSystemMetricsHistoryMaxPoints     = 1440

	// opsSystemMetricsHistoryMaxDays caps the window; minute snapshots are kept
	// for minute_metrics_retention_days (default 30).
	opsSystemMetricsHistoryMaxDays = 31
	// One hour of DST slack, as for openai-token-stats.
	opsSystemMetricsHistoryMaxWindow = opsSystemMetricsHistoryMaxDays*24*time.Hour + time.Hour
)

var (
	ErrOpsSystemMetricsHistoryRangeTooLarge = newOpsTimeRangeTooLargeError(opsSystemMetricsHistoryMaxDays)
	errOpsSystemMetricsInvalidMaxPoints     = infraerrors.BadRequest("INVALID_MAX_POINTS", "Invalid max_points, use an integer between 1 and 1440")
)

// OpsSystemMetricsHistoryPoint aggregates the snapshots of one bucket.
// Percentages and counters are bucket averages; db_ok / redis_ok are false
// when any probe in the bucket failed. created_at is the newest sample.
type OpsSystemMetricsHistoryPoint struct {
	CreatedAt   time.Time `json:"created_at"`
	SampleCount int       `json:"sample_count"`

	CPUUsagePercent    *float64 `json:"cpu_usage_percent"`
	MemoryUsagePercent *float64 `json:"memory_usage_percent"`
	MemoryUsedMB       *int64   `json:"memory_used_mb"`

	ConcurrencyQueueDepth *int `json:"concurrency_queue_depth"`
	GoroutineCount        *int `json:"goroutine_count"`

	DBOK    *bool `json:"db_ok"`
	RedisOK *bool `json:"redis_ok"`
}

// OpsSystemMetricsHistory is the response of ops/system-metrics/history.
// Buckets are anchored at start_time and cover (start, end].
type OpsSystemMetricsHistory struct {
	StartTime     time.Time                       `json:"start_time"`
	EndTime       time.Time                       `json:"end_time"`
	WindowMinutes int                             `json:"window_minutes"`
	BucketSeconds int                             `json:"bucket_seconds"`
	Points        []*OpsSystemMetricsHistoryPoint `json:"points"`
}

// OpsSystemMetricsHistoryRepository reads bucketed site-wide snapshots
// (window_minutes = 1, no platform / group). Optional so test doubles of
// OpsRepository need not implement it.
type OpsSystemMetricsHistoryRepository interface {
	ListSystemMetricsHistory(ctx context.Context, start, end time.Time, bucketSeconds int) ([]*OpsSystemMetricsHistoryPoint, error)
}

// ParseOpsSystemMetricsMaxPoints accepts 1..1440; empty means 60.
func ParseOpsSystemMetricsMaxPoints(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return opsSystemMetricsHistoryDefaultPoints, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || !validOpsSystemMetricsMaxPoints(n) {
		return 0, errOpsSystemMetricsInvalidMaxPoints
	}
	return n, nil
}

func validOpsSystemMetricsMaxPoints(n int) bool {
	return n >= 1 && n <= opsSystemMetricsHistoryMaxPoints
}

// opsSystemMetricsHistoryBucketSeconds picks the smallest whole-minute bucket
// (at least one snapshot interval) that keeps ceil(window / bucket) within
// maxPoints.
func opsSystemMetricsHistoryBucketSeconds(window time.Duration, maxPoints int) int {
	if maxPoints < 1 {
		maxPoints = 1
	}
	span := time.Duration(maxPoints) * time.Minute
	minutes := int((window + span - 1) / span)
	if minutes < 1 {
		minutes = 1
	}
	return minutes * 60
}

// GetSystemMetricsHistory returns CPU / memory / runtime snapshots of the
// window (start, end], averaged into at most maxPoints buckets.
func (s *OpsService) GetSystemMetricsHistory(ctx context.Context, start, end time.Time, maxPoints int) (*OpsSystemMetricsHistory, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	repo, ok := s.opsRepo.(OpsSystemMetricsHistoryRepository)
	if s.opsRepo == nil || !ok {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if !validOpsSystemMetricsMaxPoints(maxPoints) {
		return nil, errOpsSystemMetricsInvalidMaxPoints
	}
	start, end = start.UTC(), end.UTC()
	if !start.Before(end) {
		return nil, infraerrors.BadRequest("OPS_TIME_RANGE_INVALID", "start_time must be before end_time")
	}
	if end.Sub(start) > opsSystemMetricsHistoryMaxWindow {
		return nil, ErrOpsSystemMetricsHistoryRangeTooLarge
	}

	bucketSeconds := opsSystemMetricsHistoryBucketSeconds(end.Sub(start), maxPoints)
	points, err := repo.ListSystemMetricsHistory(ctx, start, end, bucketSeconds)
	if err != nil {
		return nil, err
	}
	if points == nil {
		points = []*OpsSystemMetricsHistoryPoint{}
	}
	return &OpsSystemMetricsHistory{
		StartTime:     start,
		EndTime:       end,
		WindowMinutes: opsSystemMetricsHistoryWindowMinutes,
		BucketSeconds: bucketSeconds,
		Points:        points,
	}, nil
}
