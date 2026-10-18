//go:build integration

package repository

import (
	"context"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRedeemCodeBatch_RealDatabase(t *testing.T) {
	ctx := context.Background()
	repo, ok := NewRedeemCodeRepository(integrationEntClient).(*redeemCodeRepository)
	require.True(t, ok)
	batch := uuid.New()
	admin := int64(5)

	tx, err := integrationEntClient.Tx(ctx)
	require.NoError(t, err)
	txCtx := dbent.NewTxContext(ctx, tx)
	require.NoError(t, repo.LockBatch(txCtx, batch))
	for i := 0; i < 3; i++ {
		code, genErr := service.GenerateRedeemCode()
		require.NoError(t, genErr)
		require.NoError(t, repo.Create(txCtx, &service.RedeemCode{Code: code, Type: service.RedeemTypeBalance, Value: 10, Status: service.StatusUnused, Notes: "双 11", BatchID: &batch, CreatedBy: &admin}))
	}
	require.NoError(t, tx.Commit())
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM redeem_codes WHERE batch_id = $1", batch)
	})

	got, err := repo.ListByBatchID(ctx, batch)
	require.NoError(t, err)
	require.Len(t, got, 3)
	for _, c := range got {
		require.Equal(t, batch, *c.BatchID)
		require.Equal(t, admin, *c.CreatedBy)
		require.Len(t, c.Code, 32, "the real code value, not a redacted one")
	}
	require.True(t, got[0].ID < got[1].ID && got[1].ID < got[2].ID, "ordered by id")

	_, res, err := repo.ListWithFilters(ctx, defaultPage(), service.RedeemCodeListFilter{BatchID: &batch})
	require.NoError(t, err)
	require.Equal(t, int64(3), res.Total)
	_, res, err = repo.ListWithFilters(ctx, defaultPage(), service.RedeemCodeListFilter{BatchID: &batch, Notes: "双"})
	require.NoError(t, err)
	require.Equal(t, int64(3), res.Total)

	empty, err := repo.ListByBatchID(ctx, uuid.New())
	require.NoError(t, err)
	require.Empty(t, empty)
}

func defaultPage() pagination.PaginationParams {
	return pagination.PaginationParams{Page: 1, PageSize: 50}
}
