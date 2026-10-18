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
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM users WHERE id = $1", adminID) })

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

func untilOf(t *testing.T, id int64) time.Time {
	t.Helper()
	var until time.Time
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT until FROM ops_alert_silences WHERE id = $1", id).Scan(&until))
	return until.UTC()
}
