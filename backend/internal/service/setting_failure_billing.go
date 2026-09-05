package service

import (
	"context"
	"log/slog"
	"time"
)

// cachedFailureBillingUpstreamUsageOnly is the process-local snapshot used by
// request settlement guards. The policy is intentionally cached per
// SettingService so tests and multiple service instances do not share state.
type cachedFailureBillingUpstreamUsageOnly struct {
	value     bool
	expiresAt int64 // unix nano
}

const (
	failureBillingUpstreamUsageOnlyCacheTTL  = 60 * time.Second
	failureBillingUpstreamUsageOnlyErrorTTL  = 5 * time.Second
	failureBillingUpstreamUsageOnlyDBTimeout = 5 * time.Second
	failureBillingUpstreamUsageOnlyCacheKey  = "failure_billing_upstream_usage_only"
)

// GetFailureBillingUpstreamUsageOnlyCached returns the current failure billing
// policy without blocking the gateway hot path after the first load. Missing or
// unreadable settings fail closed to the historical behavior (false).
func (s *SettingService) GetFailureBillingUpstreamUsageOnlyCached(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return false
	}
	now := time.Now().UnixNano()
	if cached, ok := s.failureBillingUpstreamUsageOnlyCache.Load().(*cachedFailureBillingUpstreamUsageOnly); ok && cached != nil && now < cached.expiresAt {
		return cached.value
	}

	value, _, _ := s.failureBillingUpstreamUsageOnlySF.Do(failureBillingUpstreamUsageOnlyCacheKey, func() (any, error) {
		if cached, ok := s.failureBillingUpstreamUsageOnlyCache.Load().(*cachedFailureBillingUpstreamUsageOnly); ok && cached != nil && time.Now().UnixNano() < cached.expiresAt {
			return cached.value, nil
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), failureBillingUpstreamUsageOnlyDBTimeout)
		defer cancel()
		raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyFailureBillingUpstreamUsageOnly)
		if err != nil {
			slog.Warn("failed to get failure billing policy", "error", err)
			s.failureBillingUpstreamUsageOnlyCache.Store(&cachedFailureBillingUpstreamUsageOnly{
				value:     false,
				expiresAt: time.Now().Add(failureBillingUpstreamUsageOnlyErrorTTL).UnixNano(),
			})
			return false, err
		}
		parsed := raw == "true"
		s.failureBillingUpstreamUsageOnlyCache.Store(&cachedFailureBillingUpstreamUsageOnly{
			value:     parsed,
			expiresAt: time.Now().Add(failureBillingUpstreamUsageOnlyCacheTTL).UnixNano(),
		})
		return parsed, nil
	})
	if parsed, ok := value.(bool); ok {
		return parsed
	}
	return false
}

// invalidateFailureBillingUpstreamUsageOnlyCache updates the policy snapshot
// immediately after a successful settings write.
func (s *SettingService) invalidateFailureBillingUpstreamUsageOnlyCache(settings *SystemSettings) {
	if s == nil {
		return
	}
	s.failureBillingUpstreamUsageOnlySF.Forget(failureBillingUpstreamUsageOnlyCacheKey)
	value := false
	if settings != nil {
		value = settings.FailureBillingUpstreamUsageOnly
	}
	s.failureBillingUpstreamUsageOnlyCache.Store(&cachedFailureBillingUpstreamUsageOnly{
		value:     value,
		expiresAt: time.Now().Add(failureBillingUpstreamUsageOnlyCacheTTL).UnixNano(),
	})
}
