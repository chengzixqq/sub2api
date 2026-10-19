//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type proxyFallbackFixture struct {
	ctx     context.Context
	svc     service.AdminService
	proxy   *service.Proxy
	backup  *service.Proxy
	movedA  *service.Account
	movedB  *service.Account
	proxies *proxyRepository
}

func newProxyFallbackFixture(t *testing.T) *proxyFallbackFixture {
	t.Helper()
	ctx := service.WithScope(context.Background(), service.AdminScope())
	client := testEntClient(t)
	proxy := mustCreateProxy(t, client, &service.Proxy{Name: "fb-" + uuid.NewString(), Port: 19080})
	backup := mustCreateProxy(t, client, &service.Proxy{Name: "fb-backup-" + uuid.NewString(), Port: 19081})
	a := mustCreateAccount(t, client, &service.Account{Name: "fb-a"})
	b := mustCreateAccount(t, client, &service.Account{Name: "fb-b"})
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM accounts WHERE id IN ($1, $2)", a.ID, b.ID)
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM proxies WHERE id IN ($1, $2)", proxy.ID, backup.ID)
	})
	// Expired proxy: one account moved to the backup proxy, one to direct.
	_, err := integrationDB.ExecContext(ctx, "UPDATE proxies SET status = 'expired', expires_at = $1 WHERE id = $2", time.Now().Add(-time.Hour), proxy.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET proxy_id = $1, proxy_fallback_origin_id = $2 WHERE id = $3", backup.ID, proxy.ID, a.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET proxy_id = NULL, proxy_fallback_origin_id = $1 WHERE id = $2", proxy.ID, b.ID)
	require.NoError(t, err)

	proxies := newProxyRepositoryWithSQL(client, integrationDB)
	accounts := newAccountRepositoryWithSQL(client, integrationDB, nil)
	svc := service.NewAdminService(
		nil, nil, nil, accounts, proxies, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, client, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	return &proxyFallbackFixture{ctx: ctx, svc: svc, proxy: proxy, backup: backup, movedA: a, movedB: b, proxies: proxies}
}

func (f *proxyFallbackFixture) renew(t *testing.T) {
	t.Helper()
	_, err := integrationDB.ExecContext(f.ctx, "UPDATE proxies SET status = 'active', expires_at = $1 WHERE id = $2", time.Now().Add(24*time.Hour), f.proxy.ID)
	require.NoError(t, err)
}

func TestProxyFallback_DeleteBlockedByFallbackAccounts(t *testing.T) {
	f := newProxyFallbackFixture(t)
	err := f.svc.DeleteProxy(f.ctx, f.proxy.ID)
	require.Error(t, err)
	require.Equal(t, "PROXY_IN_USE", infraerrors.Reason(err))
	require.Equal(t, "2", infraerrors.FromError(err).Metadata["fallback_account_count"])
}

func TestProxyFallback_RevertRequiresUsableOrigin(t *testing.T) {
	f := newProxyFallbackFixture(t)
	err := f.svc.RevertAccountProxyFallback(f.ctx, f.movedA.ID)
	require.Error(t, err)
	require.Equal(t, "PROXY_FALLBACK_ORIGIN_UNAVAILABLE", infraerrors.Reason(err))

	f.renew(t)
	require.NoError(t, f.svc.RevertAccountProxyFallback(f.ctx, f.movedA.ID))
	var proxyID int64
	require.NoError(t, integrationDB.QueryRowContext(f.ctx, "SELECT proxy_id FROM accounts WHERE id = $1", f.movedA.ID).Scan(&proxyID))
	require.Equal(t, f.proxy.ID, proxyID)

	_, err = integrationDB.ExecContext(f.ctx, "UPDATE proxies SET deleted_at = NOW() WHERE id = $1", f.proxy.ID)
	require.NoError(t, err)
	err = f.svc.RevertAccountProxyFallback(f.ctx, f.movedB.ID)
	require.Equal(t, "PROXY_FALLBACK_ORIGIN_UNAVAILABLE", infraerrors.Reason(err), "a deleted origin is unavailable")
}
