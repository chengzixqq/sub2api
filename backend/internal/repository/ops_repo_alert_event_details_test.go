package repository

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Events carry the rule as it was when they fired, the group name and the manual resolver. Without a snapshot the
// current rule is used; a snapshot whose rule is gone is reported as deleted; with neither the rule is null.
func TestListAlertEvents_RuleSnapshotGroupAndResolver(t *testing.T) {
	repo, mock := newOpsSilenceRepo(t)
	fired := time.Date(2026, 10, 18, 1, 29, 0, 0, time.UTC)
	resolved := fired.Add(time.Hour)
	snapshot := []byte(`{"id":41,"name":"标准组错误率","metric_type":"error_rate","operator":">","threshold":5,"window_minutes":5,"sustained_minutes":3}`)
	current := []byte(`{"id":41,"name":"标准组错误率","metric_type":"error_rate","operator":">","threshold":10,"window_minutes":5,"sustained_minutes":3}`)
	currentOnly := []byte(`{"id":42,"name":"CPU","metric_type":"cpu_usage_percent","operator":">=","threshold":80,"window_minutes":1,"sustained_minutes":2}`)

	mock.ExpectQuery(regexp.QuoteMeta("AS current_rule")).
		WillReturnRows(sqlmock.NewRows(opsEventColumnsWithSilence).
			AddRow(int64(1), int64(41), "P1", "manual_resolved", "P1: 标准组错误率", "", 5.2, 5.0, []byte(`{"group_id":2}`), fired, resolved, false, fired, nil, nil,
				snapshot, current, "标准组", int64(1), "admin").
			AddRow(int64(2), int64(42), "P2", "firing", "P2: CPU", "", nil, nil, nil, fired, nil, false, fired, nil, nil,
				nil, currentOnly, nil, nil, nil).
			AddRow(int64(3), int64(43), "P3", "resolved", "P3: gone", "", nil, nil, nil, fired, resolved, false, fired, nil, nil,
				snapshot, nil, nil, nil, nil).
			AddRow(int64(4), int64(44), "P3", "resolved", "P3: legacy", "", nil, nil, nil, fired, resolved, false, fired, nil, nil,
				nil, nil, nil, nil, nil))

	events, err := repo.ListAlertEvents(context.Background(), &service.OpsAlertEventFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, events, 4)

	withSnapshot := events[0]
	require.NotNil(t, withSnapshot.Rule)
	require.Equal(t, service.OpsAlertEventRule{ID: 41, Name: "标准组错误率", MetricType: "error_rate", Operator: ">", Threshold: 5, WindowMinutes: 5, SustainedMinutes: 3}, *withSnapshot.Rule)
	require.Equal(t, "标准组", *withSnapshot.GroupName)
	require.Equal(t, int64(1), *withSnapshot.ResolvedByUserID)
	require.Equal(t, "admin", *withSnapshot.ResolvedByUserName)

	legacyWithRule := events[1]
	require.Equal(t, service.OpsAlertEventRule{ID: 42, Name: "CPU", MetricType: "cpu_usage_percent", Operator: ">=", Threshold: 80, WindowMinutes: 1, SustainedMinutes: 2}, *legacyWithRule.Rule)
	require.Nil(t, legacyWithRule.GroupName)

	ruleDeleted := events[2]
	require.NotNil(t, ruleDeleted.Rule)
	require.True(t, ruleDeleted.Rule.Deleted)
	require.Equal(t, float64(5), ruleDeleted.Rule.Threshold)
	require.Equal(t, "error_rate", ruleDeleted.Rule.MetricType)

	legacyRuleDeleted := events[3]
	require.Nil(t, legacyRuleDeleted.Rule)
	require.Nil(t, legacyRuleDeleted.GroupName)
	require.Nil(t, legacyRuleDeleted.ResolvedByUserID)
	require.Nil(t, legacyRuleDeleted.ResolvedByUserName)

	raw, err := json.Marshal(legacyRuleDeleted)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	for _, key := range []string{"rule", "group_name", "resolved_by_user_id", "resolved_by_user_name"} {
		value, ok := decoded[key]
		require.True(t, ok, "key %s must be present", key)
		require.Nil(t, value, "key %s must be null", key)
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetAlertEventByID_RuleSnapshotGroupAndResolver(t *testing.T) {
	repo, mock := newOpsSilenceRepo(t)
	fired := time.Date(2026, 10, 18, 1, 29, 0, 0, time.UTC)

	mock.ExpectQuery(regexp.QuoteMeta("AS resolved_by_user_name")).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows(opsEventColumnsWithSilence).
			AddRow(int64(1), int64(41), "P1", "manual_resolved", "t", "", nil, nil, []byte(`{"group_id":2}`), fired, fired, false, fired, nil, nil,
				nil, []byte(`{"id":41,"name":"r","metric_type":"error_rate","operator":">","threshold":5,"window_minutes":5,"sustained_minutes":5}`), "标准组", int64(7), "ops"))

	ev, err := repo.GetAlertEventByID(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, "error_rate", ev.Rule.MetricType)
	require.False(t, ev.Rule.Deleted)
	require.Equal(t, "标准组", *ev.GroupName)
	require.Equal(t, int64(7), *ev.ResolvedByUserID)
	require.Equal(t, "ops", *ev.ResolvedByUserName)
	require.NoError(t, mock.ExpectationsWereMet())
}

// ruleSnapshotArg matches the rule_snapshot argument: the seven rule fields as JSON, without deleted.
type ruleSnapshotArg struct{}

func (a ruleSnapshotArg) Match(v driver.Value) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(s), &decoded); err != nil {
		return false
	}
	want := map[string]any{"id": float64(41), "name": "标准组错误率", "metric_type": "error_rate", "operator": ">", "threshold": float64(5), "window_minutes": float64(5), "sustained_minutes": float64(3)}
	return sameJSONMap(want, decoded)
}

