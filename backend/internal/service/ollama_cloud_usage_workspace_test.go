package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOllamaCloudUsageWorkspaceIsolation(t *testing.T) {
	for _, operation := range []string{"read", "save", "delete", "write", "refresh"} {
		t.Run(operation, func(t *testing.T) {
			account := ollamaUsageAccount(7)
			account.WorkspaceID = 22
			repo := &ollamaUsageTestRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{7: account}}}
			upstream := &ollamaUsageHTTPStub{body: ollamaUsageFixture(t)}
			svc := newOllamaUsageTestService(t, repo, upstream, nil, true)
			ctx := WithScope(context.Background(), VendorScope(11, WorkspacePermissions{AccountManage: true, MonitorView: true}))
			var err error
			switch operation {
			case "read":
				_, err = svc.GetState(ctx, 7)
			case "save":
				_, err = svc.SaveSession(ctx, 7, "wos-session=fixture")
			case "delete":
				_, err = svc.DeleteSession(ctx, 7)
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

func TestOllamaCloudUsageGroupsDoNotCrossWorkspaces(t *testing.T) {
	own := ollamaUsageAccount(7)
	own.WorkspaceID = 11
	other := ollamaUsageAccount(8)
	other.WorkspaceID = 22
	other.Credentials = mergeMap(nil, own.Credentials)
	other.Extra[OllamaCloudUsageSessionExtraKey] = "cipher:wos-session=other-workspace"
	other.Extra[OllamaCloudUsageAutoRefreshExtraKey] = true
	repo := &ollamaUsageTestRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{7: own, 8: other}}}
	svc := newOllamaUsageTestService(t, repo, nil, nil, true)
	ctx := WithScope(context.Background(), VendorScope(11, WorkspacePermissions{AccountManage: true}))
	state, err := svc.GetState(ctx, 7)
	require.NoError(t, err)
	require.False(t, state.AutoRefreshEnabled)
	_, err = svc.DeleteSession(ctx, 7)
	require.NoError(t, err)
	require.Equal(t, "cipher:wos-session=other-workspace", other.Extra[OllamaCloudUsageSessionExtraKey])
	require.Equal(t, true, other.Extra[OllamaCloudUsageAutoRefreshExtraKey])
}
