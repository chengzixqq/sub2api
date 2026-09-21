//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestChannelMonitorProbeQuota_NeverGeneratesForOAuthPlatforms(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformGrok, PlatformAnthropic, PlatformGemini, PlatformAntigravity} {
		t.Run(platform, func(t *testing.T) {
			usage := &stubMonitorUsageSource{}
			fetcher := newQuotaModeFetcher(map[int64]*Account{1: {ID: 1, Platform: platform, Type: AccountTypeOAuth}}, usage)
			result := fetcher.FetchReadOnly(context.Background(), 1)
			require.False(t, result.Success)
			require.Equal(t, 0, usage.getCalls())
		})
	}
}
func TestChannelMonitorProbeQuota_ObservedAnthropicSnapshotKeepsSampleTime(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	reset := now.Add(time.Hour)
	account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, SessionWindowEnd: &reset, Extra: map[string]any{"session_window_utilization": 0.4, "passive_usage_sampled_at": now.Format(time.RFC3339)}}
	fetcher := newQuotaModeFetcher(map[int64]*Account{1: account}, nil)
	result := fetcher.FetchReadOnly(context.Background(), 1)
	require.True(t, result.Success)
	require.Equal(t, now, result.FetchedAt)
	require.NotEmpty(t, result.Tiers)
	account.Extra["passive_usage_sampled_at"] = now.Add(-time.Hour).Format(time.RFC3339)
	result = fetcher.FetchReadOnly(context.Background(), 1)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "stale")
}
func TestChannelMonitorProbeService_RequiresOwnerScopeBeforeDependencies(t *testing.T) {
	svc := NewChannelMonitorProbeService(nil, nil, nil, nil)
	for _, ctx := range []context.Context{context.Background(), WithScope(context.Background(), Scope{WorkspaceID: 1})} {
		_, err := svc.ListTargets(ctx)
		require.Error(t, err)
		_, err = svc.Budget(ctx)
		require.Error(t, err)
		err = svc.SaveTarget(ctx, &ChannelMonitorProbeTarget{})
		require.Error(t, err)
		_, err = svc.Probe(ctx, 1, "operation-12345678")
		require.Error(t, err)
		_, err = svc.ReadQuotas(ctx, []int64{1})
		require.Error(t, err)
	}
}
