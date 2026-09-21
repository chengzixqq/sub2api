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
func (unavailableQuotaRepo) QuotaSummaries(context.Context, []int64, time.Time) (map[int64]ChannelMonitorQuotaSummary, error) {
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
