package service

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
)

type alignmentQueueCondition struct {
	key   string
	match func([]byte, http.Header, []BetaPolicyRule) func(ClaudeCustomizationSettings) bool
}

var alignmentQueueConditions = []alignmentQueueCondition{
	{ClaudeFieldContextManagementMissingBeta, contextManagementQueueCondition},
	{ClaudeFieldThinkingCacheControl, thinkingCacheControlQueueCondition},
	{ClaudeFieldCacheControlLimitMode, cacheControlLimitQueueCondition},
	{ClaudeFieldUnknownBetaHeader, unknownBetaQueueCondition},
}

// AlignmentMayRejectBeforeDispatch leaves the status writable when a matching
// switch could reject the request on a selectable account.
func (s *GatewayService) AlignmentMayRejectBeforeDispatch(c *gin.Context, body []byte, header http.Header, groupID, fallbackID *int64) bool {
	if !IsAnthropicMessagesRequest(c) || s.settingService == nil || s.accountRepo == nil {
		return false
	}
	ctx := c.Request.Context()
	snapshot := s.settingService.alignmentSnapshot(ctx)
	return s.alignmentRequestMayReject(ctx, snapshot, body, header, groupID, fallbackID)
}

func (s *GatewayService) alignmentRequestMayReject(ctx context.Context, snapshot *alignmentRejectSnapshot, body []byte, header http.Header, groupID, fallbackID *int64) bool {
	for _, condition := range alignmentQueueConditions {
		if !snapshot.reject[condition.key] {
			continue
		}
		mayReject := condition.match(body, header, snapshot.betaRules)
		if mayReject != nil && s.alignmentConditionMayReject(ctx, groupID, fallbackID, snapshot, condition.key, mayReject) {
			return true
		}
	}
	return false
}

// DeferClaudeQueuePings keeps upstream-dependent headers writable across the
// user/account queues, including sparse overrides on fallback and granted accounts.
func (s *GatewayService) DeferClaudeQueuePings(c *gin.Context, body []byte, header http.Header, groupID, fallbackID *int64) bool {
	if s.settingService == nil || s.accountRepo == nil || !IsAnthropicMessagesRequest(c) {
		return false
	}
	ctx := c.Request.Context()
	snapshot := s.settingService.alignmentSnapshot(ctx)
	if snapshot.responseHeaders {
		if s.alignmentConditionMayReject(ctx, groupID, fallbackID, snapshot, ClaudeFieldRequestIDMode, func(p ClaudeCustomizationSettings) bool { return p.RequestIDMode == ClaudeRequestIDFill }) ||
			s.alignmentConditionMayReject(ctx, groupID, fallbackID, snapshot, ClaudeFieldUpstreamHeaderPassthrough, func(p ClaudeCustomizationSettings) bool {
				return p.UpstreamHeaderPassthrough == ClaudeHeaderPassthroughOn
			}) {
			return true
		}
	}
	return s.alignmentRequestMayReject(ctx, snapshot, body, header, groupID, fallbackID)
}

func (s *GatewayService) alignmentConditionMayReject(ctx context.Context, groupID, fallbackID *int64, snapshot *alignmentRejectSnapshot, key string, mayReject func(ClaudeCustomizationSettings) bool) bool {
	globalMay := mayReject(snapshot.global)
	var ids []int64
	for id, policy := range snapshot.accounts[key] {
		if mayReject(policy) {
			ids = append(ids, id)
		}
	}
	if !globalMay && len(ids) == 0 {
		return false
	}
	if snapshot.routingUnavailable {
		return true
	}
	groups, unresolved := s.alignmentCandidatePools(ctx, groupID, fallbackID, key, snapshot.routing)
	if unresolved {
		return true
	}
	if len(groups) == 0 {
		return false
	}
	if globalMay {
		return true
	}
	if routing := snapshot.routing; routing != nil {
		ctx, err := withAccountDispatchSelection(ctx, routing, groupID)
		if err != nil {
			return true
		}
		allGroups := s.cfg != nil && s.cfg.RunMode == config.RunModeSimple
		return routing.containsCandidate(accountDispatchFromContext(ctx), ids, groups, allGroups)
	}
	ctx, err := withAccountDispatchSelection(ctx, s.accountRepo, groupID)
	if err == nil {
		var reject bool
		reject, err = s.alignmentAccountMayReject(ctx, key, snapshot.global, mayReject, ids, groups)
		if err == nil {
			return reject
		}
	}
	if shouldLogClaudePolicyFailure(&s.settingService.alignmentCandidateErrorLogAt) {
		slog.WarnContext(ctx, "alignment candidate lookup failed; keeping queue pings", "key", key, "error", err)
	}
	return false
}

