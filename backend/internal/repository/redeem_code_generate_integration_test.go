//go:build integration

package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// failingRedeemRepo fails the Nth Create inside the generate transaction.
type failingRedeemRepo struct {
	*redeemCodeRepository
	failAt int
	calls  int
}

func (r *failingRedeemRepo) Create(ctx context.Context, code *service.RedeemCode) error {
	r.calls++
	if r.calls == r.failAt {
		return errors.New("insert failed")
	}
	return r.redeemCodeRepository.Create(ctx, code)
}

func newGenerateService(redeemRepo service.RedeemCodeRepository) service.AdminService {
	return service.NewAdminService(
		nil, nil, nil, nil, nil, nil, redeemRepo, nil, nil, nil, nil,
		nil, nil, nil, integrationEntClient, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
}

func batchRowCount(t *testing.T, batch uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM redeem_codes WHERE batch_id = $1", batch).Scan(&n))
	return n
}

func TestGenerateRedeemCodes_BatchIsOneTransaction(t *testing.T) {
	base, ok := NewRedeemCodeRepository(integrationEntClient).(*redeemCodeRepository)
	require.True(t, ok)
	batch := uuid.New()
	admin := int64(5)
	ctx := service.WithScope(context.Background(), service.AdminScope())

	failing := &failingRedeemRepo{redeemCodeRepository: base, failAt: 5}
	_, err := newGenerateService(failing).GenerateRedeemCodes(ctx, &service.GenerateRedeemCodesInput{Count: 8, Type: service.RedeemTypeBalance, Value: 1, BatchID: &batch, CreatedBy: &admin})
	require.Error(t, err)
	require.Equal(t, 0, batchRowCount(t, batch), "a failed batch leaves nothing behind")
}

func TestGenerateRedeemCodes_SameBatchReturnsExistingCodes(t *testing.T) {
	repo := NewRedeemCodeRepository(integrationEntClient)
	svc := newGenerateService(repo)
	batch := uuid.New()
	admin := int64(5)
	ctx := service.WithScope(context.Background(), service.AdminScope())
	in := &service.GenerateRedeemCodesInput{Count: 3, Type: service.RedeemTypeBalance, Value: 10, Notes: "双 11 活动 #A123", BatchID: &batch, CreatedBy: &admin}
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM redeem_codes WHERE batch_id = $1", batch)
	})

	first, err := svc.GenerateRedeemCodes(ctx, in)
	require.NoError(t, err)
	require.Len(t, first, 3)
	for _, c := range first {
		require.Equal(t, "双 11 活动 #A123", c.Notes)
		require.Equal(t, batch, *c.BatchID)
		require.Equal(t, admin, *c.CreatedBy)
	}

	// The idempotency record may be long gone (24h); the batch id alone prevents a second batch.
	again, err := svc.GenerateRedeemCodes(ctx, in)
	require.NoError(t, err)
	require.Equal(t, 3, batchRowCount(t, batch))
	require.Len(t, again, 3)
	for i := range first {
		require.Equal(t, first[i].ID, again[i].ID)
		require.Equal(t, first[i].Code, again[i].Code)
	}

	changed := *in
	changed.Value = 20
	_, err = svc.GenerateRedeemCodes(ctx, &changed)
	require.ErrorIs(t, err, service.ErrRedeemBatchConflict)
	require.Equal(t, 3, batchRowCount(t, batch))
}

func TestGenerateRedeemCodes_WithoutBatchStillWorks(t *testing.T) {
	svc := newGenerateService(NewRedeemCodeRepository(integrationEntClient))
	ctx := service.WithScope(context.Background(), service.AdminScope())

	codes, err := svc.GenerateRedeemCodes(ctx, &service.GenerateRedeemCodesInput{Count: 2, Type: service.RedeemTypeBalance, Value: 1})
	require.NoError(t, err)
	require.Len(t, codes, 2)
	require.Nil(t, codes[0].BatchID)
	t.Cleanup(func() {
		for _, c := range codes {
			_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM redeem_codes WHERE id = $1", c.ID)
		}
	})
}
