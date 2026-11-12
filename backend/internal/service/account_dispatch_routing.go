package service

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

type accountDispatchContextKey struct{}
type accountDispatchRetryContextKey struct{}
type accountDispatchRetryScopeContextKey struct{}

type accountDispatchRetryScope struct {
	mu       sync.Mutex
	rejected map[int64]struct{}
}

// WithAccountDispatchRetryScope starts a fresh scope for sequential selections in
// one HTTP request or WebSocket turn. Reuse the returned context across failover
// calls; create another scope for each new turn or concurrent batch subrequest.
// Only confirmed stale candidates survive between selections, not caller-supplied
// failed IDs, so rebuilding a transient upstream-failure list keeps its meaning.
func WithAccountDispatchRetryScope(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, accountDispatchRetryScopeContextKey{}, &accountDispatchRetryScope{
		rejected: make(map[int64]struct{}),
	})
}

func (s *accountDispatchRetryScope) exclude(accountID int64) {
	if s == nil || accountID <= 0 {
		return
	}
	s.mu.Lock()
	s.rejected[accountID] = struct{}{}
	s.mu.Unlock()
}

func (s *accountDispatchRetryScope) copyRejectedTo(retry *accountDispatchCandidateRetry) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for accountID := range s.rejected {
		retry.excluded[accountID] = struct{}{}
		retry.attempted[accountID] = struct{}{}
	}
}

type accountDispatchSelection struct {
	requestGroupID int64
	policy         *AccountDispatchPolicy
	grants         map[int64]string
}

type accountDispatchCandidateRejectedError struct {
	accountID int64
	reason    string
}

func (e *accountDispatchCandidateRejectedError) Error() string {
	if e == nil {
		return "account dispatch candidate rejected"
	}
	return fmt.Sprintf("account dispatch candidate %d rejected: %s", e.accountID, e.reason)
}

func rejectAccountDispatchCandidate(accountID int64, reason string) error {
	return &accountDispatchCandidateRejectedError{accountID: accountID, reason: reason}
}

type accountDispatchCandidateRetry struct {
	excluded  map[int64]struct{}
	attempted map[int64]struct{}
	scope     *accountDispatchRetryScope
}

func newAccountDispatchCandidateRetry(excluded map[int64]struct{}) *accountDispatchCandidateRetry {
	state := &accountDispatchCandidateRetry{
		excluded:  make(map[int64]struct{}, len(excluded)+1),
		attempted: make(map[int64]struct{}, len(excluded)+1),
	}
	for accountID := range excluded {
		state.excluded[accountID] = struct{}{}
		state.attempted[accountID] = struct{}{}
	}
	return state
}

func withAccountDispatchCandidateRetry(ctx context.Context, excluded map[int64]struct{}) (context.Context, *accountDispatchCandidateRetry) {
	scope, _ := ctx.Value(accountDispatchRetryScopeContextKey{}).(*accountDispatchRetryScope)
	if retry, _ := ctx.Value(accountDispatchRetryContextKey{}).(*accountDispatchCandidateRetry); retry != nil && retry.scope == scope {
		for accountID := range excluded {
			retry.excluded[accountID] = struct{}{}
			retry.attempted[accountID] = struct{}{}
		}
		return ctx, retry
	}
	retry := newAccountDispatchCandidateRetry(excluded)
	retry.scope = scope
	scope.copyRejectedTo(retry)
	return context.WithValue(ctx, accountDispatchRetryContextKey{}, retry), retry
}

// A confirmed stale account stays excluded across nested selection and image-size
// passes. Transient read errors and pass-specific eligibility do not call this.
func excludeStaleAccountDispatchCandidate(ctx context.Context, accountID int64) {
	if accountID <= 0 {
		return
	}
	scope, _ := ctx.Value(accountDispatchRetryScopeContextKey{}).(*accountDispatchRetryScope)
	if retry, _ := ctx.Value(accountDispatchRetryContextKey{}).(*accountDispatchCandidateRetry); retry != nil && retry.scope == scope {
		retry.excluded[accountID] = struct{}{}
		retry.attempted[accountID] = struct{}{}
	}
	scope.exclude(accountID)
}

// retry adds only a candidate that failed fresh admission. Each account ID is
// retried at most once; context cancellation/deadlines remain authoritative.
func (r *accountDispatchCandidateRetry) retry(ctx context.Context, err error) (bool, error) {
	var rejected *accountDispatchCandidateRejectedError
	if !errors.As(err, &rejected) {
		return false, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}
	if rejected.accountID <= 0 {
		return false, ErrNoAvailableAccounts
	}
	if _, exists := r.attempted[rejected.accountID]; exists {
		return false, ErrNoAvailableAccounts
	}
	r.attempted[rejected.accountID] = struct{}{}
	r.excluded[rejected.accountID] = struct{}{}
	r.scope.exclude(rejected.accountID)
	return true, nil
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
	if boundAccountID := geminiCachedContentBoundAccountIDFromContext(ctx); boundAccountID > 0 && account.ID != boundAccountID {
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
	boundAccountID := geminiCachedContentBoundAccountIDFromContext(ctx)
	for id := range selection.grants {
		if boundAccountID > 0 && id != boundAccountID {
			continue
		}
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
