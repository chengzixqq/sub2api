package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestObservationLegacyLatency_PreservesVisibleModelEvidenceWithoutInventingGroupPercentiles(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	id := int64(7)
	p50 := int64(2000)
	cfg := DefaultChannelMonitorObservationConfig()
	cfg.Overrides = []ChannelMonitorObservationOverride{{GroupID: id, Model: "visible", WarningTTFTMs: 1000, CriticalTTFTMs: 1500}}
	metric := ChannelMonitorV2Metric{SuccessRequests: 100, TTFT: ChannelMonitorV2Latency{P50Ms: &p50, SampleCount: 100}}
	legacy := &channelMonitorV2RepoStub{config: ChannelMonitorV2Config{Enabled: true}, matrix: &ChannelMonitorV2Matrix{
		Coverage: ChannelMonitorV2Coverage{DataThrough: now},
		Items:    []ChannelMonitorV2MatrixRow{{GroupID: &id, Model: "visible", Metrics: metric, Buckets: []ChannelMonitorV2TrendPoint{{BucketStart: now.Add(-time.Minute), Metrics: metric}}}},
	}}
	svc := NewChannelMonitorOverviewService(&unifiedObservationRepo{policy: cfg}, legacy, nil, nil)
	groups := map[int64]Group{id: {ID: id, Platform: PlatformOpenAI}}
	catalogue := map[int64]map[string]bool{id: {"visible": true}}
	f := ChannelMonitorV2Filter{AllowedGroupIDs: []int64{id}, RestrictGroups: true, Start: now.Add(-5 * time.Minute), End: now, Bucket: time.Minute}
	rows, _, err := svc.legacyOverview(context.Background(), f, legacy.config, cfg, groups, catalogue, false)
	require.NoError(t, err)
	require.Equal(t, "critical", rows[0].Health.Latency)
	require.Equal(t, "critical", rows[0].Models[0].Health.Latency)
	require.Equal(t, "critical", rows[0].Buckets[len(rows[0].Buckets)-1].Health.Latency)
	require.Nil(t, rows[0].Metrics.TTFT.P50Ms, "model quantiles cannot be merged into an exact group quantile")
	require.EqualValues(t, 2000, *rows[0].Models[0].Metrics.TTFT.P50Ms)
	require.Zero(t, rows[0].Models[0].Metrics.TTFT.SampleCount, "public projection must not expose counts")
	out := &ObservationOverview{Items: rows}
	f.AllowedGroupIDs = nil
	require.NoError(t, svc.fillCurrentStatuses(context.Background(), out, f, cfg, groups, catalogue, false, 42, false))
	require.Equal(t, "warning", out.Items[0].CurrentStatus.State)
	require.Equal(t, "high_latency", out.Items[0].CurrentStatus.Reason)
	require.Equal(t, "warning", out.Items[0].Models[0].CurrentStatus.State)
}

func TestObservationLegacyLatency_HiddenAndInsufficientEvidenceDoesNotAffectGroup(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	id := int64(7)
	p50 := int64(20000)
	cfg := DefaultChannelMonitorObservationConfig()
	legacy := &channelMonitorV2RepoStub{config: ChannelMonitorV2Config{Enabled: true}, matrix: &ChannelMonitorV2Matrix{
		Coverage: ChannelMonitorV2Coverage{DataThrough: now},
		Items: []ChannelMonitorV2MatrixRow{
			{GroupID: &id, Model: "visible", Metrics: ChannelMonitorV2Metric{SuccessRequests: 100, TTFT: ChannelMonitorV2Latency{P50Ms: &p50, SampleCount: 1}}},
			{GroupID: &id, Model: "hidden", Metrics: ChannelMonitorV2Metric{SuccessRequests: 100, TTFT: ChannelMonitorV2Latency{P50Ms: &p50, SampleCount: 100}}},
		},
	}}
	svc := NewChannelMonitorOverviewService(&unifiedObservationRepo{policy: cfg}, legacy, nil, nil)
	out := &ObservationOverview{Items: []ObservationChannel{{Source: "legacy", GroupID: id, Platform: PlatformOpenAI, Models: []ObservationModel{{Model: "visible"}}}}}
	groups := map[int64]Group{id: {ID: id, Platform: PlatformOpenAI}}
	require.NoError(t, svc.fillCurrentStatuses(context.Background(), out, ChannelMonitorV2Filter{}, cfg, groups, map[int64]map[string]bool{id: {"visible": true}}, false, 42, false))
	require.Equal(t, "healthy", out.Items[0].CurrentStatus.State)
	require.Equal(t, "healthy", out.Items[0].Models[0].CurrentStatus.State)
	legacy.matrix.Coverage.DataThrough = now.Add(-time.Hour)
	require.NoError(t, svc.fillCurrentStatuses(context.Background(), out, ChannelMonitorV2Filter{}, cfg, groups, nil, false, 42, true))
	require.Equal(t, "unknown", out.Items[0].CurrentStatus.State)
}

func TestObservationLegacyStatus_DoesNotMixShadowCompactTraffic(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	id := int64(7)
	cfg := DefaultChannelMonitorObservationConfig()
	repo := &unifiedObservationRepo{policy: cfg, facts: []ChannelMonitorObservationFact{{Source: "traffic", GroupID: id, Model: "visible", BucketStart: now.Add(-time.Minute), SuccessRequests: 10000}}}
	legacy := &channelMonitorV2RepoStub{config: ChannelMonitorV2Config{Enabled: true}, matrix: &ChannelMonitorV2Matrix{
		Coverage: ChannelMonitorV2Coverage{DataThrough: now},
		Items:    []ChannelMonitorV2MatrixRow{{GroupID: &id, Model: "visible", Metrics: ChannelMonitorV2Metric{ErrorRequests: 50}}},
	}}
	svc := NewChannelMonitorOverviewService(repo, legacy, nil, nil)
	out := &ObservationOverview{Items: []ObservationChannel{{Source: "legacy", GroupID: id, Platform: PlatformOpenAI, Models: []ObservationModel{{Model: "visible"}}}}}
	f := ChannelMonitorV2Filter{AllowedGroupIDs: []int64{id}, RestrictGroups: true}
	require.NoError(t, svc.fillCurrentStatuses(context.Background(), out, f, cfg, map[int64]Group{id: {ID: id, Platform: PlatformOpenAI}}, nil, false, 42, false))
	require.Equal(t, "critical", out.Items[0].CurrentStatus.State)
	require.Equal(t, "critical", out.Items[0].Models[0].CurrentStatus.State)
}
