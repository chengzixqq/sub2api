package service

import (
	"context"
	"fmt"
	"slices"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// APIKeyGroupMigrationRepository supports exact undo of key group changes.
type APIKeyGroupMigrationRepository interface {
	CountOtherKeysInGroup(ctx context.Context, userID, groupID, exceptKeyID int64) (int64, error)
	MigrateUserKeysGroup(ctx context.Context, userID, oldGroupID, newGroupID int64, keyIDs []int64) ([]int64, error)
}

// AdminUpdateAPIKeyGroupOptions extends a key group change.
type AdminUpdateAPIKeyGroupOptions struct {
	// RevokeGroupAccessID removes this exclusive group from the user's allowed
	// groups in the same transaction (undo of an auto-grant). It fails with
	// GROUP_STILL_IN_USE when another live key of the user is still bound to it.
	RevokeGroupAccessID int64
}

// ReplaceUserGroupOptions extends ReplaceUserGroup for exact undo.
type ReplaceUserGroupOptions struct {
	// KeyIDs, when set, migrates only these keys; each must currently be bound to the old group.
	KeyIDs []int64
	// KeepOldGroupAccess skips removing the old group from the user's allowed groups.
	KeepOldGroupAccess bool
}

var (
	errGroupStillInUse = infraerrors.Conflict("GROUP_STILL_IN_USE", "another API key of this user is still bound to the group")
	errKeyNotInGroup   = infraerrors.BadRequest("KEY_NOT_IN_GROUP", "every key_id must belong to the user and be bound to old_group_id")
)

func (s *adminServiceImpl) groupMigrationRepo() (APIKeyGroupMigrationRepository, error) {
	repo, ok := s.apiKeyRepo.(APIKeyGroupMigrationRepository)
	if !ok {
		return nil, infraerrors.InternalServer("API_KEY_REPOSITORY_UNSUPPORTED", "api key repository cannot migrate groups")
	}
	return repo, nil
}

// AdminUpdateAPIKeyGroupIDWithOptions changes a key's group like
// AdminUpdateAPIKeyGroupID and, with RevokeGroupAccessID, also withdraws an
// exclusive group grant atomically.
func (s *adminServiceImpl) AdminUpdateAPIKeyGroupIDWithOptions(ctx context.Context, keyID int64, groupID *int64, opts AdminUpdateAPIKeyGroupOptions) (*AdminUpdateAPIKeyGroupIDResult, error) {
	if opts.RevokeGroupAccessID <= 0 {
		return s.AdminUpdateAPIKeyGroupID(ctx, keyID, groupID)
	}
	if groupID != nil && *groupID == opts.RevokeGroupAccessID {
		return nil, infraerrors.BadRequest("INVALID_REVOKE_GROUP", "cannot revoke access to the group the key moves to")
	}
	revokeGroup, err := s.groupRepo.GetByID(ctx, opts.RevokeGroupAccessID)
	if err != nil {
		return nil, err
	}
	if !revokeGroup.IsExclusive || revokeGroup.IsSubscriptionType() {
		return nil, infraerrors.BadRequest("GROUP_NOT_EXCLUSIVE", "only exclusive standard group access can be revoked here")
	}
	if s.entClient == nil {
		return nil, fmt.Errorf("entClient is nil, cannot revoke group access atomically")
	}
	migrations, err := s.groupMigrationRepo()
	if err != nil {
		return nil, err
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)

	result, err := s.AdminUpdateAPIKeyGroupID(txCtx, keyID, groupID)
	if err != nil {
		return nil, err
	}
	key := result.APIKey
	others, err := migrations.CountOtherKeysInGroup(txCtx, key.UserID, opts.RevokeGroupAccessID, key.ID)
	if err != nil {
		return nil, err
	}
	if others > 0 || (key.GroupID != nil && *key.GroupID == opts.RevokeGroupAccessID) {
		return nil, errGroupStillInUse
	}
	if err := s.userRepo.RemoveGroupFromUserAllowedGroups(txCtx, key.UserID, opts.RevokeGroupAccessID); err != nil {
		return nil, fmt.Errorf("remove group from allowed groups: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}
	s.invalidateUserKeysAuthCache(ctx, key.UserID)

	revoked := opts.RevokeGroupAccessID
	result.RevokedGroupAccess = true
	result.RevokedGroupID = &revoked
	return result, nil
}

// ReplaceUserGroupWithOptions is ReplaceUserGroup with migrated key ids,
// whether the new group was newly granted, and exact-undo options.
func (s *adminServiceImpl) ReplaceUserGroupWithOptions(ctx context.Context, userID, oldGroupID, newGroupID int64, opts ReplaceUserGroupOptions) (*ReplaceUserGroupResult, error) {
	if oldGroupID == newGroupID {
		return nil, infraerrors.BadRequest("SAME_GROUP", "old and new group must be different")
	}
	newGroup, err := s.groupRepo.GetByID(ctx, newGroupID)
	if err != nil {
		return nil, err
	}
	if newGroup.Status != StatusActive {
		return nil, infraerrors.BadRequest("GROUP_NOT_ACTIVE", "target group is not active")
	}
	if !newGroup.IsExclusive {
		return nil, infraerrors.BadRequest("GROUP_NOT_EXCLUSIVE", "target group is not exclusive")
	}
	if newGroup.IsSubscriptionType() {
		return nil, infraerrors.BadRequest("GROUP_IS_SUBSCRIPTION", "subscription groups are not supported for replacement")
	}
	if s.entClient == nil {
		return nil, fmt.Errorf("entClient is nil, cannot perform group replacement")
	}
	migrations, err := s.groupMigrationRepo()
	if err != nil {
		return nil, err
	}
	keyIDs := uniquePositiveIDs(opts.KeyIDs)

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)

	user, err := s.userRepo.GetByID(txCtx, userID)
	if err != nil {
		return nil, err
	}
	newlyGranted := !slices.Contains(user.AllowedGroups, newGroupID)
	if err := s.userRepo.AddGroupToAllowedGroups(txCtx, userID, newGroupID); err != nil {
		return nil, fmt.Errorf("add new group to allowed groups: %w", err)
	}
	moved, err := migrations.MigrateUserKeysGroup(txCtx, userID, oldGroupID, newGroupID, keyIDs)
	if err != nil {
		return nil, fmt.Errorf("migrate api keys: %w", err)
	}
	if len(keyIDs) > 0 && len(moved) != len(keyIDs) {
		return nil, errKeyNotInGroup
	}
	if !opts.KeepOldGroupAccess {
		if err := s.userRepo.RemoveGroupFromUserAllowedGroups(txCtx, userID, oldGroupID); err != nil {
			return nil, fmt.Errorf("remove old group from allowed groups: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}
	s.invalidateUserKeysAuthCache(ctx, userID)

	return &ReplaceUserGroupResult{MigratedKeys: int64(len(moved)), MigratedKeyIDs: moved, NewGroupNewlyGranted: newlyGranted}, nil
}

func (s *adminServiceImpl) invalidateUserKeysAuthCache(ctx context.Context, userID int64) {
	if s.authCacheInvalidator == nil {
		return
	}
	keys, err := s.apiKeyRepo.ListKeysByUserID(ctx, userID)
	if err != nil {
		return
	}
	for _, k := range keys {
		s.authCacheInvalidator.InvalidateAuthCacheByKey(ctx, k)
	}
}

func uniquePositiveIDs(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