// Composite routing changes the target platform within the resolved group.
// An unknown target leaves the status writable until dispatch.
func (s *GatewayService) alignmentCandidatePools(ctx context.Context, groupID, fallbackID *int64, key string, routing *ClaudeAlignmentRouting) (groups []alignmentPool, unresolved bool) {
	allGroups := s.cfg != nil && s.cfg.RunMode == config.RunModeSimple
	addPool := func(id *int64, platform string, mixedOnly bool) {
		for _, pool := range groups {
			sameGroup := pool.groupID == id || (pool.groupID != nil && id != nil && *pool.groupID == *id)
			if (allGroups || sameGroup) && pool.platform == platform && pool.mixedOnly == mixedOnly {
				return
			}
		}
		groups = append(groups, alignmentPool{groupID: id, platform: platform, mixedOnly: mixedOnly})
	}
	for i, id := range []*int64{groupID, fallbackID} {
		if i == 1 {
			if id == nil {
				break
			}
			// Messages clears the forced platform when retrying an invalid request.
			ctx = context.WithValue(ctx, ctxkey.ForcePlatform, "")
		}
		var group *Group
		resolvedID := id
		var err error
		if forced, _ := ctx.Value(ctxkey.ForcePlatform).(string); forced == "" {
			lookup := s.resolveGroupByID
			if routing != nil {
				lookup = func(ctx context.Context, id int64) (*Group, error) {
					if g := s.groupFromContext(ctx, id); g != nil {
						return g, nil
					}
					if g := routing.Groups[id]; g != nil {
						return g, nil
					}
					return nil, ErrGroupNotFound
				}
			}
			group, resolvedID, err = resolveGatewayGroupWith(ctx, id, lookup)
		}
		if errors.Is(err, ErrClaudeCodeOnly) || (i == 1 && errors.Is(err, ErrGroupNotFound)) {
			continue
		}
		if err != nil {
			s.logAlignmentGroupLookupError(ctx, id, err)
			return nil, true
		}
		if group != nil && group.Platform == PlatformComposite {
			if _, ok := ResolvedTargetPlatformFromContext(ctx); !ok {
				return nil, true
			}
		}
		platform, forced, err := s.resolvePlatform(ctx, resolvedID, group, "")
		if err != nil {
			s.logAlignmentGroupLookupError(ctx, resolvedID, err)
			return nil, true
		}
		// Non-batch selection uses a concrete group's own platform after fallback.
		if platform == PlatformAnthropic || (!forced && group != nil && group.Platform == PlatformAnthropic) {
			addPool(resolvedID, PlatformAnthropic, false)
		}
		if key == ClaudeFieldUnknownBetaHeader || key == ClaudeFieldRequestIDMode || key == ClaudeFieldUpstreamHeaderPassthrough {
			mixed := !forced && (platform == PlatformGemini || platform == PlatformAnthropic)
			direct := platform == PlatformAntigravity || (!forced && group != nil && group.Platform == PlatformAntigravity)
			if direct || mixed {
				addPool(resolvedID, PlatformAntigravity, !direct)
			}
		}
	}
	return groups, false
}

func (s *GatewayService) logAlignmentGroupLookupError(ctx context.Context, groupID *int64, err error) {
	if shouldLogClaudePolicyFailure(&s.settingService.alignmentGroupErrorLogAt) {
		slog.WarnContext(ctx, "alignment group lookup failed; deferring queue pings", "group_id", derefGroupID(groupID), "error", err)
	}
}

