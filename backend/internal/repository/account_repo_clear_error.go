package repository

import (
	"context"

	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.AccountErrorClearerPreservingInactive = (*accountRepository)(nil)

// ClearErrorPreservingInactive clears the error and sets the account active
// only when its status is active or error. The status check and the write are
// one conditional UPDATE, so a concurrent status change cannot slip between a
// read and the write. Accounts in any other status (inactive, legacy disabled)
// are left untouched and no scheduler outbox event or snapshot sync happens;
// the method then reports false.
func (r *accountRepository) ClearErrorPreservingInactive(ctx context.Context, id int64) (bool, error) {
	affected, err := r.client.Account.Update().
		Where(
			dbaccount.IDEQ(id),
			dbaccount.StatusIn(service.StatusActive, service.StatusError),
		).
		SetStatus(service.StatusActive).
		SetErrorMessage("").
		ClearErrorAt().
		Save(ctx)
	if err != nil {
		return false, err
	}
	if affected == 0 {
		return false, nil
	}
	if err := enqueueSchedulerOutbox(ctx, r.sql, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		logger.LegacyPrintf("repository.account", "[SchedulerOutbox] enqueue conditional clear error failed: account=%d err=%v", id, err)
	}
	r.syncSchedulerAccountSnapshot(ctx, id)
	return true, nil
}
