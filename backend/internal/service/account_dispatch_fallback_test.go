//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

type accountDispatchFallbackTestRepo struct {
	accountDispatchTestRepo
}

func (r *accountDispatchFallbackTestRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]Account, error) {
	return r.ListSchedulableByGroupIDAndPlatforms(ctx, groupID, []string{platform})
}

func (r *accountDispatchFallbackTestRepo) ListSchedulableByGroupIDAndPlatforms(_ context.Context, groupID int64, platforms []string) ([]Account, error) {
	return r.list(&groupID, platforms), nil
}

func (r *accountDispatchFallbackTestRepo) ListSchedulableUngroupedByPlatforms(_ context.Context, platforms []string) ([]Account, error) {
	return r.list(nil, platforms), nil
}

func (r *accountDispatchFallbackTestRepo) list(groupID *int64, platforms []string) []Account {
	var accounts []Account
	for _, account := range r.accounts {
		member := len(account.GroupIDs) == 0
		if groupID != nil {
			member = accountDispatchHasGroup(account, *groupID)
		}
		for _, platform := range platforms {
			if member && account.Platform == platform && account.IsSchedulable() {
				accounts = append(accounts, *account)
				break
			}
		}
	}
	return accounts
}

func TestAccountDispatchFallback_OpenAIUsesEffectiveGroup(t *testing.T) {
	for _, mode := range []string{"default", AccountDispatchModeAppend, AccountDispatchModeReplace} {
		for _, loadAware := range []bool{false, true} {
			name := mode + "/legacy"
			if loadAware {
				name = mode + "/load_aware"
			}
			t.Run(name, func(t *testing.T) {
				originalID, fallbackID := int64(10), int64(20)
				account := accountDispatchTestAccount(1, []int64{fallbackID}, DispatchScopeShared)
				account.Type, account.Concurrency = AccountTypeAPIKey, 1
				repo := &accountDispatchFallbackTestRepo{accountDispatchTestRepo: accountDispatchTestRepo{accounts: []*Account{&account}}}
				if mode != "default" {
					repo.policy = &AccountDispatchPolicy{Mode: mode}
				}
				ctx := context.WithValue(accountDispatchTestContext(100), ctxkey.AccountDispatchGroupID, originalID)
				ctx = context.WithValue(ctx, ctxkey.Group, &Group{ID: fallbackID, Platform: PlatformOpenAI, Hydrated: true})
				svc := &OpenAIGatewayService{accountRepo: repo, cfg: &config.Config{}}
				var selected *Account
				var err error
				if loadAware {
					var result *AccountSelectionResult
					result, err = svc.SelectAccountWithLoadAwareness(ctx, &fallbackID, "", "", nil)
					if result != nil {
						selected = result.Account
						if result.ReleaseFunc != nil {
							result.ReleaseFunc()
						}
					}
				} else {
					selected, err = svc.SelectAccountForModelWithExclusions(ctx, &fallbackID, "", "", nil)
				}
				if mode == AccountDispatchModeReplace {
					require.Error(t, err)
					require.Nil(t, selected, "fallback must not escape the original replace policy")
				} else {
					require.NoError(t, err)
					require.NotNil(t, selected)
					require.Equal(t, account.ID, selected.ID)
				}
				require.Equal(t, originalID, repo.groupID)
			})
		}
	}
}

func TestAccountDispatchFallback_GeminiUsesUngroupedPool(t *testing.T) {
	for _, mode := range []string{"default", AccountDispatchModeAppend, AccountDispatchModeReplace} {
		t.Run(mode, func(t *testing.T) {
			groupID := int64(10)
			account := accountDispatchTestAccount(1, nil, DispatchScopeShared)
			account.Platform, account.Type = PlatformGemini, AccountTypeAPIKey
			repo := &accountDispatchFallbackTestRepo{accountDispatchTestRepo: accountDispatchTestRepo{accounts: []*Account{&account}}}
			if mode != "default" {
				repo.policy = &AccountDispatchPolicy{Mode: mode}
			}
			ctx := context.WithValue(accountDispatchTestContext(100), ctxkey.AccountDispatchGroupID, groupID)
			ctx = context.WithValue(ctx, ctxkey.ForcePlatform, PlatformGemini)
			ctx = context.WithValue(ctx, ctxkey.Group, &Group{ID: groupID, Platform: PlatformGemini, Hydrated: true})
			svc := &GeminiMessagesCompatService{accountRepo: repo}
			selected, err := svc.SelectAccountForModelWithExclusions(ctx, &groupID, "", "", nil)
			if mode == AccountDispatchModeReplace {
				require.Error(t, err)
				require.Nil(t, selected, "ungrouped fallback must not escape the original replace policy")
			} else {
				require.NoError(t, err)
				require.NotNil(t, selected)
				require.Equal(t, account.ID, selected.ID)
			}
			require.Equal(t, groupID, repo.groupID)
		})
	}
}
