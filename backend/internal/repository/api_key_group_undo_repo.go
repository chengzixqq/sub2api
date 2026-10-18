package repository

import (
	"context"

	"github.com/Wei-Shaw/sub2api/ent/apikey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

var _ service.APIKeyGroupMigrationRepository = (*apiKeyRepository)(nil)

// CountOtherKeysInGroup counts the user's live keys bound to groupID, excluding exceptKeyID.
func (r *apiKeyRepository) CountOtherKeysInGroup(ctx context.Context, userID, groupID, exceptKeyID int64) (int64, error) {
	n, err := clientFromContext(ctx, r.client).APIKey.Query().
		Where(apikey.UserIDEQ(userID), apikey.GroupIDEQ(groupID), apikey.IDNEQ(exceptKeyID), apikey.DeletedAtIsNil()).
		Count(ctx)
	return int64(n), err
}

// MigrateUserKeysGroup moves the user's live keys on oldGroupID to newGroupID
// and returns the moved key ids. A non-empty keyIDs limits the move to those keys.
func (r *apiKeyRepository) MigrateUserKeysGroup(ctx context.Context, userID, oldGroupID, newGroupID int64, keyIDs []int64) (moved []int64, err error) {
	query := `UPDATE api_keys SET group_id = $1, updated_at = NOW()
		WHERE user_id = $2 AND group_id = $3 AND deleted_at IS NULL`
	args := []any{newGroupID, userID, oldGroupID}
	if len(keyIDs) > 0 {
		query += ` AND id = ANY($4)`
		args = append(args, pq.Array(keyIDs))
	}
	query += ` RETURNING id`
	rows, err := clientFromContext(ctx, r.client).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			moved = nil
		}
	}()
	moved = []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		moved = append(moved, id)
	}
	return moved, rows.Err()
}
