package service

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

type accountDispatchContextKey struct{}

type accountDispatchSelection struct {
	requestGroupID int64
	policy         *AccountDispatchPolicy
	grants         map[int64]string
}

// withAccountDispatchSelection binds the policy to the original API key group,
// before a gateway route can switch to a fallback group.
func withAccountDispatchSelection(ctx context.Context, repo any, groupID *int64) (context.Context, error) {
	if selection, ok := ctx.Value(accountDispatchContextKey{}).(*accountDispatchSelection); ok && selection != nil {
		return ctx, nil
	}
	if originalGroupID, ok := ctx.Value(ctxkey.AccountDispatchGroupID).(int64); ok && originalGroupID > 0 {
		groupID = &originalGroupID
	}
	if groupID == nil {
		if group, ok := ctx.Value(ctxkey.Group).(*Group); ok && IsGroupContextValid(group) {
			groupID = &group.ID
		}
	}
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	if userID <= 0 || groupID == nil || *groupID <= 0 {
		return ctx, nil
	}
	reader, ok := repo.(AccountDispatchPolicyReader)
	if !ok {
		return ctx, fmt.Errorf("account dispatch policy reader unavailable")
	}
	policy, err := reader.GetAccountDispatchPolicy(ctx, userID, *groupID)
	if err != nil {
		return ctx, fmt.Errorf("load account dispatch policy: %w", err)
	}
	selection := &accountDispatchSelection{requestGroupID: *groupID, policy: policy}
	if policy != nil {
		selection.grants = make(map[int64]string, len(policy.Accounts))
		for _, grant := range policy.Accounts {
			selection.grants[grant.AccountID] = grant.Origin
		}
	}
	return context.WithValue(ctx, accountDispatchContextKey{}, selection), nil
}

// CheckAccountDispatchForTurn reloads the authorization and account before each
// new WebSocket turn. A completed/ongoing turn retains its upstream connection,
// but revocation must prevent starting the next turn on that connection.
func (s *OpenAIGatewayService) CheckAccountDispatchForTurn(ctx context.Context, userID int64, groupID *int64, accountID int64) error {
	if s == nil || s.accountRepo == nil || accountID <= 0 {
		return ErrNoAvailableAccounts
	}
	ctx = context.WithValue(ctx, ctxkey.UserID, userID)
	ctx = context.WithValue(ctx, accountDispatchContextKey{}, (*accountDispatchSelection)(nil))
	var err error
	ctx, err = withAccountDispatchSelection(ctx, s.accountRepo, groupID)
	if err != nil {
		return err
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return err
	}
	if account == nil || !account.IsSchedulable() || !s.openAIAccountMatchesSchedulingGroup(ctx, account, groupID) {
		return ErrNoAvailableAccounts
	}
	return nil
}

// EnsureAccountDispatchPolicy checks the authenticated key's policy before a
// model-list handler can fall back to a static catalog on lookup failure.
func (s *GatewayService) EnsureAccountDispatchPolicy(ctx context.Context, userID int64, groupID *int64) error {
	_, err := s.BindAccountDispatchPolicy(ctx, userID, groupID)
	return err
}

// BindAccountDispatchPolicy preserves one policy snapshot across all catalog
// lookups in the same request, while keeping lookup failures visible to callers.
func (s *GatewayService) BindAccountDispatchPolicy(ctx context.Context, userID int64, groupID *int64) (context.Context, error) {
	if userID <= 0 {
		return ctx, nil
	}
	ctx = context.WithValue(ctx, ctxkey.UserID, userID)
	return withAccountDispatchSelection(ctx, s.accountRepo, groupID)
}

func (s *GeminiMessagesCompatService) BindAccountDispatchPolicy(ctx context.Context, userID int64, groupID *int64) (context.Context, error) {
	if userID <= 0 {
		return ctx, nil
	}
	ctx = context.WithValue(ctx, ctxkey.UserID, userID)
	return withAccountDispatchSelection(ctx, s.accountRepo, groupID)
}

func AccountDispatchHasReplacePolicy(ctx context.Context) bool {
	selection := accountDispatchFromContext(ctx)
	return selection != nil && selection.policy != nil && selection.policy.Mode == AccountDispatchModeReplace
}

func accountDispatchFromContext(ctx context.Context) *accountDispatchSelection {
	selection, _ := ctx.Value(accountDispatchContextKey{}).(*accountDispatchSelection)
	return selection
}

func accountDispatchHasGroup(account *Account, groupID int64) bool {
	if account == nil {
		return false
	}
	for _, id := range account.GroupIDs {
		if id == groupID {
			return true
		}
	}
	for _, group := range account.AccountGroups {
		if group.GroupID == groupID {
			return true
		}
	}
	return false
}

func accountDispatchExplicitlyAllowed(selection *accountDispatchSelection, account *Account) bool {
	if selection == nil || selection.policy == nil || account == nil {
		return false
	}
	origin, ok := selection.grants[account.ID]
	if !ok {
		return false
	}
	return origin == "extra" || (origin == "member" && accountDispatchHasGroup(account, selection.requestGroupID))
}

func accountDispatchAllows(ctx context.Context, account *Account, effectiveGroupID *int64, fromGroupPool bool) bool {
	if account == nil {
		return false
	}
	selection := accountDispatchFromContext(ctx)
	if accountDispatchExplicitlyAllowed(selection, account) {
		return true
	}
	if account.DispatchScope == DispatchScopeAuthorizedOnly {
		return false
	}
	if selection != nil && selection.policy != nil && selection.policy.Mode == "replace" {
		return false
	}
	if fromGroupPool {
		return true
	}
	if effectiveGroupID == nil {
		return len(account.GroupIDs) == 0 && len(account.AccountGroups) == 0
	}
	return accountDispatchHasGroup(account, *effectiveGroupID)
}

func accountDispatchCandidates(ctx context.Context, repo any, groupID *int64, platform string, base []Account, allowMixed bool) ([]Account, error) {
	selection := accountDispatchFromContext(ctx)
	result := make([]Account, 0, len(base))
	seen := make(map[int64]struct{}, len(base))
	for i := range base {
		if !accountDispatchAllows(ctx, &base[i], groupID, true) {
			continue
		}
		result = append(result, base[i])
		seen[base[i].ID] = struct{}{}
	}
	if selection == nil || selection.policy == nil || len(selection.grants) == 0 {
		return result, nil
	}
	ids := make([]int64, 0, len(selection.grants))
	for id := range selection.grants {
		if _, ok := seen[id]; !ok {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return result, nil
	}
	getter, ok := repo.(interface {
		GetByIDs(context.Context, []int64) ([]*Account, error)
	})
	if !ok {
		return nil, fmt.Errorf("authorized account lookup unavailable")
	}
	accounts, err := getter.GetByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load authorized accounts: %w", err)
	}
	for _, account := range accounts {
		if !accountDispatchExplicitlyAllowed(selection, account) || !account.IsSchedulable() {
			continue
		}
		if platform != "" && account.Platform != platform && !(allowMixed && account.Platform == PlatformAntigravity && account.IsMixedSchedulingEnabled() && selection.grants[account.ID] == "member") {
			continue
		}
		if _, ok := seen[account.ID]; ok {
			continue
		}
		result = append(result, *account)
		seen[account.ID] = struct{}{}
	}
	return result, nil
}
