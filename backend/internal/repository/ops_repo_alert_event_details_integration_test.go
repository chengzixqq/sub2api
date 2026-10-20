//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Events keep the rule as it was when they fired, report their group's name and the admin who resolved them by hand;
// counts follow the same scope filters as the list.
func TestOpsAlertEvents_RuleSnapshotGroupResolverCounts_RealDatabase(t *testing.T) {
	ctx := context.Background()
	repo, ok := NewOpsRepository(integrationDB).(*opsRepository)
	require.True(t, ok)
	suffix := uuid.NewString()

	var groupID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups (name, platform) VALUES ($1, 'openai') RETURNING id`, "d06-group-"+suffix).Scan(&groupID))
	var adminID int64
	adminName := "d06-admin-" + suffix
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`INSERT INTO users (email, password_hash, role, status, username) VALUES ($1, 'x', 'admin', 'active', $2) RETURNING id`,
		adminName+"@example.com", adminName).Scan(&adminID))
	rule, err := repo.CreateAlertRule(ctx, &service.OpsAlertRule{
		Name: "d06-rule-" + suffix, Enabled: false, Severity: "P1",
		MetricType: "error_rate", Operator: ">", Threshold: 5, WindowMinutes: 5, SustainedMinutes: 3, CooldownMinutes: 10,
		Filters: map[string]any{"group_id": groupID},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM ops_alert_events WHERE rule_id = $1", rule.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM ops_alert_rules WHERE id = $1", rule.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM users WHERE id = $1", adminID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM groups WHERE id = $1", groupID)
	})

	now := time.Now().UTC().Truncate(time.Second)
	dims := map[string]any{"group_id": groupID}
	snapshotEvent, err := repo.CreateAlertEvent(ctx, &service.OpsAlertEvent{
		RuleID: rule.ID, Severity: "P1", Status: service.OpsAlertStatusFiring, Title: "P1: d06", Dimensions: dims, FiredAt: now,
		Rule: &service.OpsAlertEventRule{ID: rule.ID, Name: rule.Name, MetricType: "error_rate", Operator: ">", Threshold: 5, WindowMinutes: 5, SustainedMinutes: 3},
	})
	require.NoError(t, err)
	// An event written before rule snapshots existed.
	var legacyEventID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO ops_alert_events (rule_id, severity, status, title, description, dimensions, fired_at, email_sent, created_at)
VALUES ($1, 'P2', 'firing', 't', '', jsonb_build_object('group_id', $2::BIGINT), $3, false, NOW()) RETURNING id`, rule.ID, groupID, now).Scan(&legacyEventID))

	// The rule is edited after the events fired.
	_, err = integrationDB.ExecContext(ctx, "UPDATE ops_alert_rules SET threshold = 10 WHERE id = $1", rule.ID)
	require.NoError(t, err)

	events, err := repo.ListAlertEvents(ctx, &service.OpsAlertEventFilter{Limit: 500, GroupID: &groupID})
	require.NoError(t, err)
	byID := map[int64]*service.OpsAlertEvent{}
	for _, ev := range events {
		byID[ev.ID] = ev
	}
	require.Len(t, byID, 2)
	require.Equal(t, float64(5), byID[snapshotEvent.ID].Rule.Threshold, "the snapshot keeps the threshold at firing time")
	require.Equal(t, 5, byID[snapshotEvent.ID].Rule.WindowMinutes)
	require.Equal(t, ">", byID[snapshotEvent.ID].Rule.Operator)
	require.False(t, byID[snapshotEvent.ID].Rule.Deleted)
	require.Equal(t, float64(10), byID[legacyEventID].Rule.Threshold, "without a snapshot the current rule is used")
	require.Equal(t, "error_rate", byID[legacyEventID].Rule.MetricType)
	for _, ev := range byID {
		require.NotNil(t, ev.GroupName)
		require.Equal(t, "d06-group-"+suffix, *ev.GroupName)
		require.Nil(t, ev.ResolvedByUserID)
		require.Nil(t, ev.ResolvedByUserName)
	}

	counts, err := repo.CountAlertEventsBySeverity(ctx, &service.OpsAlertEventFilter{Status: service.OpsAlertStatusFiring, GroupID: &groupID})
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"P1": 1, "P2": 1}, counts)

	// Manual resolution records the admin; automatic resolution does not.
	resolvedAt := time.Now().UTC()
	require.NoError(t, repo.UpdateAlertEventStatus(ctx, snapshotEvent.ID, service.OpsAlertStatusManualResolved, &resolvedAt, &adminID))
	require.NoError(t, repo.UpdateAlertEventStatus(ctx, legacyEventID, service.OpsAlertStatusResolved, &resolvedAt, nil))

	manual, err := repo.GetAlertEventByID(ctx, snapshotEvent.ID)
	require.NoError(t, err)
	require.NotNil(t, manual.ResolvedByUserID)
	require.Equal(t, adminID, *manual.ResolvedByUserID)
	require.Equal(t, adminName, *manual.ResolvedByUserName)
	auto, err := repo.GetAlertEventByID(ctx, legacyEventID)
	require.NoError(t, err)
	require.Nil(t, auto.ResolvedByUserID)
	require.Nil(t, auto.ResolvedByUserName)

	counts, err = repo.CountAlertEventsBySeverity(ctx, &service.OpsAlertEventFilter{Status: service.OpsAlertStatusFiring, GroupID: &groupID})
	require.NoError(t, err)
	require.Empty(t, counts)

	// After the rule is deleted the snapshot still describes it; the legacy event has nothing left to show.
	require.NoError(t, repo.DeleteAlertRule(ctx, rule.ID))
	manual, err = repo.GetAlertEventByID(ctx, snapshotEvent.ID)
	require.NoError(t, err)
	require.NotNil(t, manual.Rule)
	require.True(t, manual.Rule.Deleted)
	require.Equal(t, float64(5), manual.Rule.Threshold)
	require.Equal(t, rule.Name, manual.Rule.Name)
	auto, err = repo.GetAlertEventByID(ctx, legacyEventID)
	require.NoError(t, err)
	require.Nil(t, auto.Rule)
}