type alignmentGroupReader interface {
	HasGroupAlignmentCandidate(context.Context, []string, []int64, *int64, bool, string, bool) (bool, error)
}

type alignmentPool struct {
	groupID   *int64
	platform  string
	mixedOnly bool
}

func (s *GatewayService) alignmentAccountMayReject(ctx context.Context, key string, global ClaudeCustomizationSettings, mayReject func(ClaudeCustomizationSettings) bool, ids []int64, groups []alignmentPool) (bool, error) {
	var accountTypes []string
	for _, field := range claudeCustomizationRegistry {
		if field.Key == key {
			accountTypes = field.AccountTypes
			break
		}
	}
	dispatch := accountDispatchFromContext(ctx)
	var granted []int64
	if dispatch != nil {
		for _, id := range ids {
			if dispatch.grants[id] != "" {
				granted = append(granted, id)
			}
		}
	}
	if len(granted) > 0 {
		accounts, err := s.accountRepo.GetByIDs(ctx, granted)
		if err != nil {
			return false, err
		}
		for _, account := range accounts {
			if account == nil || !slices.Contains(accountTypes, account.Type) ||
				!account.IsActive() || !account.Schedulable || !accountDispatchExplicitlyAllowed(dispatch, account) {
				continue
			}
			policy := global
			overrides, _ := account.Extra[AccountClaudeCustomizationExtraKey].(map[string]any)
			applyClaudeAccountOverrides(&policy, overrides, account.Type)
			if mayReject(policy) && slices.ContainsFunc(groups, func(pool alignmentPool) bool {
				return pool.platform == account.Platform && (!pool.mixedOnly || (account.IsMixedSchedulingEnabled() && dispatch.grants[account.ID] == "member"))
			}) {
				return true, nil
			}
		}
	}
	if AccountDispatchHasReplacePolicy(ctx) {
		return false, nil
	}
	reader, ok := s.accountRepo.(alignmentGroupReader)
	if !ok {
		return false, errors.New("alignment group reader unavailable")
	}
	allGroups := s.cfg != nil && s.cfg.RunMode == config.RunModeSimple
	for _, pool := range groups {
		found, err := reader.HasGroupAlignmentCandidate(ctx, accountTypes, ids, pool.groupID, allGroups, pool.platform, pool.mixedOnly)
		if found || err != nil {
			return found, err
		}
	}
	return false, nil
}

func cacheControlLimitQueueCondition(body []byte, header http.Header, _ []BetaPolicyRule) func(ClaudeCustomizationSettings) bool {
	if clientCacheControlOverflow(body, header) == 0 {
		return nil
	}
	return func(policy ClaudeCustomizationSettings) bool {
		return policy.CacheControlLimitMode == ClaudeAlignReject
	}
}

// Match the incoming shape once, then check each policy that could be selected.
func contextManagementQueueCondition(body []byte, header http.Header, _ []BetaPolicyRule) func(ClaudeCustomizationSettings) bool {
	if !MissingContextManagementBeta(header, body) {
		return nil
	}
	unknown := claude.UnknownTopLevelRequestFields(body, anthropicBetaHeader(header))
	for _, field := range unknown {
		// Strict fallback policy can remove both fallback fields before dispatch.
		if field != "context_management" && field != "fallbacks" && field != "fallback_credit_token" {
			return nil
		}
	}
	return func(policy ClaudeCustomizationSettings) bool {
		return policy.ContextManagementMissingBeta == ClaudeAlignReject &&
			(len(unknown) == 1 || policy.FallbackPolicy == ClaudeFallbackStrict)
	}
}

func unknownBetaQueueCondition(_ []byte, header http.Header, rules []BetaPolicyRule) func(ClaudeCustomizationSettings) bool {
	if len(unknownBetaTokens(anthropicBetaHeader(header), rules)) == 0 {
		return nil
	}
	return func(policy ClaudeCustomizationSettings) bool { return policy.UnknownBetaHeader == ClaudeAlignReject }
}
