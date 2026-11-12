package repository

import (
	"context"
	"sort"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

func sortedUniqueIDs(ids []int64) []int64 {
	out := append([]int64(nil), ids...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	if len(out) < 2 {
		return out
	}
	unique := out[:1]
	for _, id := range out[1:] {
		if id != unique[len(unique)-1] {
			unique = append(unique, id)
		}
	}
	return unique
}

// Lock account rows before group and membership rows in all account-group
// mutations. Policy edits use the same order before locking policy parents.
func lockAccountRows(ctx context.Context, exec sqlExecutor, accountIDs []int64) error {
	ids := sortedUniqueIDs(accountIDs)
	if len(ids) == 0 {
		return nil
	}
	rows, err := exec.QueryContext(ctx, `
		SELECT id FROM accounts WHERE id = ANY($1::bigint[])
		ORDER BY id FOR NO KEY UPDATE
	`, pq.Array(ids))
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	return rows.Close()
}

func requireLiveAccountRows(ctx context.Context, exec sqlExecutor, accountIDs []int64) error {
	ids := sortedUniqueIDs(accountIDs)
	if len(ids) == 0 {
		return nil
	}
	rows, err := exec.QueryContext(ctx, `
		SELECT id FROM accounts
		WHERE id = ANY($1::bigint[]) AND deleted_at IS NULL
		ORDER BY id
	`, pq.Array(ids))
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if count != len(ids) {
		return service.ErrAccountNotFound
	}
	return nil
}

func lockAccountGroupRows(ctx context.Context, exec sqlExecutor, groupID int64, accountIDs []int64) error {
	ids := sortedUniqueIDs(accountIDs)
	if len(ids) == 0 {
		return nil
	}
	rows, err := exec.QueryContext(ctx, `
		SELECT account_id FROM account_groups
		WHERE group_id = $1 AND account_id = ANY($2::bigint[])
		ORDER BY account_id FOR UPDATE
	`, groupID, pq.Array(ids))
	if err != nil {
		return err
	}
	for rows.Next() {
		var accountID int64
		if err := rows.Scan(&accountID); err != nil {
			_ = rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	return rows.Close()
}

func accountGroupIDsForAccount(ctx context.Context, exec sqlExecutor, accountID int64, forUpdate bool) ([]int64, error) {
	query := `SELECT group_id FROM account_groups WHERE account_id = $1 ORDER BY group_id`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	rows, err := exec.QueryContext(ctx, query, accountID)
	if err != nil {
		return nil, err
	}
	groupIDs := make([]int64, 0)
	for rows.Next() {
		var groupID int64
		if err := rows.Scan(&groupID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		groupIDs = append(groupIDs, groupID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return groupIDs, nil
}

func accountIDsForGroup(ctx context.Context, exec sqlExecutor, groupID int64) ([]int64, error) {
	rows, err := exec.QueryContext(ctx, `
		SELECT account_id FROM account_groups WHERE group_id = $1 ORDER BY account_id
	`, groupID)
	if err != nil {
		return nil, err
	}
	accountIDs := make([]int64, 0)
	for rows.Next() {
		var accountID int64
		if err := rows.Scan(&accountID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		accountIDs = append(accountIDs, accountID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return accountIDs, nil
}
