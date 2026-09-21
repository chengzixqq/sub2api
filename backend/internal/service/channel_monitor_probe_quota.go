package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// FetchReadOnly never calls AccountUsageService.GetUsage: that path can generate
// billable Codex/Grok requests or mutate account scheduling state. CN metadata
// queries only persist quota snapshots; other providers reuse observed headers.
func (f *ChannelMonitorQuotaFetcher) FetchReadOnly(ctx context.Context, id int64) *domain.MonitorQuotaSnapshot {
	now := time.Now().UTC()
	a, err := f.LoadAccount(ctx, id)
	if err != nil || a == nil {
		return quotaErrorSnapshot("quota", "account unavailable", now)
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	switch a.Platform {
	case PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax:
		if a.IsCodingPlan() {
			return f.fetchCNQuota(callCtx, a, now)
		}
		return f.fetchCNBalance(callCtx, a, now)
	case PlatformOpenCodeGo:
		return f.fetchCNQuota(callCtx, a, now)
	}
	u := &UsageInfo{}
	var observed time.Time
	switch a.Platform {
	case PlatformOpenAI:
		applyExtraToUsage(u, a.Extra, now)
		if raw := a.GetExtraString("codex_usage_updated_at"); raw != "" {
			observed, _ = time.Parse(time.RFC3339Nano, raw)
		}
	case PlatformAnthropic:
		if raw, ok := a.Extra["session_window_utilization"].(float64); ok && a.SessionWindowEnd != nil {
			u.FiveHour = &UsageProgress{Utilization: raw * 100, ResetsAt: a.SessionWindowEnd}
		}
		u.SevenDay = buildPassiveUsageWindow(a.Extra, "passive_usage_7d_utilization", "passive_usage_7d_reset")
		if raw := a.GetExtraString("passive_usage_sampled_at"); raw != "" {
			observed, _ = time.Parse(time.RFC3339Nano, raw)
		}
	default:
		return quotaErrorSnapshot("observed", "read-only quota unavailable for this provider", now)
	}
	tiers := usageQuotaTiers(u)
	if len(tiers) == 0 || observed.IsZero() {
		return quotaErrorSnapshot("observed", "no observed quota snapshot", now)
	}
	snapshot := &domain.MonitorQuotaSnapshot{Source: "observed", Success: now.Sub(observed) <= 10*time.Minute, Tiers: tiers, FetchedAt: observed}
	if !snapshot.Success {
		snapshot.Error = "observed quota snapshot stale"
	}
	return snapshot
}
