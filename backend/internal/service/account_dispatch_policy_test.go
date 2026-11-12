package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountDispatchPolicy_ExplicitGrantAndEmptyReplace(t *testing.T) {
	groupID := int64(9)
	regular := &Account{ID: 1, GroupIDs: []int64{groupID}, DispatchScope: DispatchScopeShared}
	private := &Account{ID: 2, GroupIDs: []int64{groupID}, DispatchScope: DispatchScopeAuthorizedOnly}
	member := &accountDispatchSelection{requestGroupID: groupID, policy: &AccountDispatchPolicy{Mode: AccountDispatchModeReplace}, grants: map[int64]string{2: AccountDispatchOriginMember}}
	ctx := context.WithValue(context.Background(), accountDispatchContextKey{}, member)
	require.False(t, accountDispatchAllows(ctx, regular, &groupID, true))
	require.True(t, accountDispatchAllows(ctx, private, &groupID, true))

	empty := &accountDispatchSelection{requestGroupID: groupID, policy: &AccountDispatchPolicy{Mode: AccountDispatchModeReplace}}
	ctx = context.WithValue(context.Background(), accountDispatchContextKey{}, empty)
	require.False(t, accountDispatchAllows(ctx, regular, &groupID, true))
	require.False(t, accountDispatchAllows(ctx, private, &groupID, true))
}

func TestAccountDispatchPolicy_ExtraGrantSurvivesOutsideMembership(t *testing.T) {
	groupID := int64(9)
	private := &Account{ID: 3, DispatchScope: DispatchScopeAuthorizedOnly}
	selection := &accountDispatchSelection{requestGroupID: groupID, policy: &AccountDispatchPolicy{Mode: AccountDispatchModeAppend}, grants: map[int64]string{3: AccountDispatchOriginExtra}}
	ctx := context.WithValue(context.Background(), accountDispatchContextKey{}, selection)
	require.True(t, accountDispatchAllows(ctx, private, &groupID, false))
	require.False(t, accountDispatchAllows(context.Background(), private, &groupID, true))
}
