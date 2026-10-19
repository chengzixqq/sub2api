//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// latencyOverviewRepo returns a fixed overview and records the filter the evaluator asked for.
type latencyOverviewRepo struct {
	OpsRepository
	overview   *OpsDashboardOverview
	err        error
	lastFilter *OpsDashboardFilter
}

func (r *latencyOverviewRepo) GetDashboardOverview(_ context.Context, filter *OpsDashboardFilter) (*OpsDashboardOverview, error) {
	r.lastFilter = filter
	if r.err != nil {
		return nil, r.err
	}
	return r.overview, nil
}

func TestComputeRuleMetric_LatencyPercentiles(t *testing.T) {
	start := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	groupID := int64(7)

	tests := []struct {
		name       string
		metricType string
		duration   OpsPercentiles
		err        error
		wantValue  float64
		wantOK     bool
	}{
		{name: "p95 uses the window percentile", metricType: "p95_latency_ms", duration: OpsPercentiles{P95: intPtr(2500), P99: intPtr(9000)}, wantValue: 2500, wantOK: true},
		{name: "p99 uses the window percentile", metricType: "p99_latency_ms", duration: OpsPercentiles{P95: intPtr(1000), P99: intPtr(4200)}, wantValue: 4200, wantOK: true},
		{name: "p95 without samples is not evaluable", metricType: "p95_latency_ms", duration: OpsPercentiles{P99: intPtr(4200)}, wantOK: false},
		{name: "p99 without samples or after latency timeout is not evaluable", metricType: "p99_latency_ms", duration: OpsPercentiles{P95: intPtr(1000)}, wantOK: false},
		{name: "overview query failure is not evaluable", metricType: "p99_latency_ms", err: errors.New("db down"), wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &latencyOverviewRepo{overview: &OpsDashboardOverview{Duration: tt.duration}, err: tt.err}
			svc := &OpsAlertEvaluatorService{opsRepo: repo}

			got, ok := svc.computeRuleMetric(context.Background(), &OpsAlertRule{MetricType: tt.metricType}, nil, start, end, "openai", &groupID)

			require.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				require.InDelta(t, tt.wantValue, got, 0.0001)
			} else {
				require.Zero(t, got, "an unevaluable latency must not be reported as 0 ms")
			}
			require.NotNil(t, repo.lastFilter)
			require.Equal(t, OpsQueryModeRaw, repo.lastFilter.QueryMode, "per-request percentiles, not pre-aggregated maxima")
			require.Equal(t, start, repo.lastFilter.StartTime)
			require.Equal(t, end, repo.lastFilter.EndTime)
			require.Equal(t, "openai", repo.lastFilter.Platform)
			require.Equal(t, &groupID, repo.lastFilter.GroupID)
		})
	}
}

// latencyEvaluatorRepo drives evaluateOnce with an overview and records the heartbeat summary.
type latencyEvaluatorRepo struct {
	*evaluatorCaptureRepo
	overview  *OpsDashboardOverview
	heartbeat string
}

func (r *latencyEvaluatorRepo) GetDashboardOverview(context.Context, *OpsDashboardFilter) (*OpsDashboardOverview, error) {
	return r.overview, nil
}

func (r *latencyEvaluatorRepo) UpsertJobHeartbeat(_ context.Context, input *OpsUpsertJobHeartbeatInput) error {
	if input != nil && input.LastResult != nil {
		r.heartbeat = *input.LastResult
	}
	return nil
}

func newLatencyEvaluatorRepo(duration OpsPercentiles) *latencyEvaluatorRepo {
	return &latencyEvaluatorRepo{
		evaluatorCaptureRepo: &evaluatorCaptureRepo{rules: []*OpsAlertRule{{
			ID: 3, Name: "P99延迟过高", Enabled: true, Severity: "P2",
			MetricType: "p99_latency_ms", Operator: ">", Threshold: 3000,
			WindowMinutes: 5, SustainedMinutes: 1,
		}}},
		overview: &OpsDashboardOverview{Duration: duration},
	}
}

func TestEvaluateOnce_LatencyRuleEvaluatedAndFires(t *testing.T) {
	repo := newLatencyEvaluatorRepo(OpsPercentiles{P95: intPtr(2000), P99: intPtr(5000)})
	svc := NewOpsAlertEvaluatorService(nil, repo, nil, nil, nil, nil)

	svc.evaluateOnce(time.Minute)

	require.Len(t, repo.created, 1)
	ev := repo.created[0]
	require.Equal(t, int64(3), ev.RuleID)
	require.Equal(t, OpsAlertStatusFiring, ev.Status)
	require.NotNil(t, ev.MetricValue)
	require.InDelta(t, 5000.0, *ev.MetricValue, 0.0001)
	require.NotNil(t, ev.ThresholdValue)
	require.InDelta(t, 3000.0, *ev.ThresholdValue, 0.0001)
	require.Equal(t, "p99_latency_ms", ev.Rule.MetricType)
	require.Contains(t, repo.heartbeat, "evaluated=1")
	require.Contains(t, repo.heartbeat, "created=1")
}

func TestEvaluateOnce_LatencyRuleWithoutSamplesIsSkipped(t *testing.T) {
	repo := newLatencyEvaluatorRepo(OpsPercentiles{})
	// A firing event exists: treating "no samples" as 0 ms would wrongly auto-resolve it.
	repo.active = &OpsAlertEvent{ID: 9, RuleID: 3, Status: OpsAlertStatusFiring}
	svc := NewOpsAlertEvaluatorService(nil, repo, nil, nil, nil, nil)

	svc.evaluateOnce(time.Minute)

	require.Empty(t, repo.created)
	require.Zero(t, repo.resolveCalls)
	require.Contains(t, repo.heartbeat, "enabled=1 evaluated=0")
}
