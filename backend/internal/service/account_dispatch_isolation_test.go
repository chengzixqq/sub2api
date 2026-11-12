//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type accountDispatchIsolationRepo struct {
	accountDispatchFallbackTestRepo
}

func (r *accountDispatchIsolationRepo) GetAccountDispatchPolicy(_ context.Context, userID, groupID int64) (*AccountDispatchPolicy, error) {
	if userID == 100 && groupID == 10 {
		return &AccountDispatchPolicy{
			Mode:     AccountDispatchModeReplace,
			Accounts: []AccountDispatchGrant{{AccountID: 2, Origin: AccountDispatchOriginExtra}},
		}, nil
	}
	return nil, nil
}

func TestAccountDispatchSelection_IsolatesUsersAndNewGroupMembers(t *testing.T) {
	groupID := int64(10)
	shared := accountDispatchTestAccount(1, []int64{groupID}, DispatchScopeShared)
	private := accountDispatchTestAccount(2, []int64{20}, DispatchScopeAuthorizedOnly)
	newMember := accountDispatchTestAccount(3, []int64{groupID}, DispatchScopeShared)
	accounts := []*Account{&shared, &private, &newMember}
	for _, account := range accounts {
		account.Type = AccountTypeAPIKey
		account.Concurrency = 1
	}
	repo := &accountDispatchIsolationRepo{accountDispatchFallbackTestRepo{accountDispatchTestRepo{accounts: accounts}}}
	svc := &OpenAIGatewayService{accountRepo: repo, cfg: &config.Config{}}

	// Both users share the same service and group pool. Only user 100 has the
	// outside grant; adding another ordinary group member cannot widen that list.
	for _, userID := range []int64{100, 200, 100} {
		account, err := svc.SelectAccountForModelWithExclusions(accountDispatchTestContext(userID), &groupID, "", "", nil)
		require.NoError(t, err)
		require.NotNil(t, account)
		if userID == 100 {
			require.Equal(t, private.ID, account.ID)
		} else {
			require.Contains(t, []int64{shared.ID, newMember.ID}, account.ID)
		}
	}
	// An extra grant is scoped to the request group, not to all of the user's keys.
	otherGroupID := int64(20)
	account, err := svc.SelectAccountForModelWithExclusions(accountDispatchTestContext(100), &otherGroupID, "", "", nil)
	require.Error(t, err)
	require.Nil(t, account)
}
