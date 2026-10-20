package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// scopeIDsAccountRepo resolves IDs from a fixed table spread over workspaces.
type scopeIDsAccountRepo struct {
	AccountRepository
	byID  map[int64]*Account
	calls int
}

func (r *scopeIDsAccountRepo) GetByIDs(_ context.Context, ids []int64) ([]*Account, error) {
	r.calls++
	out := make([]*Account, 0, len(ids))
	for _, id := range ids {
		if account, ok := r.byID[id]; ok {
			out = append(out, account)
		}
	}
	return out, nil
}

// TestAdminFilterAccountIDsByScopeDropsOtherWorkspaces covers the two steps the
// batch read endpoints (runtime/batch, today-stats/batch) rely on: IDs are
// narrowed before any cache key or counter read, and the loaded accounts are
// re-checked against the caller's workspace.
func TestAdminFilterAccountIDsByScopeDropsOtherWorkspaces(t *testing.T) {
	repo := &scopeIDsAccountRepo{byID: map[int64]*Account{
		1: {ID: 1, WorkspaceID: 7},
		2: {ID: 2, WorkspaceID: 8},
		3: {ID: 3, WorkspaceID: 0},
		4: {ID: 4, WorkspaceID: 7},
	}}
	svc := &adminServiceImpl{accountRepo: repo}
	requested := []int64{1, 2, 3, 4, 99}

	vendor := WithScope(context.Background(), VendorScope(7, WorkspacePermissions{MonitorView: true}))
	ids, err := svc.FilterAccountIDsByScope(vendor, requested)
	require.NoError(t, err)
	require.Equal(t, []int64{1, 4}, ids)
	accounts, err := svc.GetAccountsByIDs(vendor, requested)
	require.NoError(t, err)
	require.Len(t, accounts, 2)
	for _, account := range accounts {
		require.Equal(t, int64(7), account.WorkspaceID)
	}

	repo.calls = 0
	admin := WithScope(context.Background(), AdminScope())
	ids, err = svc.FilterAccountIDsByScope(admin, requested)
	require.NoError(t, err)
	require.Equal(t, requested, ids, "the owner keeps the list as sent")
	require.Zero(t, repo.calls, "no lookup is needed to scope the owner")
	accounts, err = svc.GetAccountsByIDs(admin, requested)
	require.NoError(t, err)
	require.Len(t, accounts, 4)

	// A context without a scope is denied, never treated as unrestricted.
	ids, err = svc.FilterAccountIDsByScope(context.Background(), requested)
	require.NoError(t, err)
	require.NotNil(t, ids)
	require.Empty(t, ids)
	accounts, err = svc.GetAccountsByIDs(context.Background(), requested)
	require.NoError(t, err)
	require.Empty(t, accounts)
}
