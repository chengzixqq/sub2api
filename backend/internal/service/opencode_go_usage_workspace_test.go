package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeGoUsageWorkspaceReadPermission(t *testing.T) {
	account := openCodeGoUsageAccount(7)
	account.WorkspaceID = 11
	repo := &openCodeGoUsageTestRepo{accounts: map[int64]*Account{7: account}}
	svc := newOpenCodeGoUsageTestService(t, repo, nil, nil)
	monitor := WithScope(context.Background(), VendorScope(11, WorkspacePermissions{MonitorView: true}))
	_, err := svc.GetState(monitor, 7)
	require.NoError(t, err)
	_, err = svc.SetAutoRefresh(monitor, 7, true)
	require.ErrorIs(t, err, domain.ErrWorkspacePermissionDenied)
	_, err = svc.Refresh(monitor, 7)
	require.ErrorIs(t, err, domain.ErrWorkspacePermissionDenied)
	_, err = svc.GetState(WithScope(context.Background(), VendorScope(11, WorkspacePermissions{})), 7)
	require.ErrorIs(t, err, domain.ErrWorkspacePermissionDenied)
	_, err = svc.SetAutoRefresh(WithScope(context.Background(), AdminScope()), 7, true)
	require.NoError(t, err)
}

func TestOpenCodeGoUsageWorkspaceIsolation(t *testing.T) {
	for _, operation := range []string{"read", "write", "refresh"} {
		t.Run(operation, func(t *testing.T) {
			account := openCodeGoUsageAccount(7)
			account.WorkspaceID = 22
			repo := &openCodeGoUsageTestRepo{accounts: map[int64]*Account{7: account}}
			upstream := &openCodeGoUsageHTTPStub{body: []byte(openCodeGoUsageFixture)}
			svc := newOpenCodeGoUsageTestService(t, repo, upstream, nil)
			ctx := WithScope(context.Background(), VendorScope(11, WorkspacePermissions{AccountManage: true, MonitorView: true}))
			var err error
			switch operation {
			case "read":
				_, err = svc.GetState(ctx, 7)
			case "write":
				_, err = svc.SetAutoRefresh(ctx, 7, true)
			case "refresh":
				_, err = svc.Refresh(ctx, 7)
			}
			require.ErrorIs(t, err, ErrAccountNotFound)
			require.Zero(t, upstream.calls.Load())
			require.Empty(t, account.Extra)
		})
	}
}

func TestOpenCodeGoUsageGroupsDoNotCrossWorkspaces(t *testing.T) {
	own := openCodeGoUsageAccount(7)
	own.WorkspaceID = 11
	other := openCodeGoUsageAccount(8)
	other.WorkspaceID = 22
	other.Credentials = mergeMap(nil, own.Credentials)
	other.Extra[OpenCodeGoUsageAutoRefreshExtraKey] = true
	repo := &openCodeGoUsageTestRepo{accounts: map[int64]*Account{7: own, 8: other}}
	svc := newOpenCodeGoUsageTestService(t, repo, nil, nil)
	ctx := WithScope(context.Background(), VendorScope(11, WorkspacePermissions{AccountManage: true}))
	state, err := svc.GetState(ctx, 7)
	require.NoError(t, err)
	require.False(t, state.AutoRefreshEnabled, "another workspace must not supply this account's managed state")
	_, err = svc.SetAutoRefresh(ctx, 7, false)
	require.NoError(t, err)
	require.Equal(t, true, other.Extra[OpenCodeGoUsageAutoRefreshExtraKey])
}
