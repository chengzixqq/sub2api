package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestBuildAccountForCreate_TypeSafePreservesWorkspaceScope(t *testing.T) {
	ctx := WithScope(context.Background(), VendorScopeWithSettlementRange(
		7, WorkspacePermissions{AccountManage: true}, ratePtr(0.05), ratePtr(0.06),
	))
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		t.Run(accountType, func(t *testing.T) {
			account, err := buildAccountForCreate(ctx, &CreateAccountInput{
				Name: "typesafe", Platform: PlatformTypeSafe, Type: accountType,
				Credentials: map[string]any{"api_key": "test-key"}, RateMultiplier: ratePtr(0.055),
			}, nil)
			if accountType != AccountTypeAPIKey {
				require.EqualError(t, err, "typesafe accounts only support apikey credentials")
				require.Nil(t, account)
				return
			}
			require.NoError(t, err)
			require.Equal(t, int64(7), account.WorkspaceID)
			require.Equal(t, 0.055, *account.RateMultiplier)
		})
	}

	_, err := buildAccountForCreate(ctx, &CreateAccountInput{
		Platform: PlatformTypeSafe, Type: AccountTypeAPIKey, RateMultiplier: ratePtr(1),
	}, nil)
	require.ErrorIs(t, err, domain.ErrWorkspaceSettlementRateOutOfRange)
}

type typeSafeWorkspaceAccountRepo struct {
	AccountRepository
	account *Account
	reads   int
}

func (r *typeSafeWorkspaceAccountRepo) GetByIDScoped(context.Context, int64) (*Account, error) {
	r.reads++
	return r.account, nil
}

func TestUpdateAccount_TypeSafeRejectsOAuthAndOutOfScopeRate(t *testing.T) {
	repo := &typeSafeWorkspaceAccountRepo{account: &Account{
		ID: 1, WorkspaceID: 7, Platform: PlatformTypeSafe, Type: AccountTypeAPIKey,
	}}
	svc := &adminServiceImpl{accountRepo: repo}
	ctx := WithScope(context.Background(), VendorScopeWithSettlementRange(
		7, WorkspacePermissions{AccountManage: true}, ratePtr(0.05), ratePtr(0.06),
	))
	_, err := svc.UpdateAccount(ctx, 1, &UpdateAccountInput{RateMultiplier: ratePtr(1)})
	require.ErrorIs(t, err, domain.ErrWorkspaceSettlementRateOutOfRange)
	require.Zero(t, repo.reads, "rate scope rejection must happen before reading the account")

	_, err = svc.UpdateAccount(ctx, 1, &UpdateAccountInput{Type: AccountTypeOAuth})
	require.EqualError(t, err, "typesafe accounts only support apikey credentials")
	require.Equal(t, 1, repo.reads, "the account must be read through the workspace-scoped repository method")
	require.Equal(t, AccountTypeAPIKey, repo.account.Type)
}
