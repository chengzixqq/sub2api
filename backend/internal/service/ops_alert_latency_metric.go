package service

import (
	"context"
	"strings"
	"time"
)

// opsRuleMetricOutcome tells evaluateOnce how a round affects a rule's sustained-breach count.
type opsRuleMetricOutcome int

const (
	opsRuleMetricEvaluated opsRuleMetricOutcome = iota
	// opsRuleMetricUnavailable: nothing to measure (no data, query failed); skip the round and restart the count.
	opsRuleMetricUnavailable
	// opsRuleMetricDeferred: the window had successful requests but the raw latency query timed out;
	// skip the round and keep the count, so sporadic timeouts during an incident do not restart sustained detection.
	opsRuleMetricDeferred
)

func isOpsLatencyMetric(metricType string) bool {
	switch strings.TrimSpace(metricType) {
	case "p95_latency_ms", "p99_latency_ms":
		return true
	default:
		return false
	}
}

// evaluateRuleMetric is computeRuleMetric plus the reason a latency rule could not be measured.
func (s *OpsAlertEvaluatorService) evaluateRuleMetric(
	ctx context.Context,
	rule *OpsAlertRule,
	systemMetrics *OpsSystemMetricsSnapshot,
	start time.Time,
	end time.Time,
	platform string,
	groupID *int64,
) (float64, opsRuleMetricOutcome) {
	if rule != nil && isOpsLatencyMetric(rule.MetricType) {
		overview := s.ruleWindowOverview(ctx, start, end, platform, groupID)
		if overview == nil {
			return 0, opsRuleMetricUnavailable
		}
		return latencyRuleMetric(rule.MetricType, overview)
	}
	if value, ok := s.computeRuleMetric(ctx, rule, systemMetrics, start, end, platform, groupID); ok {
		return value, opsRuleMetricEvaluated
	}
	return 0, opsRuleMetricUnavailable
}

// ruleWindowOverview returns the raw-mode overview of a rule's window, or nil when it cannot be read.
func (s *OpsAlertEvaluatorService) ruleWindowOverview(ctx context.Context, start, end time.Time, platform string, groupID *int64) *OpsDashboardOverview {
	overview, err := s.opsRepo.GetDashboardOverview(ctx, &OpsDashboardFilter{
		StartTime: start,
		EndTime:   end,
		Platform:  platform,
		GroupID:   groupID,
		QueryMode: OpsQueryModeRaw,
	})
	if err != nil {
		return nil
	}
	return overview
}

func latencyRuleMetric(metricType string, overview *OpsDashboardOverview) (float64, opsRuleMetricOutcome) {
	percentile := overview.Duration.P95
	if strings.TrimSpace(metricType) == "p99_latency_ms" {
		percentile = overview.Duration.P99
	}
	if percentile != nil {
		return float64(*percentile), opsRuleMetricEvaluated
	}
	// A missing percentile is never 0 ms. With successful requests in the window the latency query timed out.
	if overview.SuccessCount > 0 {
		return 0, opsRuleMetricDeferred
	}
	return 0, opsRuleMetricUnavailable
}

// holdRuleState records a deferred round: the count is kept, and LastEvaluatedAt moves forward so the
// gap check in updateRuleBreaches does not mistake the skipped round for a pause in evaluation.
func (s *OpsAlertEvaluatorService) holdRuleState(ruleID int64, now time.Time) {
	if ruleID <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if state, ok := s.ruleStates[ruleID]; ok {
		state.LastEvaluatedAt = now
	}
}
