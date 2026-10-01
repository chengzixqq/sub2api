package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// loadAccountForUsage checks management scope before returning credentials to a
// quota collector. Background collectors have no management scope and continue
// to enumerate accounts globally, with each refresh group bounded by workspace.
// HTTP handlers must reject missing scope before calling these shared services.
func loadAccountForUsage(ctx context.Context, repo AccountRepository, id int64, write bool) (*Account, error) {
	scope, scoped := ScopeFromContext(ctx)
	if scoped && scope.IsVendor() {
		if write {
			if err := scope.RequireAccountManage(); err != nil {
				return nil, err
			}
		} else if !scope.Perms.AccountManage && !scope.Perms.MonitorView {
			return nil, domain.ErrWorkspacePermissionDenied
		}
	}
	account, err := repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if scoped && (account == nil || !scope.OwnsWorkspaceID(account.WorkspaceID)) {
		return nil, ErrAccountNotFound
	}
	return account, nil
}
