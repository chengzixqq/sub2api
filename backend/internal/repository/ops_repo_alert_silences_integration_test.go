//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsAlertSilences_RealDatabase(t *testing.T) {
	ctx := context.Background()
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE ops_alert_silences RESTART IDENTITY")
	repo, ok := NewOpsRepository(integrationDB).(*opsRepository)
	require.True(t, ok)

	var adminID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`INSERT INTO users (email, password_hash, role, status, username) VALUES ('silence-admin@example.com', 'x', 'admin', 'active', 'ops-admin') RETURNING id`).Scan(&adminID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM users WHERE id = $1", adminID)
	})

	now := time.Now().UTC().Truncate(time.Second)
	groupID := int64(2)
	create := func(platform string, group *int64, until time.Time) *service.OpsAlertSilence {
		s, err := repo.CreateAlertSilence(ctx, &service.OpsAlertSilence{RuleID: 41, Platform: platform, GroupID: group, Until: until, Reason: "test", CreatedBy: &adminID})
		require.NoError(t, err)
		return s
	}

	// A global-rule silence (no platform) is stored and matched.
	longer := create("", &groupID, now.Add(7*24*time.Hour))
	shorter := create("", &groupID, now.Add(24*time.Hour))
	other := create("openai", &groupID, now.Add(7*24*time.Hour)) // different scope, must not be touched
	silenced, err := repo.IsAlertSilenced(ctx, 41, "", &groupID, nil, now)
	require.NoError(t, err)
	require.True(t, silenced)

	// List: active, newest end first, with the creator's name.
	ruleID := int64(41)
	list, err := repo.ListAlertSilences(ctx, &service.OpsAlertSilenceFilter{RuleID: &ruleID, ActiveOnly: true, Limit: 10, Now: now})
	require.NoError(t, err)
	require.Len(t, list, 3)
	for i := 1; i < len(list); i++ {
		require.False(t, list[i].Until.After(list[i-1].Until), "ordered by until descending")
	}
	byID := map[int64]*service.OpsAlertSilence{}
	for _, s := range list {
		require.Equal(t, "ops-admin", s.CreatedByName)
		byID[s.ID] = s
	}
	require.Equal(t, "", byID[longer.ID].Platform)
	require.Equal(t, "openai", byID[other.ID].Platform)

	// Shortening the 24h silence to 1h caps the 7-day one of the same scope too; the openai scope is untouched.
	oneHour := now.Add(time.Hour)
	reason := "改为 1 小时"
	updated, err := repo.UpdateAlertSilenceUntil(ctx, shorter.ID, oneHour, &reason)
	require.NoError(t, err)
	require.True(t, oneHour.Equal(updated.Until))
	require.Equal(t, reason, updated.Reason)
	require.Equal(t, "ops-admin", updated.CreatedByName)
	require.True(t, oneHour.Equal(untilOf(t, longer.ID)), "the longer silence of the same scope must be capped")
	require.True(t, now.Add(7*24*time.Hour).Equal(untilOf(t, other.ID)))

	// Ending revokes every active silence of the scope; rows stay for audit.
	require.NoError(t, repo.EndAlertSilence(ctx, longer.ID, now))
	silenced, err = repo.IsAlertSilenced(ctx, 41, "", &groupID, nil, now)
	require.NoError(t, err)
	require.False(t, silenced)
	stillOther, err := repo.IsAlertSilenced(ctx, 41, "openai", &groupID, nil, now)
	require.NoError(t, err)
	require.True(t, stillOther)
	require.NoError(t, repo.EndAlertSilence(ctx, longer.ID, now), "ending an already ended silence is not an error")

	// Unknown ids.
	_, err = repo.UpdateAlertSilenceUntil(ctx, 999999, oneHour, nil)
	require.Error(t, err)
	require.Error(t, repo.EndAlertSilence(ctx, 999999, now))
}

// Events report the silence that currently covers their scope: the rule, the platform and group from the event's
// dimensions, and the region from the rule's filters.
func TestOpsAlertEvents_CarryActiveSilence_RealDatabase(t *testing.T) {
	ctx := context.Background()
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE ops_alert_silences, ops_alert_events RESTART IDENTITY")
	repo, ok := NewOpsRepository(integrationDB).(*opsRepository)
	require.True(t, ok)

	now := time.Now().UTC().Truncate(time.Second)
	groupID := int64(2)
	insertEvent := func(ruleID int64, dims string) int64 {
		var id int64
		require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO ops_alert_events (rule_id, severity, status, title, description, dimensions, fired_at, email_sent, created_at)
VALUES ($1, 'P1', 'firing', 't', '', $2::jsonb, NOW(), false, NOW()) RETURNING id`, ruleID, dims).Scan(&id))
		return id
	}
	groupEvent := insertEvent(41, `{"group_id":2}`)
	platformEvent := insertEvent(35, `{"platform":"gemini"}`)
	plainEvent := insertEvent(36, `null`)

	shortSilence, err := repo.CreateAlertSilence(ctx, &service.OpsAlertSilence{RuleID: 41, GroupID: &groupID, Until: now.Add(time.Hour)})
	require.NoError(t, err)
	longSilence, err := repo.CreateAlertSilence(ctx, &service.OpsAlertSilence{RuleID: 41, GroupID: &groupID, Until: now.Add(24 * time.Hour)})
	require.NoError(t, err)
	_, err = repo.CreateAlertSilence(ctx, &service.OpsAlertSilence{RuleID: 35, Platform: "openai", Until: now.Add(time.Hour)}) // other platform
	require.NoError(t, err)
	_, err = repo.CreateAlertSilence(ctx, &service.OpsAlertSilence{RuleID: 36, Until: now.Add(-time.Minute)}) // already over
	require.NoError(t, err)

	events, err := repo.ListAlertEvents(ctx, &service.OpsAlertEventFilter{Limit: 10, Status: "firing"})
	require.NoError(t, err)
	byID := map[int64]*service.OpsAlertEvent{}
	for _, ev := range events {
		byID[ev.ID] = ev
	}
	require.Len(t, byID, 3)
	require.NotNil(t, byID[groupEvent].Silence)
	require.Equal(t, longSilence.ID, byID[groupEvent].Silence.ID, "the latest-ending silence wins")
	require.Nil(t, byID[platformEvent].Silence, "a silence on another platform does not cover the event")
	require.Nil(t, byID[plainEvent].Silence, "an expired silence does not count")

	ev, err := repo.GetAlertEventByID(ctx, groupEvent)
	require.NoError(t, err)
	require.NotNil(t, ev.Silence)
	require.Equal(t, longSilence.ID, ev.Silence.ID)

	require.NoError(t, repo.EndAlertSilence(ctx, shortSilence.ID, time.Now().UTC()))
	ev, err = repo.GetAlertEventByID(ctx, groupEvent)
	require.NoError(t, err)
	require.Nil(t, ev.Silence, "after revoking, the event is no longer silenced")
}

func untilOf(t *testing.T, id int64) time.Time {
	t.Helper()
	var until time.Time
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT until FROM ops_alert_silences WHERE id = $1", id).Scan(&until))
	return until.UTC()
}