// Firing events whose rule was deleted or disabled are resolved without a resolver; events of enabled rules, events
// without a rule and events that are already closed are left alone.
func TestOpsAlertEvents_ResolveOrphaned_RealDatabase(t *testing.T) {
	ctx := context.Background()
	repo, ok := NewOpsRepository(integrationDB).(*opsRepository)
	require.True(t, ok)
	suffix := uuid.NewString()

	newRule := func(name string, enabled bool) *service.OpsAlertRule {
		rule, err := repo.CreateAlertRule(ctx, &service.OpsAlertRule{
			Name: name + "-" + suffix, Enabled: enabled, Severity: "P2",
			MetricType: "error_rate", Operator: ">", Threshold: 5, WindowMinutes: 5, SustainedMinutes: 1, CooldownMinutes: 10,
		})
		require.NoError(t, err)
		return rule
	}
	enabled := newRule("d06-orphan-enabled", true)
	disabled := newRule("d06-orphan-disabled", false)
	deleted := newRule("d06-orphan-deleted", true)

	var eventIDs []int64
	t.Cleanup(func() {
		bg := context.Background()
		for _, id := range eventIDs {
			_, _ = integrationDB.ExecContext(bg, "DELETE FROM ops_alert_events WHERE id = $1", id)
		}
		for _, id := range []int64{enabled.ID, disabled.ID, deleted.ID} {
			_, _ = integrationDB.ExecContext(bg, "DELETE FROM ops_alert_rules WHERE id = $1", id)
		}
	})

	firedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	insertEvent := func(ruleID *int64, status string, resolvedBy *int64) int64 {
		var resolvedAt *time.Time
		if status != service.OpsAlertStatusFiring {
			resolvedAt = &firedAt
		}
		var id int64
		require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO ops_alert_events (rule_id, severity, status, title, description, fired_at, resolved_at, resolved_by_user_id, email_sent, created_at)
VALUES ($1, 'P2', $2, 't', '', $3, $4, $5, false, NOW()) RETURNING id`,
			ruleID, status, firedAt, resolvedAt, resolvedBy).Scan(&id))
		eventIDs = append(eventIDs, id)
		return id
	}
	resolverID := int64(424242)
	enabledFiring := insertEvent(&enabled.ID, service.OpsAlertStatusFiring, nil)
	disabledFiring := insertEvent(&disabled.ID, service.OpsAlertStatusFiring, nil)
	disabledManual := insertEvent(&disabled.ID, service.OpsAlertStatusManualResolved, &resolverID)
	deletedFiring := insertEvent(&deleted.ID, service.OpsAlertStatusFiring, nil)
	noRuleFiring := insertEvent(nil, service.OpsAlertStatusFiring, nil)
	require.NoError(t, repo.DeleteAlertRule(ctx, deleted.ID))

	resolvedAt := time.Now().UTC().Truncate(time.Second)
	n, err := repo.ResolveOrphanedAlertEvents(ctx, resolvedAt)
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, int64(2))

	type state struct {
		status     string
		resolvedAt *time.Time
		resolvedBy *int64
	}
	read := func(id int64) state {
		var s state
		require.NoError(t, integrationDB.QueryRowContext(ctx,
			"SELECT status, resolved_at, resolved_by_user_id FROM ops_alert_events WHERE id = $1", id).Scan(&s.status, &s.resolvedAt, &s.resolvedBy))
		return s
	}

	for _, id := range []int64{disabledFiring, deletedFiring} {
		s := read(id)
		require.Equal(t, service.OpsAlertStatusResolved, s.status)
		require.NotNil(t, s.resolvedAt)
		require.True(t, resolvedAt.Equal(*s.resolvedAt))
		require.Nil(t, s.resolvedBy, "closing an orphaned event is automatic, not a manual resolution")
	}
	for _, id := range []int64{enabledFiring, noRuleFiring} {
		s := read(id)
		require.Equal(t, service.OpsAlertStatusFiring, s.status)
		require.Nil(t, s.resolvedAt)
	}
	manual := read(disabledManual)
	require.Equal(t, service.OpsAlertStatusManualResolved, manual.status)
	require.NotNil(t, manual.resolvedBy)
	require.Equal(t, resolverID, *manual.resolvedBy)
	require.True(t, firedAt.Equal(*manual.resolvedAt), "already closed events keep their resolution")

	// The detail read path sees the resolution.
	ev, err := repo.GetAlertEventByID(ctx, deletedFiring)
	require.NoError(t, err)
	require.Equal(t, service.OpsAlertStatusResolved, ev.Status)

	// Idempotent: nothing of ours is left to resolve.
	_, err = repo.ResolveOrphanedAlertEvents(ctx, resolvedAt.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, resolvedAt.Equal(*read(disabledFiring).resolvedAt), "a second pass does not move resolved_at")
	require.Equal(t, service.OpsAlertStatusFiring, read(enabledFiring).status)
}
