package service

import (
	"context"
	"errors"
	"sort"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const maxProxyFallbackRevert = 500

// ProxyFallbackLister lists accounts moved off a proxy by expiry fallback.
type ProxyFallbackLister interface {
	ListFallbackAccountSummariesByOriginProxyID(ctx context.Context, proxyID int64) ([]ProxyAccountSummary, error)
}

type ProxyFallbackRevertItem struct {
	ID      int64  `json:"id"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ProxyFallbackRevertResult struct {
	RevertedIDs []int64                   `json:"reverted_ids"`
	Skipped     []ProxyFallbackRevertItem `json:"skipped"`
	Failed      []ProxyFallbackRevertItem `json:"failed"`
}

func (s *adminServiceImpl) fallbackLister() (ProxyFallbackLister, error) {
	lister, ok := s.proxyRepo.(ProxyFallbackLister)
	if !ok {
		return nil, infraerrors.InternalServer("PROXY_REPOSITORY_UNSUPPORTED", "proxy repository cannot list fallback accounts")
	}
	return lister, nil
}

// GetProxyFallbackAccounts lists the caller's accounts whose fallback origin
// is proxyID, with the proxy they use now.
func (s *adminServiceImpl) GetProxyFallbackAccounts(ctx context.Context, proxyID int64) ([]ProxyAccountSummary, error) {
	if err := s.requireProxyAccess(ctx, proxyID); err != nil {
		return nil, err
	}
	lister, err := s.fallbackLister()
	if err != nil {
		return nil, err
	}
	summaries, err := lister.ListFallbackAccountSummariesByOriginProxyID(ctx, proxyID)
	if err != nil {
		return nil, err
	}
	scope := ScopeFromContextOrDeny(ctx)
	if scope.Unrestricted {
		return summaries, nil
	}
	owned := make([]ProxyAccountSummary, 0, len(summaries))
	for _, sum := range summaries {
		if scope.OwnsWorkspaceID(sum.WorkspaceID) {
			owned = append(owned, sum)
		}
	}
	return owned, nil
}

// RevertProxyFallbackAccounts restores accounts onto proxyID, their fallback
// origin. Empty accountIDs means all of them. The proxy must be live, active
// and unexpired. Each account goes through RevertAccountProxyFallback (scope
// check, shadow propagation, scheduler outbox) and commits on its own; ids
// that are not fallback accounts of this proxy are skipped, so a replay is safe.
func (s *adminServiceImpl) RevertProxyFallbackAccounts(ctx context.Context, proxyID int64, accountIDs []int64) (*ProxyFallbackRevertResult, error) {
	if len(accountIDs) > maxProxyFallbackRevert {
		return nil, infraerrors.BadRequest("TOO_MANY_ACCOUNTS", "account_ids may list at most 500 accounts")
	}
	candidates, err := s.GetProxyFallbackAccounts(ctx, proxyID)
	if err != nil {
		return nil, err
	}
	if err := s.requireUsableFallbackOrigin(ctx, proxyID); err != nil {
		return nil, err
	}

	eligible := make(map[int64]bool, len(candidates))
	for _, c := range candidates {
		eligible[c.ID] = true
	}
	targets := uniquePositiveIDs(accountIDs)
	if len(accountIDs) == 0 {
		for _, c := range candidates {
			targets = append(targets, c.ID)
		}
		sort.Slice(targets, func(i, j int) bool { return targets[i] < targets[j] })
	}

	result := &ProxyFallbackRevertResult{RevertedIDs: []int64{}, Skipped: []ProxyFallbackRevertItem{}, Failed: []ProxyFallbackRevertItem{}}
	for _, id := range targets {
		if !eligible[id] {
			result.Skipped = append(result.Skipped, ProxyFallbackRevertItem{ID: id, Code: "ACCOUNT_NOT_FALLBACK_OF_PROXY", Message: "account is not in fallback from this proxy"})
			continue
		}
		if err := s.RevertAccountProxyFallback(ctx, id); err != nil {
			if errors.Is(err, ErrAccountNotInFallback) {
				result.Skipped = append(result.Skipped, ProxyFallbackRevertItem{ID: id, Code: "ACCOUNT_NOT_IN_FALLBACK", Message: ErrAccountNotInFallback.Error()})
				continue
			}
			result.Failed = append(result.Failed, ProxyFallbackRevertItem{ID: id, Code: infraerrors.Reason(err), Message: err.Error()})
			continue
		}
		result.RevertedIDs = append(result.RevertedIDs, id)
	}
	return result, nil
}
