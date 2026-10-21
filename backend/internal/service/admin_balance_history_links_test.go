//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type linkReaderAdjustmentRepo struct {
	AdminUserAdjustmentRepository
	calls     int
	gotUserID int64
	gotIDs    []int64
	links     map[int64]AdminAdjustmentLink
}

func (r *linkReaderAdjustmentRepo) LinksByRedeemCodeIDs(_ context.Context, userID int64, ids []int64) (map[int64]AdminAdjustmentLink, error) {
	r.calls++
	r.gotUserID = userID
	r.gotIDs = append([]int64(nil), ids...)
	return r.links, nil
}

// E10: balance-history asks the ledger only for admin adjustment rows; redeemed codes and affiliate transfers (negative
// synthetic ids) never carry before/after values.
func TestAttachAdjustmentLinks_OnlyAdminRows(t *testing.T) {
	before, after := "1.00000000", "3.00000000"
	repo := &linkReaderAdjustmentRepo{links: map[int64]AdminAdjustmentLink{
		3: {AdjustmentID: 9, BeforeValue: &before, AfterValue: &after},
	}}
	svc := &adminServiceImpl{adjustmentRepo: repo}
	codes := []RedeemCode{
		{ID: 1, Type: RedeemTypeBalance},
		{ID: 3, Type: AdjustmentTypeAdminBalance},
		{ID: 4, Type: AdjustmentTypeAdminConcurrency},
		{ID: -5, Type: RedeemTypeAffiliateBalance},
	}

	require.NoError(t, svc.attachAdjustmentLinks(context.Background(), 7, codes))

	require.Equal(t, 1, repo.calls)
	require.Equal(t, int64(7), repo.gotUserID)
	require.Equal(t, []int64{3, 4}, repo.gotIDs)
	require.Nil(t, codes[0].Adjustment)
	require.NotNil(t, codes[1].Adjustment)
	require.Equal(t, int64(9), codes[1].Adjustment.AdjustmentID)
	require.Equal(t, &after, codes[1].Adjustment.AfterValue)
	require.Nil(t, codes[2].Adjustment, "an admin row without a ledger match stays null")
	require.Nil(t, codes[3].Adjustment)
}

func TestAttachAdjustmentLinks_SkipsWhenNothingToLink(t *testing.T) {
	repo := &linkReaderAdjustmentRepo{}
	svc := &adminServiceImpl{adjustmentRepo: repo}
	codes := []RedeemCode{{ID: 1, Type: RedeemTypeBalance}, {ID: -2, Type: RedeemTypeAffiliateBalance}}
	require.NoError(t, svc.attachAdjustmentLinks(context.Background(), 7, codes))
	require.Zero(t, repo.calls, "no admin rows, no ledger query")

	// A ledger repository without the reader (older test doubles) leaves the rows untouched.
	plain := &adminServiceImpl{adjustmentRepo: struct{ AdminUserAdjustmentRepository }{}}
	require.NoError(t, plain.attachAdjustmentLinks(context.Background(), 7, []RedeemCode{{ID: 3, Type: AdjustmentTypeAdminBalance}}))
	require.NoError(t, (&adminServiceImpl{}).attachAdjustmentLinks(context.Background(), 7, []RedeemCode{{ID: 3, Type: AdjustmentTypeAdminBalance}}))
}

func TestGetUserRechargeBreakdown_NilWithoutReader(t *testing.T) {
	breakdown, err := (&adminServiceImpl{}).GetUserRechargeBreakdown(context.Background(), 7)
	require.NoError(t, err)
	require.Nil(t, breakdown)
}
