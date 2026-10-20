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

// newSustainedLatencyEvaluator needs three consecutive breaches at a one-minute interval before firing.
func newSustainedLatencyEvaluator() (*latencyEvaluatorRepo, *OpsAlertEvaluatorService) {
	repo := newLatencyEvaluatorRepo(OpsPercentiles{})
	repo.rules[0].SustainedMinutes = 3
	return repo, NewOpsAlertEvaluatorService(nil, repo, nil, nil, nil, nil)
}

func slowLatencyWindow() *OpsDashboardOverview {
	return &OpsDashboardOverview{SuccessCount: 120, Duration: OpsPercentiles{P95: intPtr(2000), P99: intPtr(5000)}}
}

// timedOutLatencyWindow has successful requests but no percentiles: the raw latency query timed out.
func timedOutLatencyWindow() *OpsDashboardOverview {
	return &OpsDashboardOverview{SuccessCount: 120}
}

func TestEvaluateOnce_LatencyQueryTimeoutKeepsSustainedCount(t *testing.T) {
	repo, svc := newSustainedLatencyEvaluator()
	rounds := []*OpsDashboardOverview{slowLatencyWindow(), slowLatencyWindow(), timedOutLatencyWindow(), slowLatencyWindow()}

	for i, overview := range rounds {
		repo.overview = overview
		svc.evaluateOnce(time.Minute)
		if i < len(rounds)-1 {
			require.Empty(t, repo.created, "round %d must not fire yet", i+1)
		}
	}

	require.Len(t, repo.created, 1, "a timed-out round must not restart the sustained-breach count")
	require.Equal(t, int64(3), repo.created[0].RuleID)
	require.Contains(t, repo.heartbeat, "evaluated=1")
}

func TestEvaluateOnce_LatencyQueryTimeoutHoldsStateAndReportsIt(t *testing.T) {
	repo, svc := newSustainedLatencyEvaluator()
	repo.overview = timedOutLatencyWindow()
	repo.active = &OpsAlertEvent{ID: 9, RuleID: 3, Status: OpsAlertStatusFiring}
	svc.ruleStates[3] = &opsAlertRuleState{LastEvaluatedAt: time.Now().UTC().Add(-time.Minute), ConsecutiveBreaches: 2}
	before := time.Now().UTC()

	svc.evaluateOnce(time.Minute)

	state := svc.ruleStates[3]
	require.NotNil(t, state)
	require.Equal(t, 2, state.ConsecutiveBreaches)
	require.False(t, state.LastEvaluatedAt.Before(before),
		"the skipped round moves the clock so the next round's gap check does not see a pause")
	require.Empty(t, repo.created)
	require.Zero(t, repo.resolveCalls, "an unmeasured round never auto-resolves")
	require.Contains(t, repo.heartbeat, "enabled=1 evaluated=0")
	require.Contains(t, repo.heartbeat, "latency_timeouts=1")
}

func TestEvaluateOnce_LatencyWindowWithoutTrafficRestartsSustainedCount(t *testing.T) {
	repo, svc := newSustainedLatencyEvaluator()
	rounds := []*OpsDashboardOverview{slowLatencyWindow(), slowLatencyWindow(), {}, slowLatencyWindow()}

	for _, overview := range rounds {
		repo.overview = overview
		svc.evaluateOnce(time.Minute)
	}

	require.Empty(t, repo.created, "a window without successful requests restarts the count like other overview metrics")
	require.Equal(t, 1, svc.ruleStates[3].ConsecutiveBreaches)
	require.NotContains(t, repo.heartbeat, "latency_timeouts")
}
