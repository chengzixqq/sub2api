//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestUsageLogRepository_BillingDetailsRoundTrip(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	user := mustCreateUser(t, client, &service.User{Email: "advisor-" + uuid.NewString() + "@example.com"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-advisor-" + uuid.NewString(), Name: "advisor"})
	account := mustCreateAccount(t, client, &service.Account{Name: "advisor"})
	for _, status := range []string{"complete", "partial_usage", "partial_unpriced"} {
		amount := 0.25
		log := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: uuid.NewString(), Model: "executor", InputTokens: 12, TotalCost: amount, ActualCost: amount,
			BillingDetails: &service.BillingDetails{Version: 1, Status: status, Components: []service.BillingComponent{{Role: "advisor", Model: "advisor", InputTokens: 99, TotalCost: &amount}, {Role: "advisor", Model: "unpriced", InputTokens: 7}}}}
		inserted, err := repo.Create(ctx, log)
		require.NoError(t, err)
		require.True(t, inserted)
		saved, err := repo.GetByID(ctx, log.ID)
		require.NoError(t, err)
		require.Equal(t, log.BillingDetails, saved.BillingDetails)
		require.Equal(t, 12, saved.InputTokens)
		inserted, err = repo.Create(ctx, log)
		require.NoError(t, err)
		require.False(t, inserted)
	}
}
