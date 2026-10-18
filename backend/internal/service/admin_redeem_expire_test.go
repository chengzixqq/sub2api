//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type expireRedeemRepoStub struct {
	*redeemRepoStub
	code    *RedeemCode
	updated *RedeemCode
}

func (s *expireRedeemRepoStub) GetByID(context.Context, int64) (*RedeemCode, error) {
	clone := *s.code
	return &clone, nil
}

func (s *expireRedeemRepoStub) Update(_ context.Context, code *RedeemCode) error {
	clone := *code
	s.updated = &clone
	return nil
}

func TestExpireRedeemCode_RefusesRedeemedCode(t *testing.T) {
	userID := int64(7)
	for name, code := range map[string]*RedeemCode{
		"status used":           {ID: 1, Status: StatusUsed, UsedBy: &userID},
		"used_by set (expired)": {ID: 1, Status: StatusExpired, UsedBy: &userID},
	} {
		t.Run(name, func(t *testing.T) {
			repo := &expireRedeemRepoStub{redeemRepoStub: &redeemRepoStub{}, code: code}
			svc := &adminServiceImpl{redeemCodeRepo: repo}

			_, err := svc.ExpireRedeemCode(context.Background(), 1)
			require.ErrorIs(t, err, ErrRedeemCodeUsed)
			require.Nil(t, repo.updated, "a redeemed code is not modified")
		})
	}
}

func TestExpireRedeemCode_ExpiresUnusedCode(t *testing.T) {
	repo := &expireRedeemRepoStub{redeemRepoStub: &redeemRepoStub{}, code: &RedeemCode{ID: 1, Status: StatusUnused}}
	svc := &adminServiceImpl{redeemCodeRepo: repo}

	got, err := svc.ExpireRedeemCode(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, StatusExpired, got.Status)
	require.Equal(t, StatusExpired, repo.updated.Status)
}
