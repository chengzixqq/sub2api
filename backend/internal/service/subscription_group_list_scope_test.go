package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/stretchr/testify/require"
)

// The route layer already denies GET /admin/groups/:id/subscriptions to vendors; the service refuses as well so a
// future caller cannot leak subscribers by reusing the method (the same double guard as GetGroupAPIKeys).
func TestListGroupSubscriptions_VendorScopeIsRefused(t *testing.T) {
	svc := &SubscriptionService{} // the repository must never be reached

	ctx := WithScope(context.Background(), VendorScope(3, WorkspacePermissions{GroupOps: true, GroupBilling: true, MonitorView: true}))
	subs, pag, err := svc.ListGroupSubscriptions(ctx, 1, 1, 20)
	require.ErrorIs(t, err, domain.ErrWorkspaceScopeViolation)
	require.Nil(t, subs)
	require.Nil(t, pag)

	// A context without a scope is treated as a vendor too (default deny).
	_, _, err = svc.ListGroupSubscriptions(context.Background(), 1, 1, 20)
	require.ErrorIs(t, err, domain.ErrWorkspaceScopeViolation)
}
