//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAdminGetGroupStats_MatchesLists(t *testing.T) {
	ctx := service.WithScope(context.Background(), service.AdminScope())
	client := testEntClient(t)
	group := mustCreateGroup(t, client, &service.Group{Name: "stats-" + uuid.NewString(), Platform: service.PlatformAnthropic})
	user := mustCreateUser(t, client, &service.User{Email: "stats-" + uuid.NewString() + "@example.com"})
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM api_keys WHERE user_id = $1", user.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM user_subscriptions WHERE user_id = $1", user.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM users WHERE id = $1", user.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM groups WHERE id = $1", group.ID)
	})
	for _, status := range []string{service.StatusActive, service.StatusActive, service.StatusAPIKeyInactive} {
		key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-stats-" + uuid.NewString(), Status: status, Name: "k"})
		_, err := integrationDB.ExecContext(ctx, "UPDATE api_keys SET group_id = $1 WHERE id = $2", group.ID, key.ID)
		require.NoError(t, err)
	}
	deleted := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-stats-del-" + uuid.NewString(), Name: "gone"})
	_, err := integrationDB.ExecContext(ctx, "UPDATE api_keys SET group_id = $1, deleted_at = NOW() WHERE id = $2", group.ID, deleted.ID)
	require.NoError(t, err)

	now := time.Now()
	subscribers := []int64{}
	for _, sub := range []struct {
		status  string
		expires time.Time
	}{{service.SubscriptionStatusActive, now.Add(24 * time.Hour)}, {service.SubscriptionStatusActive, now.Add(-time.Hour)}, {service.SubscriptionStatusExpired, now.Add(-time.Hour)}} {
		subscriber := mustCreateUser(t, client, &service.User{Email: "stats-sub-" + uuid.NewString() + "@example.com"})
		subscribers = append(subscribers, subscriber.ID)
		_, err := client.UserSubscription.Create().SetUserID(subscriber.ID).SetGroupID(group.ID).
			SetStatus(sub.status).SetStartsAt(now.Add(-48 * time.Hour)).SetExpiresAt(sub.expires).Save(ctx)
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		for _, id := range subscribers {
			_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM user_subscriptions WHERE user_id = $1", id)
			_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM users WHERE id = $1", id)
		}
	})

	keys := newAPIKeyRepositoryWithSQL(client, integrationDB)
	groups := newGroupRepositoryWithSQL(client, integrationDB)
	subs := NewUserSubscriptionRepository(client)
	svc := service.NewAdminService(
		nil, nil, groups, nil, nil, keys, nil, nil, nil, nil, nil,
		nil, nil, nil, client, nil, nil, subs, nil, nil, nil, nil, nil, nil, nil,
	)

	stats, err := svc.GetGroupStats(ctx, group.ID)
	require.NoError(t, err)
	_, keyTotal, err := svc.GetGroupAPIKeys(ctx, group.ID, 1, 1)
	require.NoError(t, err)
	require.Equal(t, keyTotal, stats.TotalAPIKeys)
	require.Equal(t, int64(3), stats.TotalAPIKeys, "soft-deleted keys are excluded")
	require.Equal(t, int64(2), stats.ActiveAPIKeys)
	_, subPage, err := subs.ListByGroupID(ctx, group.ID, pagination.PaginationParams{Page: 1, PageSize: 1})
	require.NoError(t, err)
	require.Equal(t, subPage.Total, stats.SubscriptionsTotal)
	require.Equal(t, int64(1), stats.SubscriptionsActive, "active means status active and not yet expired")

	_, err = svc.GetGroupStats(ctx, 999999999)
	require.ErrorIs(t, err, service.ErrGroupNotFound)
}
