//go:build unit

package service

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/require"
)

type dispatchFallbackIsolationRepo struct {
	*fallbackCoalesceRepo
}

func (r *dispatchFallbackIsolationRepo) GetAccountDispatchPolicy(_ context.Context, userID, groupID int64) (*AccountDispatchPolicy, error) {
	if userID != 101 {
		return nil, nil
	}
	return &AccountDispatchPolicy{UserID: userID, GroupID: groupID, Mode: AccountDispatchModeReplace,
		Accounts: []AccountDispatchGrant{{AccountID: 8102, Origin: AccountDispatchOriginMember}}}, nil
}

func TestSchedulerFallback_UserDispatchDoesNotAlterSharedPool(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		groupID := int64(81)
		cache := &fallbackCoalesceCache{}
		repo := &dispatchFallbackIsolationRepo{fallbackCoalesceRepo: &fallbackCoalesceRepo{
			release: make(chan struct{}),
			accounts: []Account{
				accountDispatchTestAccount(8101, []int64{groupID}, DispatchScopeShared),
				accountDispatchTestAccount(8102, []int64{groupID}, DispatchScopeAuthorizedOnly),
			},
		}}
		svc := newFallbackCoalesceService(cache, repo)
		var pools [2][]Account
		var errors [2]error
		var workers sync.WaitGroup
		for i := range pools {
			workers.Go(func() {
				pools[i], _, errors[i] = svc.ListSchedulableAccounts(accountDispatchTestContext(int64(101+i)), &groupID, PlatformOpenAI, false)
			})
		}
		synctest.Wait()
		require.EqualValues(t, 1, repo.calls.Load())
		close(repo.release)
		workers.Wait()
		var candidates [2][]Account
		for i := range pools {
			require.NoError(t, errors[i])
			ctx, err := withAccountDispatchSelection(accountDispatchTestContext(int64(101+i)), repo, &groupID)
			require.NoError(t, err)
			candidates[i], err = accountDispatchCandidates(ctx, repo, &groupID, PlatformOpenAI, pools[i], false)
			require.NoError(t, err)
		}
		require.Len(t, candidates[0], 1)
		require.Equal(t, int64(8102), candidates[0][0].ID)
		require.Len(t, candidates[1], 1)
		require.Equal(t, int64(8101), candidates[1][0].ID)
		candidates[0][0].DispatchScope = DispatchScopeShared
		candidates[0][0].GroupIDs = nil
		for _, pool := range pools {
			require.Len(t, pool, 2)
			require.Equal(t, DispatchScopeAuthorizedOnly, pool[1].DispatchScope)
			require.Equal(t, []int64{groupID}, pool[1].GroupIDs)
		}
		require.Len(t, cache.snapshot, 2)
		require.Equal(t, DispatchScopeAuthorizedOnly, cache.snapshot[1].DispatchScope)
	})
}
