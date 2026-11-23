package service

import (
	"context"
	"errors"
	"log/slog"
	"runtime/debug"
	"slices"
	"time"
)

// ClaudeAlignmentOverrides is the sparse policy projection of a Claude gateway account.
type ClaudeAlignmentOverrides struct {
	AccountID     int64
	AccountType   string
	Platform      string
	Fields        map[string]any
	GroupIDs      []int64
	Schedulable   bool
	Mixed         bool
	DispatchScope string
}

type alignmentOverrideReader interface {
	ListClaudeAlignmentOverrides(context.Context, []string) ([]ClaudeAlignmentOverrides, error)
}

type alignmentRejectSnapshot struct {
	global             ClaudeCustomizationSettings
	reject             map[string]bool
	responseHeaders    bool
	betaRules          []BetaPolicyRule
	betaRulesLoaded    bool
	routing            *ClaudeAlignmentRouting
	routingUnavailable bool
	// Only accounts with overrides need a separate policy for shape-dependent checks.
	accounts  map[string]map[int64]ClaudeCustomizationSettings
	expiresAt time.Time
}

// AnyAlignmentReject reports whether the global policy or a native account
// can reject on this switch. A failed override lookup preserves the global policy.
func (s *SettingService) AnyAlignmentReject(ctx context.Context, key string) bool {
	return s != nil && s.alignmentSnapshot(ctx).reject[key]
}

// ClaudeModelsCatalogPolicy reads both discovery switches before account selection.
func (s *SettingService) ClaudeModelsCatalogPolicy(ctx context.Context) (official, fillRequestID bool) {
	if s == nil || s.settingRepo == nil {
		return false, false
	}
	global := &s.alignmentSnapshot(ctx).global
	return global.ModelsCatalog == ClaudeModelsCatalogOfficial, global.RequestIDMode == ClaudeRequestIDFill
}

func (s *SettingService) invalidateAlignmentSnapshot(ctx context.Context) {
	if s == nil {
		return
	}
	s.alignmentRejectMu.Lock()
	defer s.alignmentRejectMu.Unlock()
	if s.alignmentHits != nil {
		// Publish the replacement before the admin write returns. Requests
		// can keep observing the previous snapshot throughout the read.
		s.refreshAlignmentSnapshot(ctx)
	} else {
		s.alignmentRejectCache.Store(nil)
	}
}

func (s *SettingService) alignmentSnapshot(ctx context.Context) *alignmentRejectSnapshot {
	if p := s.alignmentRejectCache.Load(); p != nil {
		s.tryRefreshAlignmentSnapshot(ctx, p)
		return p
	}
	s.alignmentRejectMu.Lock()
	defer s.alignmentRejectMu.Unlock()
	if p := s.alignmentRejectCache.Load(); p != nil {
		return p
	}
	return s.refreshAlignmentSnapshot(ctx)
}

// A cold auth failure uses the default while the same refresh runs as for
// stale snapshots. Other cold consumers may still wait for a complete policy.
func (s *SettingService) tryRefreshAlignmentSnapshot(ctx context.Context, snapshot *alignmentRejectSnapshot) {
	if snapshot != nil && time.Now().Before(snapshot.expiresAt) || !s.alignmentRejectMu.TryLock() {
		return
	}
	if s.alignmentRejectCache.Load() != snapshot {
		s.alignmentRejectMu.Unlock()
		return
	}
	go func() {
		defer s.alignmentRejectMu.Unlock()
		defer func() {
			if r := recover(); r != nil {
				var stale alignmentRejectSnapshot
				if snapshot != nil {
					stale = *snapshot
				} else {
					stale.global = DefaultClaudeCustomizationSettings()
				}
				stale.expiresAt = time.Now().Add(time.Second)
				s.alignmentRejectCache.Store(&stale)
				if shouldLogClaudePolicyFailure(&s.alignmentPanicLogAt) {
					slog.ErrorContext(ctx, "alignment snapshot refresh panicked", "panic", r, "stack", string(debug.Stack()))
				}
			}
		}()
		s.refreshAlignmentSnapshot(ctx)
	}()
}

