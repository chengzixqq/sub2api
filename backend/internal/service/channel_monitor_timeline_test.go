package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestObservationTimeline_UTCOriginAndClippedEdges(t *testing.T) {
	at := time.Date(2026, 10, 12, 9, 17, 0, 0, time.FixedZone("UTC+8", 8*3600))
	for _, bucket := range []time.Duration{5 * time.Minute, 35 * time.Minute, 75 * time.Minute, 9 * time.Hour, 36 * time.Hour, 30 * 24 * time.Hour} {
		got := ChannelMonitorObservationBucketStart(at, bucket)
		require.Equal(t, int64(0), got.Unix()%int64(bucket/time.Second))
		require.False(t, got.After(at))
		require.True(t, got.Add(bucket).After(at))
		require.Equal(t, time.UTC, got.Location())
	}
	start := time.Date(2026, 10, 12, 0, 7, 0, 0, time.UTC)
	f := ChannelMonitorV2Filter{Start: start, End: start.Add(90 * time.Minute), Bucket: 15 * time.Minute}
	_, _, buckets := observationSeries(nil, DefaultChannelMonitorObservationConfig(), "openai", 7, "m", f, true)
	require.Len(t, buckets, 7)
	require.Equal(t, start.Add(-7*time.Minute), buckets[0].BucketStart)
	for _, b := range buckets {
		require.Equal(t, "no_samples", b.Metrics.SampleState)
		require.Equal(t, "unknown", b.Health.Reliability)
	}
}

func TestObservationTimeline_SampleWindowsDescribeIncludedIntervalsNotEventTimes(t *testing.T) {
	start := time.Date(2026, 10, 12, 0, 7, 0, 0, time.UTC)
	f := ChannelMonitorV2Filter{Start: start, End: start.Add(90 * time.Minute), Bucket: 15 * time.Minute, RollupBucket: time.Minute}
	_, _, buckets := observationSeries(nil, DefaultChannelMonitorObservationConfig(), "openai", 7, "m", f, true)
	annotateObservationSampleWindows(buckets, f, f.Start, f.End)
	require.Equal(t, f.Start, *buckets[0].SampleStart)
	require.Equal(t, time.Date(2026, 10, 12, 0, 15, 0, 0, time.UTC), *buckets[0].SampleEnd)
	require.Equal(t, f.End, *buckets[len(buckets)-1].SampleEnd)
	// Older partial source hours stay uncovered rather than becoming fabricated samples.
	f.Bucket = 30 * time.Minute
	_, _, buckets = observationSeries(nil, DefaultChannelMonitorObservationConfig(), "openai", 7, "m", f, true)
	annotateObservationSampleWindows(buckets, f, start.Truncate(time.Hour).Add(time.Hour), f.End.Truncate(time.Hour))
	require.Nil(t, buckets[0].SampleStart)
}

func TestObservationTimeline_RealSamplesAndHistogramsAreMergedWithoutDroppingErrors(t *testing.T) {
	start := ChannelMonitorObservationBucketStart(time.Now().UTC(), 15*time.Minute)
	cfg := DefaultChannelMonitorObservationConfig()
	cfg.MinimumSample = 1
	facts := []ChannelMonitorObservationFact{
		{BucketStart: start, SuccessRequests: 90, TTFTSumMs: 9000, TTFTCount: 90, TTFTHistogram: map[int64]int64{100: 90}, InputTokens: 1000, CacheReadTokens: 900},
		{BucketStart: start.Add(5 * time.Minute), SuccessRequests: 10, ChannelErrors: 20, TTFTSumMs: 100000, TTFTCount: 10, TTFTHistogram: map[int64]int64{10000: 10}, InputTokens: 100, CacheReadTokens: 0, ErrorCategories: map[string]int64{"upstream_5xx": 20}},
	}
	f := ChannelMonitorV2Filter{Start: start, End: start.Add(30 * time.Minute), Bucket: 5 * time.Minute}
	fine, _, fineBuckets := observationSeries(facts, cfg, "openai", 7, "m", f, true)
	f.Bucket = 15 * time.Minute
	coarse, _, coarseBuckets := observationSeries(facts, cfg, "openai", 7, "m", f, true)
	require.Len(t, fineBuckets, 6)
	require.Len(t, coarseBuckets, 2)
	require.Equal(t, fine, coarse)
	require.EqualValues(t, 120, coarseBuckets[0].Metrics.RequestCount)
	require.EqualValues(t, 20, coarseBuckets[0].Metrics.ChannelErrors)
	require.EqualValues(t, 100, *coarseBuckets[0].Metrics.TTFT.P50Ms)
	require.EqualValues(t, 10000, *coarseBuckets[0].Metrics.TTFT.P95Ms)
	require.InDelta(t, 100.0/120, *coarseBuckets[0].Metrics.ReliabilityRate, .00001)
	require.Equal(t, "critical", coarseBuckets[0].Health.Reliability)
	require.EqualValues(t, 20, coarseBuckets[0].Metrics.ErrorCategories["upstream_5xx"])
	require.Equal(t, "no_samples", coarseBuckets[1].Metrics.SampleState)
}

type observationTimelineCountingRepo struct {
	ChannelMonitorObservationRepository
	calls atomic.Int32
}

