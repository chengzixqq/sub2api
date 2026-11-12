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
	user := mustCreateUser(t, client, &service.User{Email: "dispatch-members-" + uuid.NewString() + "@example.test"})
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM account_groups WHERE group_id IN ($1, $2)", group.ID, other.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM accounts WHERE id IN ($1, $2, $3)", a.ID, b.ID, c.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM users WHERE id = $1", user.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM groups WHERE id IN ($1, $2)", group.ID, other.ID)
	})
	mustBindAccountToGroup(t, client, a.ID, other.ID, 7)
	mustBindAccountToGroup(t, client, c.ID, group.ID, 3)
	var beforePolicyVersion int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO user_group_account_policies (user_id, group_id, mode)
		VALUES ($1, $2, 'replace') RETURNING version
	`, user.ID, group.ID).Scan(&beforePolicyVersion))
	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO user_group_account_grants (user_id, group_id, account_id, origin)
		VALUES ($1, $2, $3, 'member')
	`, user.ID, group.ID, c.ID)
	require.NoError(t, err)

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
	var afterPolicyVersion int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT version FROM user_group_account_policies WHERE user_id = $1 AND group_id = $2
	`, user.ID, group.ID).Scan(&afterPolicyVersion))
	require.Greater(t, afterPolicyVersion, beforePolicyVersion, "membership revocation invalidates stale policy edits")
	var grantCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM user_group_account_grants WHERE user_id = $1 AND group_id = $2 AND account_id = $3
	`, user.ID, group.ID, c.ID).Scan(&grantCount))
	require.Zero(t, grantCount)
	var policyMode string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT mode FROM user_group_account_policies WHERE user_id = $1 AND group_id = $2
	`, user.ID, group.ID).Scan(&policyMode))
	require.Equal(t, "replace", policyMode, "structural revocation preserves an empty replace policy")

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

func TestRemoveFromGroupRevokesOnlyMemberDispatchGrants(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	group := mustCreateGroup(t, client, &service.Group{Name: "remove-member-" + uuid.NewString(), Platform: service.PlatformAnthropic})
	member := mustCreateAccount(t, client, &service.Account{Name: "remove-member", Platform: service.PlatformAnthropic})
	extra := mustCreateAccount(t, client, &service.Account{Name: "remove-extra", Platform: service.PlatformAnthropic})
	user := mustCreateUser(t, client, &service.User{Email: "remove-member-" + uuid.NewString() + "@example.test"})
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM user_group_account_policies WHERE user_id = $1 AND group_id = $2", user.ID, group.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM account_groups WHERE group_id = $1", group.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM accounts WHERE id IN ($1, $2)", member.ID, extra.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM users WHERE id = $1", user.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM groups WHERE id = $1", group.ID)
	})
	mustBindAccountToGroup(t, client, member.ID, group.ID, 1)
	var beforeVersion int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO user_group_account_policies (user_id, group_id, mode)
		VALUES ($1, $2, 'replace') RETURNING version
	`, user.ID, group.ID).Scan(&beforeVersion))
	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO user_group_account_grants (user_id, group_id, account_id, origin)
		VALUES ($1, $2, $3, 'member'), ($1, $2, $4, 'extra')
	`, user.ID, group.ID, member.ID, extra.ID)
	require.NoError(t, err)

	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	require.NoError(t, repo.RemoveFromGroup(ctx, member.ID, group.ID))

	var memberGrantCount, extraGrantCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FILTER (WHERE account_id = $3 AND origin = 'member'),
		       COUNT(*) FILTER (WHERE account_id = $4 AND origin = 'extra')
		FROM user_group_account_grants WHERE user_id = $1 AND group_id = $2
	`, user.ID, group.ID, member.ID, extra.ID).Scan(&memberGrantCount, &extraGrantCount))
	require.Zero(t, memberGrantCount)
	require.Equal(t, 1, extraGrantCount, "group-external grants are independent of member revocation")
	var afterVersion int64
	var mode string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT version, mode FROM user_group_account_policies WHERE user_id = $1 AND group_id = $2
	`, user.ID, group.ID).Scan(&afterVersion, &mode))
	require.Greater(t, afterVersion, beforeVersion, "member revocation invalidates stale policy edits")
	require.Equal(t, "replace", mode)

	mustBindAccountToGroup(t, client, member.ID, group.ID, 1)
	afterRejoin, err := repo.GetAccountDispatchPolicy(ctx, user.ID, group.ID)
	require.NoError(t, err)
	require.NotNil(t, afterRejoin)
	require.Equal(t, service.AccountDispatchModeReplace, afterRejoin.Mode)
	require.Len(t, afterRejoin.Accounts, 1)
	require.Equal(t, extra.ID, afterRejoin.Accounts[0].AccountID, "rejoining a group must not revive an old member grant")
}

func TestRemoveFromGroupSerializesWithDispatchPolicyReplacement(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	group := mustCreateGroup(t, client, &service.Group{Name: "remove-race-" + uuid.NewString(), Platform: service.PlatformAnthropic})
	member := mustCreateAccount(t, client, &service.Account{Name: "remove-race-member", Platform: service.PlatformAnthropic})
	extra := mustCreateAccount(t, client, &service.Account{Name: "remove-race-extra", Platform: service.PlatformAnthropic})
	user := mustCreateUser(t, client, &service.User{Email: "remove-race-" + uuid.NewString() + "@example.test"})
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM user_group_account_policies WHERE user_id = $1 AND group_id = $2", user.ID, group.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM account_groups WHERE group_id = $1", group.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM accounts WHERE id IN ($1, $2)", member.ID, extra.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM users WHERE id = $1", user.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM groups WHERE id = $1", group.ID)
	})
	mustBindAccountToGroup(t, client, member.ID, group.ID, 1)
	var beforeVersion int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO user_group_account_policies (user_id, group_id, mode)
		VALUES ($1, $2, 'replace') RETURNING version
	`, user.ID, group.ID).Scan(&beforeVersion))
	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO user_group_account_grants (user_id, group_id, account_id, origin)
		VALUES ($1, $2, $3, 'member'), ($1, $2, $4, 'extra')
	`, user.ID, group.ID, member.ID, extra.ID)
	require.NoError(t, err)

	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	start := make(chan struct{})
	removeDone := make(chan error, 1)
	putDone := make(chan error, 1)
	go func() {
		<-start
		removeDone <- repo.RemoveFromGroup(ctx, member.ID, group.ID)
	}()
	go func() {
		<-start
		_, putErr := repo.PutAccountDispatchPolicy(ctx, &service.AccountDispatchPolicy{
			UserID: user.ID, GroupID: group.ID, Mode: service.AccountDispatchModeReplace,
			Accounts: []service.AccountDispatchGrant{
				{AccountID: member.ID, Origin: service.AccountDispatchOriginMember},
				{AccountID: extra.ID, Origin: service.AccountDispatchOriginExtra},
			},
		}, beforeVersion)
		putDone <- putErr
	}()
	close(start)

	select {
	case err := <-removeDone:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("RemoveFromGroup deadlocked with policy replacement")
	}
	select {
	case err := <-putDone:
		// Replacement may win before revocation or reject after revocation. In both
		// cases the member grant must be absent once both transactions settle.
		if err != nil {
			require.ErrorContains(t, err, "membership changed while updating dispatch policy")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("policy replacement deadlocked with RemoveFromGroup")
	}

	var memberGrantCount, extraGrantCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FILTER (WHERE account_id = $3 AND origin = 'member'),
		       COUNT(*) FILTER (WHERE account_id = $4 AND origin = 'extra')
		FROM user_group_account_grants WHERE user_id = $1 AND group_id = $2
	`, user.ID, group.ID, member.ID, extra.ID).Scan(&memberGrantCount, &extraGrantCount))
	require.Zero(t, memberGrantCount)
	require.Equal(t, 1, extraGrantCount)
	var afterVersion int64
	var mode string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT version, mode FROM user_group_account_policies WHERE user_id = $1 AND group_id = $2
	`, user.ID, group.ID).Scan(&afterVersion, &mode))
	require.Greater(t, afterVersion, beforeVersion)
	require.Equal(t, "replace", mode)
}

func TestUpdateGroupMembersSerializesWithDispatchPolicyReplacement(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	group := mustCreateGroup(t, client, &service.Group{Name: "bulk-remove-race-" + uuid.NewString(), Platform: service.PlatformAnthropic})
	member := mustCreateAccount(t, client, &service.Account{Name: "bulk-remove-race-member", Platform: service.PlatformAnthropic})
	extra := mustCreateAccount(t, client, &service.Account{Name: "bulk-remove-race-extra", Platform: service.PlatformAnthropic})
	user := mustCreateUser(t, client, &service.User{Email: "bulk-remove-race-" + uuid.NewString() + "@example.test"})
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM user_group_account_policies WHERE user_id = $1 AND group_id = $2", user.ID, group.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM account_groups WHERE group_id = $1", group.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM accounts WHERE id IN ($1, $2)", member.ID, extra.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM users WHERE id = $1", user.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM groups WHERE id = $1", group.ID)
	})
	mustBindAccountToGroup(t, client, member.ID, group.ID, 1)
	var beforeVersion int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO user_group_account_policies (user_id, group_id, mode)
		VALUES ($1, $2, 'replace') RETURNING version
	`, user.ID, group.ID).Scan(&beforeVersion))
	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO user_group_account_grants (user_id, group_id, account_id, origin)
		VALUES ($1, $2, $3, 'member'), ($1, $2, $4, 'extra')
	`, user.ID, group.ID, member.ID, extra.ID)
	require.NoError(t, err)

	accountsRepo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	groupRepo := newGroupRepositoryWithSQL(client, integrationDB)
	start := make(chan struct{})
	removeDone := make(chan error, 1)
	putDone := make(chan error, 1)
	go func() {
		<-start
		_, removeErr := groupRepo.UpdateGroupMembers(ctx, group.ID, nil, []int64{member.ID})
		removeDone <- removeErr
	}()
	go func() {
		<-start
		_, putErr := accountsRepo.PutAccountDispatchPolicy(ctx, &service.AccountDispatchPolicy{
			UserID: user.ID, GroupID: group.ID, Mode: service.AccountDispatchModeReplace,
			Accounts: []service.AccountDispatchGrant{
				{AccountID: member.ID, Origin: service.AccountDispatchOriginMember},
				{AccountID: extra.ID, Origin: service.AccountDispatchOriginExtra},
			},
		}, beforeVersion)
		putDone <- putErr
	}()
	close(start)

	select {
	case err := <-removeDone:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("UpdateGroupMembers deadlocked with policy replacement")
	}
	select {
	case err := <-putDone:
		if err != nil {
			require.ErrorContains(t, err, "membership changed while updating dispatch policy")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("policy replacement deadlocked with UpdateGroupMembers")
	}

	var memberGrantCount, extraGrantCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FILTER (WHERE account_id = $3 AND origin = 'member'),
		       COUNT(*) FILTER (WHERE account_id = $4 AND origin = 'extra')
		FROM user_group_account_grants WHERE user_id = $1 AND group_id = $2
	`, user.ID, group.ID, member.ID, extra.ID).Scan(&memberGrantCount, &extraGrantCount))
	require.Zero(t, memberGrantCount)
	require.Equal(t, 1, extraGrantCount)
	var afterVersion int64
	var mode string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT version, mode FROM user_group_account_policies WHERE user_id = $1 AND group_id = $2
	`, user.ID, group.ID).Scan(&afterVersion, &mode))
	require.Greater(t, afterVersion, beforeVersion)
	require.Equal(t, "replace", mode)
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