// Called with alignmentRejectMu held. Keep request context values, but let the
// shared read finish even if the initiating request has already gone away.
func (s *SettingService) refreshAlignmentSnapshot(requestCtx context.Context) *alignmentRejectSnapshot {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(requestCtx), 2*time.Second)
	defer cancel()
	global, globalErr := s.GetClaudeCustomizationSettings(ctx)
	if globalErr != nil {
		global = DefaultClaudeCustomizationSettings()
		s.logClaudeCustomizationResolveFailure(ctx, nil, globalErr)
	}
	p := &alignmentRejectSnapshot{global: global, reject: make(map[string]bool)}
	p.responseHeaders = officialResponseEnabled(global)
	for _, f := range claudeCustomizationRegistry {
		if f.Group == ClaudeFieldGroupOfficialAlignment && f.get(&global) == ClaudeAlignReject {
			p.reject[f.Key] = true
		}
	}
	var overrides []ClaudeAlignmentOverrides
	var err error
	if reader, ok := s.settingRepo.(alignmentOverrideReader); ok {
		overrides, err = reader.ListClaudeAlignmentOverrides(ctx, nativeClaudeAlignmentAccountTypes)
	} else {
		err = errors.New("alignment override reader unavailable")
	}
	if err != nil {
		if shouldLogClaudePolicyFailure(&s.alignmentOverrideErrorLogAt) {
			slog.WarnContext(ctx, "alignment override lookup failed", "error", err)
		}
	} else {
		p.accounts = make(map[string]map[int64]ClaudeCustomizationSettings)
		for _, account := range overrides {
			policy := global
			applyClaudeAccountOverrides(&policy, account.Fields, account.AccountType)
			for _, f := range claudeCustomizationRegistry {
				headerSwitch := (f.Key == ClaudeFieldRequestIDMode && policy.RequestIDMode == ClaudeRequestIDFill) ||
					(f.Key == ClaudeFieldUpstreamHeaderPassthrough && policy.UpstreamHeaderPassthrough == ClaudeHeaderPassthroughOn)
				if account.Platform == PlatformAntigravity && f.Key != ClaudeFieldUnknownBetaHeader && !headerSwitch {
					continue
				}
				if slices.Contains(f.AccountTypes, account.AccountType) && (headerSwitch || f.get(&policy) == ClaudeAlignReject) {
					if headerSwitch {
						p.responseHeaders = true
					} else {
						p.reject[f.Key] = true
					}
					if p.accounts[f.Key] == nil {
						p.accounts[f.Key] = make(map[int64]ClaudeCustomizationSettings)
					}
					p.accounts[f.Key][account.AccountID] = policy
				}
			}
		}
	}
	if reader, ok := s.settingRepo.(alignmentRoutingReader); ok && (p.responseHeaders || len(p.reject) > 0) {
		var routingErr error
		p.routing, routingErr = reader.ClaudeAlignmentRouting(ctx)
		err = errors.Join(err, routingErr)
		p.routingUnavailable = routingErr != nil || p.routing == nil
		if !p.routingUnavailable {
			p.routing.accounts = make(map[int64]ClaudeAlignmentOverrides, len(overrides))
			for _, a := range overrides {
				p.routing.accounts[a.AccountID] = a
			}
		} else if shouldLogClaudePolicyFailure(&s.alignmentCandidateErrorLogAt) {
			slog.WarnContext(ctx, "alignment routing snapshot unavailable", "error", err)
		}
	}
	ttl := 10 * time.Second
	var betaErr error
	if s.settingRepo != nil {
		var settings *BetaPolicySettings
		settings, betaErr = s.GetBetaPolicySettings(ctx)
		if betaErr == nil {
			p.betaRules = settings.Rules
			p.betaRulesLoaded = true
		} else if shouldLogClaudePolicyFailure(&s.unknownBetaRuleErrorLogAt) {
			slog.WarnContext(ctx, "unknown beta rule lookup failed; allowing request", "source", "alignment_snapshot", "error", betaErr)
		}
	}
	if globalErr != nil || err != nil || betaErr != nil {
		ttl = time.Second
	}
	if ctx.Err() != nil {
		if stale := s.alignmentRejectCache.Load(); stale != nil {
			if betaErr == nil {
				return stale
			}
			failedRules := *stale
			failedRules.betaRules, failedRules.betaRulesLoaded = nil, false
			p = &failedRules
		} else {
			// Cold waiters share the default ping behavior until the short retry TTL.
			p = &alignmentRejectSnapshot{global: DefaultClaudeCustomizationSettings()}
		}
		ttl = time.Second
	}
	p.expiresAt = time.Now().Add(ttl)
	s.alignmentRejectCache.Store(p)
	return p
}