func (r *observationTimelineCountingRepo) Query(_ context.Context, f ChannelMonitorV2Filter) (*ChannelMonitorObservationSnapshot, error) {
	r.calls.Add(1)
	return &ChannelMonitorObservationSnapshot{Coverage: ChannelMonitorObservationCoverage{DataThrough: f.End}}, nil
}

func TestObservationTimeline_QueryCacheIsolatedByOutputAndStorageBucket(t *testing.T) {
	repo := &observationTimelineCountingRepo{}
	svc := NewChannelMonitorOverviewService(repo, nil, nil, nil)
	f := ChannelMonitorV2Filter{Start: time.Now().UTC().Add(-time.Hour), End: time.Now().UTC(), Bucket: 5 * time.Minute}
	_, err := svc.query(context.Background(), f, 1, "user", 42, false)
	require.NoError(t, err)
	_, err = svc.query(context.Background(), f, 1, "user", 42, false)
	require.NoError(t, err)
	require.EqualValues(t, 1, repo.calls.Load())
	f.Bucket = 15 * time.Minute
	_, err = svc.query(context.Background(), f, 1, "user", 42, false)
	require.NoError(t, err)
	require.EqualValues(t, 2, repo.calls.Load())
	f.RollupBucket = 5 * time.Minute
	_, err = svc.query(context.Background(), f, 1, "user", 42, false)
	require.NoError(t, err)
	require.EqualValues(t, 3, repo.calls.Load())
	// Same five-minute display as the first request, but custom minute source.
	f.Bucket, f.RollupBucket = 5*time.Minute, time.Minute
	_, err = svc.query(context.Background(), f, 1, "user", 42, false)
	require.NoError(t, err)
	require.EqualValues(t, 4, repo.calls.Load())
}

type observationTimelineOverviewRepo struct {
	unifiedObservationRepo
	filters []ChannelMonitorV2Filter
}

func (r *observationTimelineOverviewRepo) Query(ctx context.Context, f ChannelMonitorV2Filter) (*ChannelMonitorObservationSnapshot, error) {
	r.filters = append(r.filters, f)
	snapshot, err := r.unifiedObservationRepo.Query(ctx, f)
	if snapshot != nil {
		snapshot.QueryStart = f.Start
		snapshot.QueryEnd = f.End
	}
	return snapshot, err
}

func TestObservationTimeline_OverviewAndModelsUseBucketsWhileCurrentHealthStaysIndependent(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	policy := DefaultChannelMonitorObservationConfig()
	policy.Mode = "live"
	policy.MinimumSample = 1
	repo := &observationTimelineOverviewRepo{unifiedObservationRepo: unifiedObservationRepo{policy: policy, facts: []ChannelMonitorObservationFact{
		{Source: "traffic", GroupID: 7, Model: "m", BucketStart: now.Add(-time.Minute), SuccessRequests: 100},
		{Source: "traffic", GroupID: 7, Model: "m", BucketStart: now.Add(-8 * time.Minute), ChannelErrors: 20},
	}}}
	legacy := &channelMonitorV2RepoStub{config: ChannelMonitorV2Config{Enabled: true, Platforms: []ChannelMonitorV2PlatformConfig{{Platform: "openai", Enabled: true}}}, matrix: &ChannelMonitorV2Matrix{}}
	svc := NewChannelMonitorOverviewService(repo, legacy, unifiedGroups{items: []Group{{ID: 7, Platform: "openai"}}}, nil)
	svc.SetModelCatalogue(func(context.Context, *Group) ([]string, error) { return []string{"m", "idle"}, nil })
	f := ChannelMonitorV2Filter{Range: "90m", Start: now.Add(-90 * time.Minute), End: now, Bucket: 5 * time.Minute, RollupBucket: time.Minute, RestrictGroups: true, AllowedGroupIDs: []int64{7}}
	fine, err := svc.Overview(context.Background(), f, true, 1, false)
	require.NoError(t, err)
	f.Bucket = 15 * time.Minute
	coarse, err := svc.Overview(context.Background(), f, true, 1, false)
	require.NoError(t, err)
	require.EqualValues(t, 900, coarse.Coverage.BucketSeconds)
	require.Equal(t, fine.Items[0].Metrics, coarse.Items[0].Metrics)
	require.Equal(t, fine.Items[0].CurrentStatus, coarse.Items[0].CurrentStatus)
	require.Equal(t, "healthy", coarse.Items[0].CurrentStatus.State)
	require.EqualValues(t, 120, coarse.Items[0].Metrics.RequestCount)
	require.Less(t, len(coarse.Items[0].Buckets), len(fine.Items[0].Buckets))
	for _, model := range coarse.Items[0].Models {
		require.Len(t, model.Buckets, len(coarse.Items[0].Buckets))
		if model.Model == "idle" {
			require.Equal(t, "no_samples", model.Metrics.SampleState)
		}
	}
	require.NotNil(t, coarse.Items[0].Buckets[0].SampleStart)
	require.Equal(t, f.Start, *coarse.Items[0].Buckets[0].SampleStart)
	for _, queried := range repo.filters {
		if queried.Bucket == time.Minute {
			require.Zero(t, queried.RollupBucket)
		}
	}
}
