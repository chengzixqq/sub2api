package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type unifiedObservationRepo struct {
	collectorObservationRepo
	policy ChannelMonitorObservationConfig
	facts  []ChannelMonitorObservationFact
}

func (r *unifiedObservationRepo) GetConfig(context.Context) (*ChannelMonitorObservationConfig, error) {
	cfg := r.policy
	return &cfg, nil
}
func (r *unifiedObservationRepo) Query(_ context.Context, f ChannelMonitorV2Filter) (*ChannelMonitorObservationSnapshot, error) {
	out := &ChannelMonitorObservationSnapshot{Coverage: ChannelMonitorObservationCoverage{State: "complete", CollectorState: "healthy", DataThrough: time.Now().UTC()}}
	for _, fact := range r.facts {
		if observationHasID(f.AllowedGroupIDs, fact.GroupID) && !fact.BucketStart.Before(f.Start) && fact.BucketStart.Before(f.End) {
			out.Facts = append(out.Facts, fact)
		}
	}
	return out, nil
}

type unifiedGroups struct {
	GroupRepository
	items []Group
}

func (r unifiedGroups) ListActive(context.Context) ([]Group, error) { return r.items, nil }

func TestObservationOverview_SameSourceAndAuthorizedCatalogue(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	policy := DefaultChannelMonitorObservationConfig()
	policy.Mode = "live"
	repo := &unifiedObservationRepo{policy: policy, facts: []ChannelMonitorObservationFact{
		{Source: "traffic", GroupID: 7, Model: "visible", BucketStart: now.Add(-time.Minute), SuccessRequests: 100},
		{Source: "traffic", GroupID: 7, Model: "secret", BucketStart: now.Add(-time.Minute), SuccessRequests: 500},
		{Source: "probe", GroupID: 7, Model: "visible", BucketStart: now.Add(-time.Minute), SuccessRequests: 500},
		{Source: "traffic", GroupID: 9, Model: "private", BucketStart: now.Add(-time.Minute), SuccessRequests: 500},
	}}
	legacy := &channelMonitorV2RepoStub{config: ChannelMonitorV2Config{Enabled: true, Platforms: []ChannelMonitorV2PlatformConfig{{Platform: PlatformOpenAI, Enabled: true}}}, matrix: &ChannelMonitorV2Matrix{}}
	groups := unifiedGroups{items: []Group{{ID: 7, Platform: PlatformOpenAI}, {ID: 9, Platform: PlatformOpenAI}}}
	svc := NewChannelMonitorOverviewService(repo, legacy, groups, nil)
	svc.SetModelCatalogue(func(context.Context, *Group) ([]string, error) { return []string{"visible", "idle"}, nil })
	f := ChannelMonitorV2Filter{Start: now.Add(-24 * time.Hour), End: now, Bucket: 5 * time.Minute, Range: "24h", RestrictGroups: true, AllowedGroupIDs: []int64{7}}
	public, err := svc.Overview(context.Background(), f, false, 42, false)
	require.NoError(t, err)
	admin, err := svc.Overview(context.Background(), f, true, 1, false)
	require.NoError(t, err)
	require.Equal(t, "compact", public.Source)
	require.Equal(t, public.Source, admin.Source)
	require.Len(t, public.Items, 1)
	require.Len(t, public.Items[0].Models, 2)
	require.EqualValues(t, 100, admin.Items[0].Metrics.RequestCount)
	require.Zero(t, public.Items[0].Metrics.RequestCount)
	require.Equal(t, admin.Items[0].CurrentStatus, public.Items[0].CurrentStatus)
	f.Models = []string{"idle"}
	filtered, err := svc.Overview(context.Background(), f, false, 42, false)
	require.NoError(t, err)
	require.Len(t, filtered.Dimensions.Models, 2)
	require.Len(t, filtered.Items, 1)
	require.Len(t, filtered.Items[0].Models, 1)
	require.Equal(t, "idle", filtered.Items[0].Models[0].Model)
}

