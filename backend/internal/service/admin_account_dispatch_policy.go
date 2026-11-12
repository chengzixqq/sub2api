package service

import (
	"context"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func (s *adminServiceImpl) accountDispatchPolicyWriter(ctx context.Context) (AccountDispatchPolicyWriter, error) {
	if err := s.ValidateSimpleModeGroupOperation(AdminGroupOperationRPMOverride); err != nil {
		return nil, err
	}
	if err := RequireStationOwnerScope(ctx); err != nil {
		return nil, err
	}
	writer, ok := s.accountRepo.(AccountDispatchPolicyWriter)
	if !ok {
		return nil, infraerrors.New(http.StatusServiceUnavailable, "ACCOUNT_DISPATCH_POLICY_UNAVAILABLE", "account dispatch policy repository unavailable")
	}
	return writer, nil
}

func (s *adminServiceImpl) ListAccountDispatchPolicies(ctx context.Context, groupID int64) ([]AccountDispatchPolicy, error) {
	writer, err := s.accountDispatchPolicyWriter(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.GetGroup(ctx, groupID); err != nil {
		return nil, err
	}
	return writer.ListAccountDispatchPolicies(ctx, groupID)
}

func (s *adminServiceImpl) PutAccountDispatchPolicy(ctx context.Context, policy *AccountDispatchPolicy, expectedVersion int64) (*AccountDispatchPolicy, error) {
	writer, err := s.accountDispatchPolicyWriter(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireAccountDispatchWritesEnabled(); err != nil {
		return nil, err
	}
	if policy == nil || policy.UserID <= 0 || policy.GroupID <= 0 || expectedVersion < 0 {
		return nil, infraerrors.BadRequest("INVALID_ACCOUNT_DISPATCH_POLICY", "invalid account dispatch policy identifiers or version")
	}
	if policy.Mode != AccountDispatchModeAppend && policy.Mode != AccountDispatchModeReplace {
		return nil, infraerrors.BadRequest("INVALID_ACCOUNT_DISPATCH_MODE", "mode must be append or replace")
	}
	if len(policy.Accounts) > 500 {
		return nil, infraerrors.BadRequest("TOO_MANY_ACCOUNT_DISPATCH_GRANTS", "account dispatch policy supports at most 500 accounts")
	}
	group, err := s.GetGroup(ctx, policy.GroupID)
	if err != nil {
		return nil, err
	}
	user, err := s.userRepo.GetByID(ctx, policy.UserID)
	if err != nil {
		return nil, err
	}
	canUseGroup := user.CanBindGroup(group.ID, group.IsExclusive)
	if group.IsSubscriptionType() {
		canUseGroup = false
		if s.userSubRepo != nil {
			_, err := s.userSubRepo.GetActiveByUserIDAndGroupID(ctx, user.ID, group.ID)
			canUseGroup = err == nil
		}
	}
	if !canUseGroup {
		return nil, infraerrors.New(http.StatusForbidden, "ACCOUNT_DISPATCH_GROUP_DENIED", "user cannot use this group")
	}
	ids := make([]int64, 0, len(policy.Accounts))
	seen := make(map[int64]struct{}, len(policy.Accounts))
	for _, grant := range policy.Accounts {
		if grant.AccountID <= 0 || (grant.Origin != AccountDispatchOriginMember && grant.Origin != AccountDispatchOriginExtra) {
			return nil, infraerrors.BadRequest("INVALID_ACCOUNT_DISPATCH_GRANT", "invalid account grant")
		}
		if _, ok := seen[grant.AccountID]; ok {
			return nil, infraerrors.BadRequest("DUPLICATE_ACCOUNT_DISPATCH_GRANT", "duplicate account grant")
		}
		seen[grant.AccountID] = struct{}{}
		ids = append(ids, grant.AccountID)
	}
	accounts, err := s.accountRepo.GetByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	accountByID := make(map[int64]*Account, len(accounts))
	for _, account := range accounts {
		accountByID[account.ID] = account
	}
	for _, grant := range policy.Accounts {
		account := accountByID[grant.AccountID]
		if account == nil {
			return nil, infraerrors.BadRequest("ACCOUNT_DISPATCH_ACCOUNT_NOT_FOUND", "account not found")
		}
		member := false
		for _, groupID := range account.GroupIDs {
			if groupID == group.ID {
				member = true
				break
			}
		}
		if grant.Origin == AccountDispatchOriginMember && !member || grant.Origin == AccountDispatchOriginExtra && member {
			return nil, infraerrors.BadRequest("ACCOUNT_DISPATCH_ORIGIN_MISMATCH", "account group membership changed; reload and retry")
		}
		if grant.Origin == AccountDispatchOriginExtra && (group.Platform == PlatformComposite || account.Platform != group.Platform) {
			return nil, infraerrors.BadRequest("ACCOUNT_DISPATCH_PLATFORM_MISMATCH", "extra account must match a single-platform group")
		}
		if grant.Origin == AccountDispatchOriginExtra && account.WorkspaceID != domain.DefaultWorkspaceID {
			if !s.accountDispatchWorkspaceGranted(ctx, account.WorkspaceID, group.ID) {
				return nil, infraerrors.New(http.StatusForbidden, "ACCOUNT_DISPATCH_WORKSPACE_DENIED", "account workspace is not granted this group")
			}
		}
	}
	return writer.PutAccountDispatchPolicy(ctx, policy, expectedVersion)
}

func (s *adminServiceImpl) accountDispatchWorkspaceGranted(ctx context.Context, workspaceID, groupID int64) bool {
	if s.entClient == nil {
		return false
	}
	rows, err := s.entClient.QueryContext(ctx, `
		SELECT 1 FROM workspace_group_grants wg
		JOIN workspaces w ON w.id = wg.workspace_id AND w.deleted_at IS NULL AND w.status = 'active'
		WHERE wg.workspace_id = $1 AND wg.group_id = $2 AND wg.enabled = TRUE LIMIT 1
	`, workspaceID, groupID)
	if err != nil {
		return false
	}
	defer func() { _ = rows.Close() }()
	return rows.Next()
}

func (s *adminServiceImpl) DeleteAccountDispatchPolicy(ctx context.Context, userID, groupID, expectedVersion int64) error {
	writer, err := s.accountDispatchPolicyWriter(ctx)
	if err != nil {
		return err
	}
	if err := requireAccountDispatchWritesEnabled(); err != nil {
		return err
	}
	if userID <= 0 || groupID <= 0 || expectedVersion <= 0 {
		return infraerrors.BadRequest("INVALID_ACCOUNT_DISPATCH_POLICY", "invalid account dispatch policy identifiers or version")
	}
	return writer.DeleteAccountDispatchPolicy(ctx, userID, groupID, expectedVersion)
}
