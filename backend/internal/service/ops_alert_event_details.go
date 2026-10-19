package service

import (
	"context"
	"strings"
)

// opsAlertEventCountSeverities are always present in alert event counts, even at zero.
var opsAlertEventCountSeverities = []string{"P0", "P1", "P2", "P3"}

// opsAlertEventRuleFromRule is the rule summary stored with a new event. windowMinutes is the evaluated window
// (normalized like the event description).
func opsAlertEventRuleFromRule(rule *OpsAlertRule, windowMinutes int) *OpsAlertEventRule {
	if rule == nil {
		return nil
	}
	return &OpsAlertEventRule{
		ID:               rule.ID,
		Name:             strings.TrimSpace(rule.Name),
		MetricType:       strings.TrimSpace(rule.MetricType),
		Operator:         strings.TrimSpace(rule.Operator),
		Threshold:        rule.Threshold,
		WindowMinutes:    windowMinutes,
		SustainedMinutes: rule.SustainedMinutes,
	}
}

// GetFiringAlertEventCounts counts firing alert events by severity, optionally scoped to a platform and group (the
// same dimension filters as the event list). Silenced firing events are included.
func (s *OpsService) GetFiringAlertEventCounts(ctx context.Context, platform string, groupID *int64) (*OpsAlertEventCounts, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	out := &OpsAlertEventCounts{BySeverity: make(map[string]int64, len(opsAlertEventCountSeverities))}
	for _, severity := range opsAlertEventCountSeverities {
		out.BySeverity[severity] = 0
	}
	if s.opsRepo == nil {
		return out, nil
	}
	counts, err := s.opsRepo.CountAlertEventsBySeverity(ctx, &OpsAlertEventFilter{
		Status:   OpsAlertStatusFiring,
		Platform: strings.TrimSpace(platform),
		GroupID:  groupID,
	})
	if err != nil {
		return nil, err
	}
	for severity, n := range counts {
		out.BySeverity[strings.TrimSpace(severity)] += n
		out.Firing += n
	}
	return out, nil
}