func TestObservationSource_ShadowAndGroupRollout(t *testing.T) {
	cfg := DefaultChannelMonitorObservationConfig()
	require.Equal(t, "legacy", observationSourceForGroup(cfg, 7, false))
	require.Equal(t, "compact", observationSourceForGroup(cfg, 7, true))
	cfg.Mode, cfg.LiveGroupIDs = "live", []int64{7}
	require.Equal(t, "compact", observationSourceForGroup(cfg, 7, false))
	require.Equal(t, "legacy", observationSourceForGroup(cfg, 8, false))
	cfg.Enabled = false
	require.Equal(t, "compact", observationSourceForGroup(cfg, 7, false))
}

func TestObservationOverview_CompactLiveIgnoresLegacyCollectionFilters(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	policy := DefaultChannelMonitorObservationConfig()
	policy.Mode = "live"
	policy.LiveGroupIDs = []int64{7}
	repo := &unifiedObservationRepo{policy: policy, facts: []ChannelMonitorObservationFact{{
		Source: "traffic", GroupID: 7, Model: "visible", BucketStart: now.Add(-time.Minute), SuccessRequests: 1,
	}}}
	legacy := &channelMonitorV2RepoStub{config: ChannelMonitorV2Config{
		Enabled:   false,
		GroupIDs:  []int64{9},
		Platforms: []ChannelMonitorV2PlatformConfig{{Platform: PlatformOpenAI, Enabled: false}},
	}}
	svc := NewChannelMonitorOverviewService(repo, legacy, unifiedGroups{items: []Group{{ID: 7, Platform: PlatformOpenAI}}}, nil)
	svc.SetModelCatalogue(func(context.Context, *Group) ([]string, error) { return []string{"visible"}, nil })
	out, err := svc.Overview(context.Background(), ChannelMonitorV2Filter{
		Start: now.Add(-time.Hour), End: now, Bucket: time.Minute, RestrictGroups: true, AllowedGroupIDs: []int64{7},
	}, false, 42, false)
	require.NoError(t, err)
	require.Len(t, out.Items, 1)
	require.Equal(t, "compact", out.Items[0].Source)
}

func TestObservationCurrentStatus_RecentTrafficOnlyAndCollectorFailure(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	cfg := DefaultChannelMonitorObservationConfig()
	facts := []ChannelMonitorObservationFact{
		{BucketStart: now.Add(-time.Hour), SuccessRequests: 1000},
		{BucketStart: now.Add(-time.Minute), ChannelErrors: 50},
	}
	status := observationCurrentStatus(facts, cfg, "openai", 7, "m", now, "healthy")
	require.Equal(t, "critical", status.State)
	require.Equal(t, "traffic", status.Source)
	status = observationCurrentStatus(facts, cfg, "openai", 7, "m", now, "write_failed")
	require.Equal(t, "unknown", status.State)
	require.Equal(t, "collection_unavailable", status.Reason)
}

