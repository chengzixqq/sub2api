package repository

import (
	"context"
	"errors"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

const defaultGroupMemberPriority = 50

var _ service.GroupMemberRepository = (*groupRepository)(nil)

// UpdateGroupMembers adds and removes account memberships of one group in a
// single transaction, touching only (account_id, group_id) rows: the accounts
// themselves and their memberships in other groups are left alone. Already
// present adds and absent removes are no-ops, so a replay is safe.
func (r *groupRepository) UpdateGroupMembers(ctx context.Context, groupID int64, adds []service.GroupMemberAdd, removeIDs []int64) (*service.GroupMemberChange, error) {
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return nil, err
	}
	exec := sqlExecutor(r.client)
	if tx != nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
	}
	if err := lockLiveGroups(ctx, exec, []int64{groupID}); err != nil {
		return nil, err
	}

	change := &service.GroupMemberChange{Added: []int64{}, Removed: []service.GroupMemberRemoved{}}
	if len(adds) > 0 {
		ids := make([]int64, len(adds))
		priorities := make([]int64, len(adds))
		for i, add := range adds {
			ids[i] = add.AccountID
			priorities[i] = int64(add.Priority)
			if add.Priority <= 0 {
				priorities[i] = defaultGroupMemberPriority
			}
		}
		added, err := scanInt64Rows(exec.QueryContext(ctx, `
			INSERT INTO account_groups (account_id, group_id, priority, created_at)
			SELECT m.account_id, $1, m.priority, NOW()
			FROM unnest($2::bigint[], $3::bigint[]) AS m(account_id, priority)
			ON CONFLICT (account_id, group_id) DO NOTHING
			RETURNING account_id`, groupID, pq.Array(ids), pq.Array(priorities)))
		if err != nil {
			return nil, err
		}
		change.Added = added
	}
	if len(removeIDs) > 0 {
		rows, err := exec.QueryContext(ctx, `
			DELETE FROM account_groups WHERE group_id = $1 AND account_id = ANY($2)
			RETURNING account_id, priority`, groupID, pq.Array(removeIDs))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var removed service.GroupMemberRemoved
			if err := rows.Scan(&removed.AccountID, &removed.Priority); err != nil {
				_ = rows.Close()
				return nil, err
			}
			change.Removed = append(change.Removed, removed)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}

	if len(change.Added) > 0 || len(change.Removed) > 0 {
		if err := enqueueSchedulerOutbox(ctx, r.sql, service.SchedulerOutboxEventGroupChanged, nil, &groupID, nil); err != nil {
			logger.LegacyPrintf("repository.group", "[SchedulerOutbox] enqueue group members change failed: group=%d err=%v", groupID, err)
		}
		touched := append([]int64{}, change.Added...)
		for _, removed := range change.Removed {
			touched = append(touched, removed.AccountID)
		}
		payload := map[string]any{"group_ids": []int64{groupID}}
		for _, accountID := range touched {
			id := accountID
			if err := enqueueSchedulerOutbox(ctx, r.sql, service.SchedulerOutboxEventAccountGroupsChanged, &id, nil, payload); err != nil {
				logger.LegacyPrintf("repository.group", "[SchedulerOutbox] enqueue account groups change failed: account=%d err=%v", id, err)
			}
		}
	}
	return change, nil
}

func scanInt64Rows(rows interface {
	Next() bool
	Scan(...any) error
	Close() error
	Err() error
}, queryErr error) ([]int64, error) {
	if queryErr != nil {
		return nil, queryErr
	}
	out := []int64{}
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return out, rows.Err()
}
