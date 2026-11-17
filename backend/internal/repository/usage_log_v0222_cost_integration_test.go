//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestUsageLog_V0222ModelAndEndpointCostsPreserveProbeDeduplication(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@cost.test"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: uuid.NewString(), Name: "cost"})
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString()})
	start := time.Now().UTC().Add(-time.Hour)
	end := start.Add(2 * time.Hour)
	rate, accountCost := 0.5, 3.0
	endpoint := "/v1beta/models"
	for _, model := range []string{"gemini-3-flash", "gemini-3-pro"} {
		for _, recorded := range []bool{true, false} {
			usage := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: uuid.NewString(), Model: model, RequestedModel: model, InputTokens: 10, OutputTokens: 20, TotalCost: 2, ActualCost: 1.5, AccountStatsCost: &accountCost, AccountRateMultiplier: &rate, ProviderCostRecorded: true, InboundEndpoint: &endpoint, CreatedAt: start.Add(time.Hour)}
			_, err := repo.Create(ctx, usage)
			require.NoError(t, err)
			if !recorded {
				_, err = tx.ExecContext(ctx, "UPDATE usage_logs SET provider_cost_recorded=false WHERE id=$1", usage.ID)
				require.NoError(t, err)
			}
		}
	}
	models, err := repo.GetModelStatsWithUsageFiltersBySource(ctx, start, end, usagestats.UsageLogFilters{AccountID: account.ID}, usagestats.ModelSourceRequested)
	require.NoError(t, err)
	require.Len(t, models, 2)
	for _, model := range models {
		require.EqualValues(t, 2, model.Requests)
		require.InDelta(t, 2, model.Cost, 1e-9)
		require.InDelta(t, 3, model.ActualCost, 1e-9, "customer charges include both requests")
		require.InDelta(t, 1.5, model.AccountCost, 1e-9, "provider cost uses the historical account multiplier once")
	}
	endpoints, err := repo.GetEndpointStatsWithFilters(ctx, start, end, 0, 0, account.ID, 0, "", nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, endpoints, 1)
	require.InDelta(t, 4, endpoints[0].Cost, 1e-9)
	require.InDelta(t, 6, endpoints[0].ActualCost, 1e-9)
	require.InDelta(t, 3, endpoints[0].AccountCost, 1e-9)
	totals, err := repo.GetGeminiUsageTotalsBatch(ctx, []int64{account.ID}, start, end)
	require.NoError(t, err)
	require.InDelta(t, 1.5, totals[account.ID].FlashCost, 1e-9)
	require.InDelta(t, 1.5, totals[account.ID].ProCost, 1e-9)
}