func TestObservationOverview_UnknownFailuresApplyToHistoricalAndCurrentMetrics(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	policy := DefaultChannelMonitorObservationConfig()
	policy.Mode = "live"
	repo := &unifiedObservationRepo{policy: policy, facts: []ChannelMonitorObservationFact{
		{Source: "traffic", GroupID: 7, Model: "visible", BucketStart: now.Add(-30 * time.Minute), SuccessRequests: 100, UnknownRequests: 10, ErrorCategories: map[string]int64{"empty_output": 10}},
		{Source: "traffic", GroupID: 7, Model: "visible", BucketStart: now.Add(-time.Minute), SuccessRequests: 50, UnknownRequests: 50, ErrorCategories: map[string]int64{"incomplete_stream": 50}},
	}}
	svc := NewChannelMonitorOverviewService(repo, &channelMonitorV2RepoStub{}, unifiedGroups{items: []Group{{ID: 7, Platform: PlatformOpenAI}}}, nil)
	svc.SetModelCatalogue(func(context.Context, *Group) ([]string, error) { return []string{"visible"}, nil })
	filter := ChannelMonitorV2Filter{Start: now.Add(-time.Hour), End: now, Bucket: 5 * time.Minute, RestrictGroups: true, AllowedGroupIDs: []int64{7}}
	out, err := svc.Overview(context.Background(), filter, true, 1, false)
	require.NoError(t, err)
	require.Equal(t, "complete", out.Coverage.State)
	require.True(t, out.Coverage.CoverageComplete)
	require.NotContains(t, out.Coverage.GapReasons, "unknown_outcome")
	require.Len(t, out.Items, 1)
	channel := out.Items[0]
	require.Len(t, channel.Models, 1)
	model := channel.Models[0]
	for _, metrics := range []ObservationMetrics{channel.Metrics, channel.Traffic, model.Metrics} {
		require.EqualValues(t, 210, metrics.RequestCount)
		require.EqualValues(t, 150, metrics.SuccessRequests)
		require.EqualValues(t, 60, metrics.ChannelErrors)
		require.Zero(t, metrics.UnknownRequests)
		require.InDelta(t, 150.0/210, *metrics.ReliabilityRate, 1e-9)
		require.InDelta(t, 150.0/210, *metrics.SuccessRate, 1e-9)
		require.InDelta(t, 210.0/60, metrics.RPM, 1e-9)
		require.Equal(t, map[string]int64{"empty_output": 10, "incomplete_stream": 50}, metrics.ErrorCategories)
	}
	require.Equal(t, "critical", channel.Health.Reliability)
	require.Equal(t, channel.Health, model.Health)
	require.Equal(t, "critical", channel.CurrentStatus.State)
	require.Equal(t, "recent_traffic", channel.CurrentStatus.Reason)
	require.Equal(t, channel.CurrentStatus, model.CurrentStatus)
	require.Equal(t, channel.Buckets, model.Buckets)
	var requests, errors int64
	for _, bucket := range channel.Buckets {
		requests += bucket.Metrics.RequestCount
		errors += bucket.Metrics.ChannelErrors
		require.Zero(t, bucket.Metrics.UnknownRequests)
		if bucket.Metrics.RequestCount > 0 {
			require.InDelta(t, float64(bucket.Metrics.SuccessRequests)/float64(bucket.Metrics.RequestCount), *bucket.Metrics.ReliabilityRate, 1e-9)
		}
	}
	require.Equal(t, channel.Metrics.RequestCount, requests)
	require.Equal(t, channel.Metrics.ChannelErrors, errors)
	require.EqualValues(t, 10, repo.facts[0].UnknownRequests)
	require.EqualValues(t, 50, repo.facts[1].UnknownRequests)
}

func TestObservationCurrentStatus_UnknownOnlyTrafficIsCritical(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	cfg := DefaultChannelMonitorObservationConfig()
	facts := []ChannelMonitorObservationFact{{Source: "traffic", BucketStart: now.Add(-time.Minute), UnknownRequests: cfg.MinimumSample}}
	status := observationCurrentStatus(facts, cfg, "openai", 7, "m", now, "healthy")
	require.Equal(t, "critical", status.State)
	require.Equal(t, "traffic", status.Source)
	require.Equal(t, "recent_traffic", status.Reason)
}

func TestObservationCurrentStatus_ProbeNeverEntersTraffic(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	facts := []ChannelMonitorObservationFact{{Source: "probe", BucketStart: now.Add(-time.Minute), SuccessRequests: 100}}
	status := observationCurrentStatus(facts, DefaultChannelMonitorObservationConfig(), "openai", 7, "m", now, "healthy")
	require.Equal(t, "unknown", status.State)
	require.Equal(t, "none", status.Source)
}

func TestObservationGroupProbe_DoesNotClaimUntestedModelsHealthy(t *testing.T) {
	now := time.Now().UTC()
	models := []ObservationModel{{Model: "tested", Probe: &ObservationProbeStatus{Status: "healthy", LastCheckedAt: &now}}, {Model: "untested"}}
	require.Nil(t, observationGroupProbe(models))
	models[0].Probe.Status = "critical"
	require.Equal(t, "critical", observationGroupProbe(models).Status)
}

