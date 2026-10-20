//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// evaluatorCaptureRepo drives evaluateOnce with one rule and records the events it creates and resolves.
type evaluatorCaptureRepo struct {
	OpsRepository
	rules        []*OpsAlertRule
	cpu          float64
	active       *OpsAlertEvent
	created      []*OpsAlertEvent
	resolvedID   int64
	resolvedTo   string
	resolvedAt   *time.Time
	resolvedBy   *int64
	resolveCalls int
	orphanCalls  int
	orphanAt     time.Time
	orphanCount  int64
}

func (r *evaluatorCaptureRepo) ResolveOrphanedAlertEvents(_ context.Context, resolvedAt time.Time) (int64, error) {
	r.orphanCalls++
	r.orphanAt = resolvedAt
	return r.orphanCount, nil
}

func (r *evaluatorCaptureRepo) ListAlertRules(context.Context) ([]*OpsAlertRule, error) {
	return r.rules, nil
}

func (r *evaluatorCaptureRepo) GetLatestSystemMetrics(context.Context, int) (*OpsSystemMetricsSnapshot, error) {
	cpu := r.cpu
	return &OpsSystemMetricsSnapshot{CPUUsagePercent: &cpu}, nil
}

func (r *evaluatorCaptureRepo) GetActiveAlertEvent(context.Context, int64) (*OpsAlertEvent, error) {
	return r.active, nil
}

func (r *evaluatorCaptureRepo) GetLatestAlertEvent(context.Context, int64) (*OpsAlertEvent, error) {
	return nil, nil
}

func (r *evaluatorCaptureRepo) CreateAlertEvent(_ context.Context, event *OpsAlertEvent) (*OpsAlertEvent, error) {
	r.created = append(r.created, event)
	return event, nil
}

func (r *evaluatorCaptureRepo) UpdateAlertEventStatus(_ context.Context, eventID int64, status string, resolvedAt *time.Time, resolvedBy *int64) error {
	r.resolveCalls++
	r.resolvedID, r.resolvedTo, r.resolvedAt, r.resolvedBy = eventID, status, resolvedAt, resolvedBy
	return nil
}

func (r *evaluatorCaptureRepo) UpsertJobHeartbeat(context.Context, *OpsUpsertJobHeartbeatInput) error {
	return nil
}

func TestOpsAlertEvaluator_FiredEventCarriesRuleSnapshot(t *testing.T) {
	repo := &evaluatorCaptureRepo{
		rules: []*OpsAlertRule{{
			ID: 41, Name: " CPU 过高 ", Enabled: true, Severity: "P1",
			MetricType: "cpu_usage_percent", Operator: ">", Threshold: 80,
			WindowMinutes: 5, SustainedMinutes: 1,
		}},
		cpu: 95,
	}
	svc := NewOpsAlertEvaluatorService(nil, repo, nil, nil, nil, nil)

	svc.evaluateOnce(time.Minute)
	require.Len(t, repo.created, 1)
	require.Equal(t, &OpsAlertEventRule{ID: 41, Name: "CPU 过高", MetricType: "cpu_usage_percent", Operator: ">", Threshold: 80, WindowMinutes: 5, SustainedMinutes: 1}, repo.created[0].Rule)

	// Recovery: the evaluator resolves the active event without a resolver.
	repo.cpu = 10
	repo.active = &OpsAlertEvent{ID: 9, RuleID: 41, Status: OpsAlertStatusFiring}
	svc.evaluateOnce(time.Minute)
	require.Equal(t, 1, repo.resolveCalls)
	require.Equal(t, int64(9), repo.resolvedID)
	require.Equal(t, OpsAlertStatusResolved, repo.resolvedTo)
	require.NotNil(t, repo.resolvedAt)
	require.Nil(t, repo.resolvedBy)
}

// Events of disabled or deleted rules are never evaluated again, so each round closes them; otherwise they stay firing
// and keep the alert counts up forever.
func TestOpsAlertEvaluator_ResolvesEventsOfDisabledOrDeletedRules(t *testing.T) {
	repo := &evaluatorCaptureRepo{
		rules: []*OpsAlertRule{{
			ID: 41, Name: "CPU 过高", Enabled: false, Severity: "P1",
			MetricType: "cpu_usage_percent", Operator: ">", Threshold: 80, WindowMinutes: 5, SustainedMinutes: 1,
		}},
		cpu:         95,
		orphanCount: 2,
	}
	heartbeat := &evaluatorHeartbeatRepo{evaluatorCaptureRepo: repo}
	svc := NewOpsAlertEvaluatorService(nil, heartbeat, nil, nil, nil, nil)

	before := time.Now().UTC()
	svc.evaluateOnce(time.Minute)

	require.Equal(t, 1, repo.orphanCalls)
	require.False(t, repo.orphanAt.Before(before.Truncate(time.Second)))
	require.Empty(t, repo.created, "a disabled rule does not fire")
	require.Contains(t, heartbeat.result, "resolved=2")
}

