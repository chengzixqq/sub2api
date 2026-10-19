//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsRepositoryListIngressRejects_RequestCountTotalAndByReason(t *testing.T) {
	ctx := context.Background()
	// user_id has no foreign key; a unique large value isolates this test's rows.
	userID := 9_000_000_000_000 + time.Now().UnixNano()%1_000_000_000
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM ops_ingress_reject_aggregates WHERE user_id = $1", userID)
	})

	repo := NewOpsRepository(integrationDB).(*opsRepository)
	bucket := time.Now().UTC().Truncate(time.Minute)
	row := func(reason, ip string, count int64) *service.OpsIngressRejectAggregate {
		uid := userID
		return &service.OpsIngressRejectAggregate{
			BucketStart: bucket, RejectReason: reason, RouteFamily: "messages", Protocol: "anthropic",
			ClientIP: ip, UserID: &uid, RequestCount: count, FirstSeen: bucket, LastSeen: bucket.Add(time.Second),
		}
	}
	require.NoError(t, repo.BatchUpsertIngressRejects(ctx, []*service.OpsIngressRejectAggregate{
		row("invalid_api_key", "10.0.0.1", 2),
		row("invalid_api_key", "10.0.0.2", 2),
		row("ip_restricted", "10.0.0.3", 5),
	}))

	uid := userID
	list, err := repo.ListIngressRejects(ctx, &service.OpsIngressRejectFilter{UserID: &uid, Page: 1, PageSize: 1})
	require.NoError(t, err)
	require.Len(t, list.Items, 1, "page_size limits items only")
	require.Equal(t, 3, list.Total, "total stays the row count")
	require.Equal(t, int64(9), list.RequestCountTotal)
	require.Equal(t, []service.OpsIngressRejectReasonCount{
		{Reason: "ip_restricted", RequestCount: 5},
		{Reason: "invalid_api_key", RequestCount: 4},
	}, list.ByReason)

	page2, err := repo.ListIngressRejects(ctx, &service.OpsIngressRejectFilter{UserID: &uid, Page: 2, PageSize: 1})
	require.NoError(t, err)
	require.Equal(t, int64(9), page2.RequestCountTotal, "totals ignore pagination")
	require.Equal(t, list.ByReason, page2.ByReason)

	filtered, err := repo.ListIngressRejects(ctx, &service.OpsIngressRejectFilter{UserID: &uid, RejectReason: "invalid_api_key"})
	require.NoError(t, err)
	require.Equal(t, 2, filtered.Total)
	require.Equal(t, int64(4), filtered.RequestCountTotal)
	require.Equal(t, []service.OpsIngressRejectReasonCount{{Reason: "invalid_api_key", RequestCount: 4}}, filtered.ByReason)

	// Ties on request_count are ordered by reason.
	require.NoError(t, repo.BatchUpsertIngressRejects(ctx, []*service.OpsIngressRejectAggregate{row("api_key_disabled", "10.0.0.4", 4)}))
	list, err = repo.ListIngressRejects(ctx, &service.OpsIngressRejectFilter{UserID: &uid})
	require.NoError(t, err)
	require.Equal(t, []service.OpsIngressRejectReasonCount{
		{Reason: "ip_restricted", RequestCount: 5},
		{Reason: "api_key_disabled", RequestCount: 4},
		{Reason: "invalid_api_key", RequestCount: 4},
	}, list.ByReason)
	require.Equal(t, int64(13), list.RequestCountTotal)

	other := userID + 1
	empty, err := repo.ListIngressRejects(ctx, &service.OpsIngressRejectFilter{UserID: &other})
	require.NoError(t, err)
	require.Zero(t, empty.Total)
	require.Zero(t, empty.RequestCountTotal)
	require.NotNil(t, empty.ByReason)
	require.Empty(t, empty.ByReason)
}
