package repository

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.ProxyDetailCounter = (*proxyRepository)(nil)

// CountProxyAccountsScoped counts accounts on proxyID and accounts moved off
// it by expiry fallback (proxy_fallback_origin_id), narrowed to the caller's
// workspace like GetAccountCountsForProxies.
func (r *proxyRepository) CountProxyAccountsScoped(ctx context.Context, proxyID int64) (accounts int64, fallback int64, err error) {
	query := `SELECT
			COUNT(*) FILTER (WHERE proxy_id = $1),
			COUNT(*) FILTER (WHERE proxy_fallback_origin_id = $1)
		FROM accounts
		WHERE deleted_at IS NULL AND (proxy_id = $1 OR proxy_fallback_origin_id = $1)`
	args := []any{proxyID}
	if workspaceID := service.ScopeFromContextOrDeny(ctx).WorkspaceFilter(); workspaceID != nil {
		query += " AND workspace_id = $2"
		args = append(args, *workspaceID)
	}
	err = scanSingleRow(ctx, r.sql, query, args, &accounts, &fallback)
	return accounts, fallback, err
}

// getFallbackAccountCountsForProxies maps proxy id to the number of accounts
// moved off it by expiry fallback, narrowed like GetAccountCountsForProxies.
func (r *proxyRepository) getFallbackAccountCountsForProxies(ctx context.Context) (counts map[int64]int64, err error) {
	query := "SELECT proxy_fallback_origin_id, COUNT(*) FROM accounts WHERE proxy_fallback_origin_id IS NOT NULL AND deleted_at IS NULL"
	var args []any
	if workspaceID := service.ScopeFromContextOrDeny(ctx).WorkspaceFilter(); workspaceID != nil {
		query += " AND workspace_id = $1"
		args = append(args, *workspaceID)
	}
	query += " GROUP BY proxy_fallback_origin_id"
	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			counts = nil
		}
	}()
	counts = make(map[int64]int64)
	for rows.Next() {
		var proxyID, count int64
		if err = rows.Scan(&proxyID, &count); err != nil {
			return nil, err
		}
		counts[proxyID] = count
	}
	return counts, rows.Err()
}

var _ service.ProxyFallbackCounter = (*proxyRepository)(nil)

// CountAccountsByFallbackOriginProxyID counts live accounts whose fallback
// origin is proxyID (unscoped: used for delete protection).
func (r *proxyRepository) CountAccountsByFallbackOriginProxyID(ctx context.Context, proxyID int64) (count int64, err error) {
	err = scanSingleRow(ctx, r.sql, "SELECT COUNT(*) FROM accounts WHERE proxy_fallback_origin_id = $1 AND deleted_at IS NULL", []any{proxyID}, &count)
	return count, err
}
