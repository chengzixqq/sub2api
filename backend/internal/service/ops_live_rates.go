package service

import (
	"context"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// OpsLiveRateCounts are the raw counts behind dashboard/rates. Error counts
// are only meaningful when the repository was asked to include errors.
type OpsLiveRateCounts struct {
	SuccessCount     int64
	TokenConsumed    int64
	ErrorCount       int64
	ErrorCountSLA    int64
	Upstream429Count int64
	Upstream529Count int64
}

// OpsLiveRatesRepository reads live counts with the same where clauses as the
// ops overview and throughput trend.
type OpsLiveRatesRepository interface {
	GetLiveRateCounts(ctx context.Context, filter *OpsDashboardFilter, includeErrors bool) (*OpsLiveRateCounts, error)
}

type OpsLiveRatesQuery struct {
	Window   time.Duration
	End      time.Time
	Platform string
	GroupID  *int64
}

// OpsLiveRates is the recent-window request / token rate. Token rate includes
// cache tokens. Error fields are null when ops monitoring is off, because
// error logs are not written then.
type OpsLiveRates struct {
	WindowSeconds        int       `json:"window_seconds"`
	StartTime            time.Time `json:"start_time"`
	EndTime              time.Time `json:"end_time"`
	SuccessCount         int64     `json:"success_count"`
	ErrorCount           *int64    `json:"error_count"`
	ErrorCountSLA        *int64    `json:"error_count_sla"`
	RequestCount         int64     `json:"request_count"`
	TokenConsumed        int64     `json:"token_consumed"`
	RPM                  float64   `json:"rpm"`
	TPM                  float64   `json:"tpm"`
	ErrorRate            *float64  `json:"error_rate"`
	Upstream429Count     *int64    `json:"upstream_429_count"`
	Upstream529Count     *int64    `json:"upstream_529_count"`
	OpsMonitoringEnabled bool      `json:"ops_monitoring_enabled"`
	ErrorsAvailable      bool      `json:"errors_available"`
	RPMIncludesErrors    bool      `json:"rpm_includes_errors"`
}

var opsLiveRatesWindows = map[string]time.Duration{
	"1m":  time.Minute,
	"5m":  5 * time.Minute,
	"15m": 15 * time.Minute,
}

// ParseOpsLiveRatesWindow accepts 1m, 5m or 15m; empty means 5m.
func ParseOpsLiveRatesWindow(raw string) (time.Duration, error) {
	if raw == "" {
		return 5 * time.Minute, nil
	}
	window, ok := opsLiveRatesWindows[raw]
	if !ok {
		return 0, infraerrors.BadRequest("INVALID_WINDOW", "Invalid window")
	}
	return window, nil
}

// GetLiveRates works whether or not ops monitoring is enabled: RPM / TPM come
// from usage_logs, which is always written; error counts come from
// ops_error_logs only while monitoring is on.
func (s *OpsService) GetLiveRates(ctx context.Context, q OpsLiveRatesQuery) (*OpsLiveRates, error) {
	repo, ok := s.opsRepo.(OpsLiveRatesRepository)
	if s.opsRepo == nil || !ok {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if q.Window <= 0 {
		q.Window = 5 * time.Minute
	}
	end := q.End.UTC().Truncate(time.Second)
	start := end.Add(-q.Window)
	monitoring := s.IsMonitoringEnabled(ctx)

	counts, err := repo.GetLiveRateCounts(ctx, &OpsDashboardFilter{
		StartTime: start, EndTime: end, Platform: q.Platform, GroupID: q.GroupID, QueryMode: OpsQueryModeRaw,
	}, monitoring)
	if err != nil {
		return nil, err
	}

	windowSeconds := q.Window.Seconds()
	out := &OpsLiveRates{
		WindowSeconds:        int(windowSeconds),
		StartTime:            start,
		EndTime:              end,
		SuccessCount:         counts.SuccessCount,
		RequestCount:         counts.SuccessCount,
		TokenConsumed:        counts.TokenConsumed,
		OpsMonitoringEnabled: monitoring,
	}
	if monitoring {
		errorCount, errorCountSLA := counts.ErrorCount, counts.ErrorCountSLA
		upstream429, upstream529 := counts.Upstream429Count, counts.Upstream529Count
		errorRate := safeLiveRate(float64(errorCountSLA), float64(counts.SuccessCount+errorCountSLA))
		out.ErrorCount, out.ErrorCountSLA = &errorCount, &errorCountSLA
		out.Upstream429Count, out.Upstream529Count = &upstream429, &upstream529
		out.ErrorRate = &errorRate
		out.RequestCount = counts.SuccessCount + errorCount
		out.ErrorsAvailable = true
		out.RPMIncludesErrors = true
	}
	out.RPM = float64(out.RequestCount) * 60 / windowSeconds
	out.TPM = float64(counts.TokenConsumed) * 60 / windowSeconds
	return out, nil
}

func safeLiveRate(numerator, denominator float64) float64 {
	if denominator <= 0 {
		return 0
	}
	return numerator / denominator
}
