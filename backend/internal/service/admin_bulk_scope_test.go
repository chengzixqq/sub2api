package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestAdminBatchMutationsRequireStationOwnerScope(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
	}{
		{name: "missing scope", ctx: context.Background()},
		{name: "vendor scope", ctx: WithScope(context.Background(), VendorScope(7, WorkspacePermissions{AccountManage: true}))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &adminServiceImpl{}
			affected, err := svc.BatchUpdateConcurrency(tt.ctx, []int64{1}, 3, AdjustmentOperationSet)
			require.Zero(t, affected)
			require.True(t, infraerrors.IsForbidden(err))

			concurrency := 3
			affected, err = svc.BatchUpdateLimits(tt.ctx, []int64{1}, &concurrency, nil)
			require.Zero(t, affected)
			require.True(t, infraerrors.IsForbidden(err))
		})
	}
}

func TestSubscriptionBulkMutationsRequireStationOwnerScope(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
	}{
		{name: "missing scope", ctx: context.Background()},
		{name: "vendor scope", ctx: WithScope(context.Background(), VendorScope(7, WorkspacePermissions{MonitorView: true}))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &SubscriptionService{}
			result, err := svc.BulkSubscriptionAction(tt.ctx, &BulkSubscriptionActionInput{
				SubscriptionIDs: []int64{1}, Action: "revoke",
			})
			require.Nil(t, result)
			require.True(t, infraerrors.IsForbidden(err))

			assigned, err := svc.BulkAssignSubscription(tt.ctx, &BulkAssignSubscriptionInput{
				UserIDs: []int64{1}, GroupID: 1, ValidityDays: 1,
			})
			require.Nil(t, assigned)
			require.True(t, infraerrors.IsForbidden(err))
		})
	}
}
