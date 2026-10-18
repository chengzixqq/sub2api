//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestGetProxyWithAccountCount_ScopedCountsAndFallback(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	proxy := mustCreateProxy(t, client, &service.Proxy{Name: "detail-" + uuid.NewString(), Port: 18080})
	backup := mustCreateProxy(t, client, &service.Proxy{Name: "detail-backup-" + uuid.NewString(), Port: 18081})
	onProxy := mustCreateAccount(t, client, &service.Account{Name: "detail-on"})
	moved := mustCreateAccount(t, client, &service.Account{Name: "detail-moved"})
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM accounts WHERE id IN ($1, $2)", onProxy.ID, moved.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM proxies WHERE id IN ($1, $2)", proxy.ID, backup.ID)
	})
	_, err := integrationDB.ExecContext(ctx, "UPDATE accounts SET proxy_id = $1 WHERE id = $2", proxy.ID, onProxy.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET proxy_id = $1, proxy_fallback_origin_id = $2 WHERE id = $3", backup.ID, proxy.ID, moved.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE proxies SET status = 'expired' WHERE id = $1", proxy.ID)
	require.NoError(t, err)

	proxies := newProxyRepositoryWithSQL(client, integrationDB)
	svc := service.NewAdminService(
		nil, nil, nil, nil, proxies, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, client, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	adminCtx := service.WithScope(ctx, service.AdminScope())
	got, err := svc.GetProxyWithAccountCount(adminCtx, proxy.ID)
	require.NoError(t, err)
	require.Equal(t, "expired", got.Status, "expired proxies are readable too")
	require.Equal(t, int64(1), got.AccountCount)
	require.Equal(t, int64(1), got.FallbackAccountCount)

	other := service.WithScope(ctx, service.VendorScope(99999, service.WorkspacePermissions{ProxyManage: true}))
	accounts, fallback, err := proxies.CountProxyAccountsScoped(other, proxy.ID)
	require.NoError(t, err)
	require.Zero(t, accounts, "counts are narrowed to the caller's workspace")
	require.Zero(t, fallback)
}

func TestProxyList_FallbackAccountCount(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	proxy := mustCreateProxy(t, client, &service.Proxy{Name: "list-fallback-" + uuid.NewString(), Port: 18090})
	moved := mustCreateAccount(t, client, &service.Account{Name: "list-moved"})
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM accounts WHERE id = $1", moved.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM proxies WHERE id = $1", proxy.ID)
	})
	_, err := integrationDB.ExecContext(ctx, "UPDATE accounts SET proxy_fallback_origin_id = $1 WHERE id = $2", proxy.ID, moved.ID)
	require.NoError(t, err)

	repo := newProxyRepositoryWithSQL(client, integrationDB)
	active, err := repo.ListActiveWithAccountCount(service.WithScope(ctx, service.AdminScope()))
	require.NoError(t, err)
	found := false
	for _, p := range active {
		if p.ID == proxy.ID {
			found = true
			require.Equal(t, int64(1), p.FallbackAccountCount)
			require.Zero(t, p.AccountCount)
		}
	}
	require.True(t, found, "proxy %d not in %d active proxies", proxy.ID, len(active))
}