func sameJSONMap(want, got map[string]any) bool {
	if len(want) != len(got) {
		return false
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

var opsEventReturningColumns = []string{"id", "rule_id", "severity", "status", "title", "description", "metric_value", "threshold_value", "dimensions", "fired_at", "resolved_at", "email_sent", "created_at"}

func TestCreateAlertEvent_WritesRuleSnapshot(t *testing.T) {
	repo, mock := newOpsSilenceRepo(t)
	fired := time.Date(2026, 10, 18, 1, 29, 0, 0, time.UTC)
	rule := &service.OpsAlertEventRule{ID: 41, Name: "标准组错误率", MetricType: "error_rate", Operator: ">", Threshold: 5, WindowMinutes: 5, SustainedMinutes: 3, Deleted: true}

	anyArgs := make([]driver.Value, 0, 12)
	for i := 0; i < 11; i++ {
		anyArgs = append(anyArgs, sqlmock.AnyArg())
	}
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO ops_alert_events")).
		WithArgs(append(anyArgs, ruleSnapshotArg{})...).
		WillReturnRows(sqlmock.NewRows(opsEventReturningColumns).
			AddRow(int64(9), int64(41), "P1", "firing", "P1: 标准组错误率", "", 5.2, 5.0, nil, fired, nil, false, fired))

	created, err := repo.CreateAlertEvent(context.Background(), &service.OpsAlertEvent{RuleID: 41, Severity: "P1", Status: "firing", FiredAt: fired, Rule: rule})
	require.NoError(t, err)
	require.Equal(t, int64(9), created.ID)
	require.NotNil(t, created.Rule, "the created event keeps its rule summary for notifiers")
	require.Equal(t, "error_rate", created.Rule.MetricType)

	// Without a rule summary the snapshot is NULL.
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO ops_alert_events")).
		WithArgs(append(anyArgs, nil)...).
		WillReturnRows(sqlmock.NewRows(opsEventReturningColumns).
			AddRow(int64(10), int64(41), "P1", "firing", "t", "", nil, nil, nil, fired, nil, false, fired))
	created, err = repo.CreateAlertEvent(context.Background(), &service.OpsAlertEvent{RuleID: 41, Severity: "P1", Status: "firing", FiredAt: fired})
	require.NoError(t, err)
	require.Nil(t, created.Rule)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateAlertEventStatus_WritesResolvedBy(t *testing.T) {
	repo, mock := newOpsSilenceRepo(t)
	resolvedAt := time.Date(2026, 10, 18, 2, 0, 0, 0, time.UTC)
	adminID := int64(7)

	mock.ExpectExec(regexp.QuoteMeta("resolved_by_user_id = $4")).
		WithArgs(int64(5), "manual_resolved", resolvedAt, int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.UpdateAlertEventStatus(context.Background(), 5, "manual_resolved", &resolvedAt, &adminID))

	mock.ExpectExec(regexp.QuoteMeta("resolved_by_user_id = $4")).
		WithArgs(int64(6), "resolved", resolvedAt, nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.UpdateAlertEventStatus(context.Background(), 6, "resolved", &resolvedAt, nil))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCountAlertEventsBySeverity_FiltersScope(t *testing.T) {
	repo, mock := newOpsSilenceRepo(t)
	groupID := int64(2)

	mock.ExpectQuery(regexp.QuoteMeta("GROUP BY 1")).
		WithArgs("firing", "openai", "2").
		WillReturnRows(sqlmock.NewRows([]string{"severity", "count"}).
			AddRow("P1", int64(1)).
			AddRow("P2", int64(3)))

	counts, err := repo.CountAlertEventsBySeverity(context.Background(), &service.OpsAlertEventFilter{Status: "firing", Platform: "openai", GroupID: &groupID})
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"P1": 1, "P2": 3}, counts)
	require.NoError(t, mock.ExpectationsWereMet())
}
