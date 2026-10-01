//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestAdminAccountUpdatesKeepSimpleModeGroupBindingGuard(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		name := "single"
		if bulk {
			name = "bulk"
		}
		t.Run(name, func(t *testing.T) {
			ctx := WithScope(context.Background(), AdminScope())
			repo := &accountRepoStubForBulkUpdate{getByIDAccounts: map[int64]*Account{
				1: {ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
			}}
			svc := &adminServiceImpl{
				cfg:         &config.Config{RunMode: config.RunModeSimple},
				accountRepo: repo,
				groupRepo: &groupRepoStubForAdmin{getByIDByID: map[int64]*Group{
					9: {ID: 9, Platform: PlatformComposite},
				}},
			}
			groups := []int64{9}
			var err error
			if bulk {
				_, err = svc.BulkUpdateAccounts(ctx, &BulkUpdateAccountsInput{
					AccountIDs: []int64{1}, GroupIDs: &groups, SkipMixedChannelCheck: true,
				})
			} else {
				_, err = svc.UpdateAccount(ctx, 1, &UpdateAccountInput{
					GroupIDs: &groups, SkipMixedChannelCheck: true,
				})
			}
			require.Equal(t, "SIMPLE_MODE_GROUP_NOT_BINDABLE", infraerrors.Reason(err))
			require.Empty(t, repo.updatedAccounts)
			require.Empty(t, repo.bulkUpdateIDs)
			require.Empty(t, repo.bindGroupsCalls)
		})
	}
}
