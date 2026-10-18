//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsLiveRateCounts_MatchesOverview(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID})
	account := mustCreateAccount(t, client, &service.Account{Name: "live-rates", Platform: service.PlatformAnthropic})
	end := time.Date(2022, 3, 4, 5, 10, 0, 0, time.UTC)
	start := end.Add(-5 * time.Minute)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM usage_logs WHERE user_id = $1", user.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM ops_error_logs WHERE user_id = $1", user.ID)
	})
	for i, at := range []time.Time{start.Add(time.Minute), start.Add(2 * time.Minute), end.Add(time.Minute)} {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO usage_logs
			(user_id, api_key_id, account_id, request_id, model, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens, created_at)
			VALUES ($1,$2,$3,$4,'m',100,50,10,40,$5)`,
			user.ID, key.ID, account.ID, fmt.Sprintf("live-%d-%d", user.ID, i), at)
		require.NoError(t, err)
	}
	for _, row := range []struct {
		status   int
		business bool
		upstream int
	}{{502, false, 502}, {429, true, 429}, {200, false, 529}} {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO ops_error_logs
			(user_id, error_phase, error_type, severity, status_code, upstream_status_code, is_business_limited, error_owner, created_at)
			VALUES ($1,'upstream','upstream_error','error',$2,$3,$4,'provider',$5)`,
			user.ID, row.status, row.upstream, row.business, start.Add(3*time.Minute))
		require.NoError(t, err)
	}

	repo, ok := NewOpsRepository(integrationDB).(*opsRepository)
	require.True(t, ok)
	filter := &service.OpsDashboardFilter{StartTime: start, EndTime: end, QueryMode: service.OpsQueryModeRaw}
	counts, err := repo.GetLiveRateCounts(ctx, filter, true)
	require.NoError(t, err)
	require.Equal(t, int64(2), counts.SuccessCount, "rows outside the window are excluded")
	require.Equal(t, int64(400), counts.TokenConsumed, "tokens include cache creation and cache reads")
	require.Equal(t, int64(2), counts.ErrorCount)
	require.Equal(t, int64(1), counts.ErrorCountSLA, "business-limited errors are excluded from SLA")

	overview, err := repo.GetDashboardOverview(ctx, filter)
	require.NoError(t, err)
	require.Equal(t, overview.SuccessCount, counts.SuccessCount)
	require.Equal(t, overview.TokenConsumed, counts.TokenConsumed)
	require.Equal(t, overview.ErrorCountTotal, counts.ErrorCount)
	require.Equal(t, overview.ErrorCountSLA, counts.ErrorCountSLA)

	withoutErrors, err := repo.GetLiveRateCounts(ctx, filter, false)
	require.NoError(t, err)
	require.Zero(t, withoutErrors.ErrorCount)
	require.Equal(t, int64(2), withoutErrors.SuccessCount)
}
