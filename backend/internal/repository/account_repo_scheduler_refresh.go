package repository

import (
	"context"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *accountRepository) RefreshAccountSchedulerSnapshots(ctx context.Context, accountID int64) error {
	if r == nil || r.schedulerCache == nil || accountID <= 0 {
		return nil
	}
	ctx = dbent.WithoutTx(ctx)

	ids := []int64{accountID}
	shadows, err := r.ListShadowsByParent(ctx, accountID)
	if err != nil {
		return err
	}
	for _, shadow := range shadows {
		if shadow != nil && shadow.ID > 0 {
			ids = append(ids, shadow.ID)
		}
	}

	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		account, err := r.GetByID(ctx, id)
		if err != nil {
			return err
		}
		if err := r.schedulerCache.SetAccount(ctx, account); err != nil {
			return err
		}
	}
	return nil
}

var _ service.AccountSchedulerSnapshotRefresher = (*accountRepository)(nil)