func TestObservationProbe_CollectionFailureRemainsUnknown(t *testing.T) {
	status := ObservationCurrentStatus{State: "unknown", Source: "none", Reason: "collection_unavailable"}
	applyObservationProbe(&status, &ObservationProbeStatus{Status: "healthy"})
	require.Equal(t, "unknown", status.State)
	require.Equal(t, "collection_unavailable", status.Reason)
}

func TestObservationProbeEvidence_DoesNotMaskFailedProtocolWithAnotherProtocol(t *testing.T) {
	now := time.Now().UTC()
	failed, succeeded := false, true
	accountOne, accountTwo := int64(1), int64(2)
	firstFailure := now.Add(-3 * time.Minute)
	secondFailure := now.Add(-2 * time.Minute)
	success := now.Add(-time.Minute)
	status := observationProbeEvidence(ChannelMonitorProbeEvidence{
		Targets: []ChannelMonitorProbeTarget{{ID: 1, GroupID: 7, Model: "model", Protocol: "anthropic", Enabled: true}, {ID: 2, GroupID: 7, Model: "model", Protocol: "openai_chat", Enabled: true}},
		Runs: []ChannelMonitorProbeRun{
			{TargetID: 1, GroupID: 7, Model: "model", Protocol: "anthropic", Success: &failed, AccountID: &accountOne, CompletedAt: &firstFailure},
			{TargetID: 1, GroupID: 7, Model: "model", Protocol: "anthropic", Success: &failed, AccountID: &accountOne, CompletedAt: &secondFailure},
			{TargetID: 2, GroupID: 7, Model: "model", Protocol: "openai_chat", Success: &succeeded, AccountID: &accountTwo, CompletedAt: &success},
		},
	}, 7, "model", now)
	require.NotNil(t, status)
	require.Equal(t, "critical", status.Status)
}

type unavailableQuotaRepo struct{ ChannelMonitorProbeRepository }

func (unavailableQuotaRepo) RecentRuns(context.Context, []int64, time.Time) ([]ChannelMonitorProbeRun, error) {
	return nil, nil
}
func (unavailableQuotaRepo) ListTargets(context.Context) ([]ChannelMonitorProbeTarget, error) {
	return nil, nil
}
func (unavailableQuotaRepo) QuotaSummaries(context.Context, []int64, time.Time, time.Duration) (map[int64]ChannelMonitorQuotaSummary, error) {
	return nil, errors.New("quota unavailable")
}

func TestObservationQuota_FailureDoesNotChangeTrafficHealth(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	cfg := DefaultChannelMonitorObservationConfig()
	repo := &unifiedObservationRepo{policy: cfg, facts: []ChannelMonitorObservationFact{{Source: "traffic", GroupID: 7, Model: "visible", BucketStart: now.Add(-time.Minute), SuccessRequests: 100}}}
	svc := NewChannelMonitorOverviewService(repo, nil, nil, nil)
	svc.SetProbeService(NewChannelMonitorProbeService(unavailableQuotaRepo{}, nil, nil, nil))
	out := &ObservationOverview{Items: []ObservationChannel{{Source: "compact", GroupID: 7, Platform: PlatformOpenAI, Models: []ObservationModel{{Model: "visible"}}}}}
	err := svc.fillCurrentStatuses(context.Background(), out, ChannelMonitorV2Filter{AllowedGroupIDs: []int64{7}, RestrictGroups: true}, cfg, map[int64]Group{7: {ID: 7}}, nil, false, 42, false)
	require.NoError(t, err)
	require.Equal(t, "healthy", out.Items[0].CurrentStatus.State)
	require.Equal(t, "unknown", out.Items[0].Quota.Status)
	require.Contains(t, out.Coverage.GapReasons, "quota_unavailable")
}

func TestObservationConfig_RequiresOwnerScope(t *testing.T) {
	svc := NewChannelMonitorOverviewService(&unifiedObservationRepo{policy: DefaultChannelMonitorObservationConfig()}, nil, nil, nil)
	_, err := svc.UpdateConfig(context.Background(), DefaultChannelMonitorObservationConfig())
	require.Error(t, err)
	_, err = svc.GetConfig(context.Background())
	require.Error(t, err)
}