type evaluatorHeartbeatRepo struct {
	*evaluatorCaptureRepo
	result string
}

func (r *evaluatorHeartbeatRepo) UpsertJobHeartbeat(_ context.Context, input *OpsUpsertJobHeartbeatInput) error {
	if input != nil && input.LastResult != nil {
		r.result = *input.LastResult
	}
	return nil
}

func TestOpsAlertEventRuleFromRule_NormalizesWindow(t *testing.T) {
	require.Nil(t, opsAlertEventRuleFromRule(nil, 5))
	got := opsAlertEventRuleFromRule(&OpsAlertRule{ID: 3, Name: "n", MetricType: "error_rate", Operator: "<", Threshold: 1.5, WindowMinutes: 0, SustainedMinutes: 2}, 1)
	require.Equal(t, 1, got.WindowMinutes, "the snapshot uses the evaluated window, like the description")
	require.False(t, got.Deleted)
}

func TestOpsService_UpdateAlertEventStatus_DropsResolverUnlessManual(t *testing.T) {
	var gotBy *int64
	var calls int
	repo := &opsRepoMock{
		UpdateAlertEventStatusFn: func(_ context.Context, _ int64, _ string, _ *time.Time, resolvedBy *int64) error {
			calls++
			gotBy = resolvedBy
			return nil
		},
	}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	now := time.Now().UTC()
	adminID := int64(7)

	require.NoError(t, svc.UpdateAlertEventStatus(context.Background(), 1, OpsAlertStatusResolved, &now, &adminID))
	require.Nil(t, gotBy, "only manual resolution records a resolver")

	require.NoError(t, svc.UpdateAlertEventStatus(context.Background(), 1, OpsAlertStatusManualResolved, &now, &adminID))
	require.NotNil(t, gotBy)
	require.Equal(t, int64(7), *gotBy)

	zero := int64(0)
	require.NoError(t, svc.UpdateAlertEventStatus(context.Background(), 1, OpsAlertStatusManualResolved, &now, &zero))
	require.Nil(t, gotBy, "a non-positive user id is not recorded")

	err := svc.UpdateAlertEventStatus(context.Background(), 1, "bogus", &now, &adminID)
	require.Error(t, err)
	require.Equal(t, "INVALID_STATUS", infraerrors.Reason(err))
	require.Equal(t, 3, calls)
}

func TestOpsService_GetFiringAlertEventCounts_FillsAllSeverities(t *testing.T) {
	var gotFilter *OpsAlertEventFilter
	counts := map[string]int64{"P1": 1, "P2": 1}
	repo := &opsRepoMock{
		CountAlertEventsBySeverityFn: func(_ context.Context, filter *OpsAlertEventFilter) (map[string]int64, error) {
			gotFilter = filter
			return counts, nil
		},
	}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	groupID := int64(2)

	out, err := svc.GetFiringAlertEventCounts(context.Background(), " openai ", &groupID)
	require.NoError(t, err)
	require.Equal(t, int64(2), out.Firing)
	require.Equal(t, map[string]int64{"P0": 0, "P1": 1, "P2": 1, "P3": 0}, out.BySeverity)
	require.Equal(t, OpsAlertStatusFiring, gotFilter.Status)
	require.Equal(t, "openai", gotFilter.Platform)
	require.Equal(t, int64(2), *gotFilter.GroupID)

	raw, err := json.Marshal(out)
	require.NoError(t, err)
	require.JSONEq(t, `{"firing":2,"by_severity":{"P0":0,"P1":1,"P2":1,"P3":0}}`, string(raw))

	// Other severities are reported too, so the keys always add up to firing.
	counts = map[string]int64{"P0": 2, "warning": 1}
	out, err = svc.GetFiringAlertEventCounts(context.Background(), "", nil)
	require.NoError(t, err)
	require.Equal(t, int64(3), out.Firing)
	require.Equal(t, map[string]int64{"P0": 2, "P1": 0, "P2": 0, "P3": 0, "warning": 1}, out.BySeverity)
	require.Equal(t, "", gotFilter.Platform)
	require.Nil(t, gotFilter.GroupID)
}

func TestOpsService_GetFiringAlertEventCounts_NoRepo(t *testing.T) {
	svc := NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	out, err := svc.GetFiringAlertEventCounts(context.Background(), "", nil)
	require.NoError(t, err)
	require.Equal(t, int64(0), out.Firing)
	require.Equal(t, map[string]int64{"P0": 0, "P1": 0, "P2": 0, "P3": 0}, out.BySeverity)
}
