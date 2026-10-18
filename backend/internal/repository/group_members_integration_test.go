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

func TestUpdateGroupMembers(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	group := mustCreateGroup(t, client, &service.Group{Name: "members-" + uuid.NewString(), Platform: service.PlatformAnthropic})
	other := mustCreateGroup(t, client, &service.Group{Name: "members-other-" + uuid.NewString(), Platform: service.PlatformAnthropic})
	a := mustCreateAccount(t, client, &service.Account{Name: "m-a"})
	b := mustCreateAccount(t, client, &service.Account{Name: "m-b"})
	c := mustCreateAccount(t, client, &service.Account{Name: "m-c"})
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM account_groups WHERE group_id IN ($1, $2)", group.ID, other.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM accounts WHERE id IN ($1, $2, $3)", a.ID, b.ID, c.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM groups WHERE id IN ($1, $2)", group.ID, other.ID)
	})
	mustBindAccountToGroup(t, client, a.ID, other.ID, 7)
	mustBindAccountToGroup(t, client, c.ID, group.ID, 3)

	repo := newGroupRepositoryWithSQL(client, integrationDB)
	res, err := repo.UpdateGroupMembers(ctx, group.ID, []service.GroupMemberAdd{{AccountID: a.ID}, {AccountID: b.ID, Priority: 2}}, []int64{c.ID})
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{a.ID, b.ID}, res.Added)
	require.Len(t, res.Removed, 1)
	require.Equal(t, c.ID, res.Removed[0].AccountID)
	require.Equal(t, 3, res.Removed[0].Priority, "the removed priority is returned for undo")

	var priority int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT priority FROM account_groups WHERE account_id = $1 AND group_id = $2", a.ID, other.ID).Scan(&priority))
	require.Equal(t, 7, priority, "membership in other groups is untouched")
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT priority FROM account_groups WHERE account_id = $1 AND group_id = $2", a.ID, group.ID).Scan(&priority))
	require.Equal(t, 50, priority, "default priority")
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT priority FROM account_groups WHERE account_id = $1 AND group_id = $2", b.ID, group.ID).Scan(&priority))
	require.Equal(t, 2, priority)

	// Replaying the same change is a no-op.
	again, err := repo.UpdateGroupMembers(ctx, group.ID, []service.GroupMemberAdd{{AccountID: a.ID}, {AccountID: b.ID, Priority: 2}}, []int64{c.ID})
	require.NoError(t, err)
	require.Empty(t, again.Added)
	require.Empty(t, again.Removed)

	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET deleted_at = $1 WHERE id = $2", time.Now(), group.ID)
	require.NoError(t, err)
	_, err = repo.UpdateGroupMembers(ctx, group.ID, []service.GroupMemberAdd{{AccountID: c.ID}}, nil)
	require.ErrorIs(t, err, service.ErrGroupNotFound)
}

func TestAdminUpdateGroupMembers_PerAccountResults(t *testing.T) {
	ctx := service.WithScope(context.Background(), service.AdminScope())
	client := testEntClient(t)
	group := mustCreateGroup(t, client, &service.Group{Name: "svc-members-" + uuid.NewString(), Platform: service.PlatformAnthropic})
	claude := mustCreateAccount(t, client, &service.Account{Name: "svc-claude", Platform: service.PlatformAnthropic})
	antigravity := mustCreateAccount(t, client, &service.Account{Name: "svc-ag", Platform: service.PlatformAntigravity})
	newcomer := mustCreateAccount(t, client, &service.Account{Name: "svc-new", Platform: service.PlatformAnthropic})
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM account_groups WHERE group_id = $1", group.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM accounts WHERE id IN ($1, $2, $3)", claude.ID, antigravity.ID, newcomer.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM groups WHERE id = $1", group.ID)
	})
	mustBindAccountToGroup(t, client, claude.ID, group.ID, 1)

	accounts := newAccountRepositoryWithSQL(client, integrationDB, nil)
	groups := newGroupRepositoryWithSQL(client, integrationDB)
	svc := service.NewAdminService(
		nil, nil, groups, accounts, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, client, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)

	res, err := svc.UpdateGroupMembers(ctx, group.ID, service.UpdateGroupMembersInput{
		Add: []service.GroupMemberAdd{{AccountID: newcomer.ID}, {AccountID: antigravity.ID}, {AccountID: claude.ID}, {AccountID: 999999999}},
	})
	require.NoError(t, err)
	require.Equal(t, 1, res.Added)
	require.Equal(t, 1, res.Unchanged, "already a member")
	require.Equal(t, 2, res.Failed)
	byID := map[int64]service.GroupMemberResult{}
	for _, r := range res.Results {
		byID[r.AccountID] = r
	}
	require.Equal(t, "MIXED_CHANNEL_RISK", byID[antigravity.ID].Reason)
	require.Equal(t, "Antigravity", byID[antigravity.ID].Details["current_platform"])
	require.Equal(t, "ACCOUNT_NOT_FOUND", byID[999999999].Reason)
	require.NotNil(t, res.Counts)
	require.Equal(t, int64(2), res.Counts.AccountCount)

	confirmed, err := svc.UpdateGroupMembers(ctx, group.ID, service.UpdateGroupMembersInput{
		Add: []service.GroupMemberAdd{{AccountID: antigravity.ID}}, Remove: []int64{newcomer.ID}, SkipMixedChannelCheck: true,
	})
	require.NoError(t, err)
	require.Equal(t, 1, confirmed.Added)
	require.Equal(t, 1, confirmed.Removed)
	for _, r := range confirmed.Results {
		if r.AccountID == newcomer.ID {
			require.Equal(t, 50, *r.Priority, "removed priority is returned for undo")
		}
	}

	_, err = svc.UpdateGroupMembers(ctx, group.ID, service.UpdateGroupMembersInput{Add: []service.GroupMemberAdd{{AccountID: claude.ID}}, Remove: []int64{claude.ID}})
	require.Error(t, err)
	_, err = svc.UpdateGroupMembers(ctx, group.ID, service.UpdateGroupMembersInput{})
	require.Error(t, err)
}
