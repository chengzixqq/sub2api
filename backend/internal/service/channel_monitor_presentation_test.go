package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMonitorPresentation_DefaultsCompatibilityAndValidation(t *testing.T) {
	cfg := DefaultChannelMonitorObservationConfig()
	require.Equal(t, DefaultChannelMonitorPresentationDefaults(), cfg.DisplayDefaults())
	require.NoError(t, ValidateChannelMonitorObservationConfig(cfg))
	for _, change := range []func(*ChannelMonitorPresentationDefaults){
		func(p *ChannelMonitorPresentationDefaults) { p.Columns = "500" },
		func(p *ChannelMonitorPresentationDefaults) { p.HistoryMode = "unknown" },
		func(p *ChannelMonitorPresentationDefaults) { p.HistoryBucketMinutes = 0 },
		func(p *ChannelMonitorPresentationDefaults) { p.HistoryBucketMinutes = 43201 },
		func(p *ChannelMonitorPresentationDefaults) { p.HistoryBarCount = 721 },
		func(p *ChannelMonitorPresentationDefaults) { p.DefaultRange = "forever" },
	} {
		p := DefaultChannelMonitorPresentationDefaults()
		change(&p)
		cfg.PresentationDefaults = &p
		require.Error(t, ValidateChannelMonitorObservationConfig(cfg))
	}
}
func TestMonitorPresentation_PublicOverviewIncludesDefaultsWithoutSamples(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	policy := DefaultChannelMonitorObservationConfig()
	policy.Version = 8
	policy.Mode = "live"
	defaults := DefaultChannelMonitorPresentationDefaults()
	defaults.Columns = "3"
	defaults.HistoryMode = "count"
	defaults.HistoryBarCount = 72
	policy.PresentationDefaults = &defaults
	repo := &unifiedObservationRepo{policy: policy}
	legacy := &channelMonitorV2RepoStub{config: ChannelMonitorV2Config{Enabled: true}}
	svc := NewChannelMonitorOverviewService(repo, legacy, unifiedGroups{}, nil)
	f := ChannelMonitorV2Filter{Start: now.Add(-time.Hour), End: now, Bucket: 5 * time.Minute, Range: "24h"}
	for _, admin := range []bool{false, true} {
		result, err := svc.Overview(context.Background(), f, admin, 1, false)
		require.NoError(t, err)
		require.Equal(t, defaults, result.PresentationDefaults)
		require.Equal(t, policy.Version, result.PresentationDefaultsVersion)
		require.Empty(t, result.Items)
	}
	// Metadata only reads policy, so it also works without query/legacy/group dependencies.
	metadata, version, err := NewChannelMonitorOverviewService(repo, nil, nil, nil).PresentationDefaults(context.Background())
	require.NoError(t, err)
	require.Equal(t, defaults, metadata)
	require.Equal(t, policy.Version, version)
}
