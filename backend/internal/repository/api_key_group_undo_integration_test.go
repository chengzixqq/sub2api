//go:build integration

package repository

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type groupUndoFixture struct {
	ctx       context.Context
	svc       service.AdminService
	users     *userRepository
	keys      *apiKeyRepository
	userID    int64
	plainID   int64
	exclusive int64
	other     int64
}

func newGroupUndoFixture(t *testing.T) *groupUndoFixture {
	t.Helper()
	client := testEntClient(t)
	ctx := service.WithScope(context.Background(), service.AdminScope())
	user := mustCreateUser(t, client, &service.User{Email: "undo-" + uuid.NewString() + "@example.com"})
	plain := mustCreateGroup(t, client, &service.Group{Name: "undo-plain-" + uuid.NewString(), Platform: service.PlatformAnthropic})
	exclusive := mustCreateGroup(t, client, &service.Group{Name: "undo-excl-" + uuid.NewString(), Platform: service.PlatformAnthropic, IsExclusive: true})
	other := mustCreateGroup(t, client, &service.Group{Name: "undo-excl2-" + uuid.NewString(), Platform: service.PlatformAnthropic, IsExclusive: true})
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM api_keys WHERE user_id = $1", user.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM user_allowed_groups WHERE user_id = $1", user.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM groups WHERE id IN ($1, $2, $3)", plain.ID, exclusive.ID, other.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM users WHERE id = $1", user.ID)
	})
	users := newUserRepositoryWithSQL(client, integrationDB)
	keys := newAPIKeyRepositoryWithSQL(client, integrationDB)
	groups := newGroupRepositoryWithSQL(client, integrationDB)
	svc := service.NewAdminService(
		nil, users, groups, nil, nil, keys, nil, nil, nil, nil, nil,
		nil, nil, nil, client, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	return &groupUndoFixture{ctx: ctx, svc: svc, users: users, keys: keys, userID: user.ID, plainID: plain.ID, exclusive: exclusive.ID, other: other.ID}
}

func (f *groupUndoFixture) newKey(t *testing.T, groupID int64) int64 {
	t.Helper()
	key := mustCreateApiKey(t, testEntClient(t), &service.APIKey{UserID: f.userID, Key: "sk-undo-" + uuid.NewString()})
	g := groupID
	_, err := f.svc.AdminUpdateAPIKeyGroupID(f.ctx, key.ID, &g)
	require.NoError(t, err)
	return key.ID
}

func (f *groupUndoFixture) allowed(t *testing.T) []int64 {
	t.Helper()
	u, err := f.users.GetByID(f.ctx, f.userID)
	require.NoError(t, err)
	return u.AllowedGroups
}

func TestAdminAPIKeyGroupUndo_RevokesAutoGrantedAccess(t *testing.T) {
	f := newGroupUndoFixture(t)
	keyID := f.newKey(t, f.plainID)

	excl := f.exclusive
	moved, err := f.svc.AdminUpdateAPIKeyGroupID(f.ctx, keyID, &excl)
	require.NoError(t, err)
	require.True(t, moved.AutoGrantedGroupAccess)
	require.Contains(t, f.allowed(t), f.exclusive)

	back := f.plainID
	undone, err := f.svc.AdminUpdateAPIKeyGroupIDWithOptions(f.ctx, keyID, &back, service.AdminUpdateAPIKeyGroupOptions{RevokeGroupAccessID: f.exclusive})
	require.NoError(t, err)
	require.True(t, undone.RevokedGroupAccess)
	require.Equal(t, f.exclusive, *undone.RevokedGroupID)
	require.Equal(t, f.plainID, *undone.APIKey.GroupID)
	require.NotContains(t, f.allowed(t), f.exclusive)
}

func TestAdminAPIKeyGroupUndo_GroupStillInUseRollsBack(t *testing.T) {
	f := newGroupUndoFixture(t)
	keyID := f.newKey(t, f.exclusive)
	f.newKey(t, f.exclusive) // another key keeps using the exclusive group

	back := f.plainID
	_, err := f.svc.AdminUpdateAPIKeyGroupIDWithOptions(f.ctx, keyID, &back, service.AdminUpdateAPIKeyGroupOptions{RevokeGroupAccessID: f.exclusive})
	require.Error(t, err)
	require.Equal(t, "GROUP_STILL_IN_USE", infraerrors.Reason(err))

	key, err := f.keys.GetByID(f.ctx, keyID)
	require.NoError(t, err)
	require.Equal(t, f.exclusive, *key.GroupID, "the key move is rolled back too")
	require.Contains(t, f.allowed(t), f.exclusive)

	_, err = f.svc.AdminUpdateAPIKeyGroupIDWithOptions(f.ctx, keyID, &back, service.AdminUpdateAPIKeyGroupOptions{RevokeGroupAccessID: f.plainID})
	require.Error(t, err)
	require.Equal(t, "INVALID_REVOKE_GROUP", infraerrors.Reason(err), "cannot revoke the group the key moves to")

	excl := f.other
	_, err = f.svc.AdminUpdateAPIKeyGroupIDWithOptions(f.ctx, keyID, &excl, service.AdminUpdateAPIKeyGroupOptions{RevokeGroupAccessID: f.plainID})
	require.Error(t, err)
	require.Equal(t, "GROUP_NOT_EXCLUSIVE", infraerrors.Reason(err))
}

func TestReplaceUserGroup_ReportsAndExactUndo(t *testing.T) {
	f := newGroupUndoFixture(t)
	// The user already has the new group and one key on it before the replacement.
	alreadyOnNew := f.newKey(t, f.other)
	a := f.newKey(t, f.exclusive)
	b := f.newKey(t, f.exclusive)
	beforeAllowed := f.allowed(t)

	res, err := f.svc.ReplaceUserGroupWithOptions(f.ctx, f.userID, f.exclusive, f.other, service.ReplaceUserGroupOptions{})
	require.NoError(t, err)
	require.Equal(t, int64(2), res.MigratedKeys)
	require.ElementsMatch(t, []int64{a, b}, res.MigratedKeyIDs)
	require.False(t, res.NewGroupNewlyGranted, "the user already had the new group")

	// Exact undo: move only the migrated keys back and keep the pre-existing grant.
	undo, err := f.svc.ReplaceUserGroupWithOptions(f.ctx, f.userID, f.other, f.exclusive, service.ReplaceUserGroupOptions{
		KeyIDs: res.MigratedKeyIDs, KeepOldGroupAccess: true,
	})
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{a, b}, undo.MigratedKeyIDs)

	for id, want := range map[int64]int64{a: f.exclusive, b: f.exclusive, alreadyOnNew: f.other} {
		key, err := f.keys.GetByID(f.ctx, id)
		require.NoError(t, err)
		require.Equal(t, want, *key.GroupID)
	}
	require.ElementsMatch(t, beforeAllowed, f.allowed(t))

	_, err = f.svc.ReplaceUserGroupWithOptions(f.ctx, f.userID, f.exclusive, f.other, service.ReplaceUserGroupOptions{KeyIDs: []int64{alreadyOnNew}})
	require.Error(t, err)
	require.Equal(t, "KEY_NOT_IN_GROUP", infraerrors.Reason(err))
}
