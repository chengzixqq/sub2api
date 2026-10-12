package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type coverageReasonObservationRepo struct {
	unifiedObservationRepo
	coverage ChannelMonitorObservationCoverage
}

func (r *coverageReasonObservationRepo) Query(ctx context.Context, f ChannelMonitorV2Filter) (*ChannelMonitorObservationSnapshot, error) {
	out, err := r.unifiedObservationRepo.Query(ctx, f)
	if err != nil {
		return nil, err
	}
	out.Coverage = r.coverage
	return out, nil
}

func TestObservationOverview_UnknownFailuresDoNotCreateCoverageGaps(t *testing.T) {
	for _, tc := range []struct {
		name        string
		initial     string
		reasons     []string
		model       string
		unknown     int64
		wantState   string
		wantReasons []string
	}{
		{name: "healthy collector with unknown failures", initial: "complete", model: "visible", unknown: 1, wantState: "complete", wantReasons: []string{}},
		{name: "existing gap remains explained", initial: "partial", reasons: []string{"queue_loss"}, model: "visible", unknown: 1, wantState: "partial", wantReasons: []string{"queue_loss"}},
		{name: "stale state is not upgraded", initial: "stale", reasons: []string{"aggregation_lag"}, model: "visible", unknown: 1, wantState: "stale", wantReasons: []string{"aggregation_lag"}},
		{name: "healthy idle window is complete", initial: "complete", model: "visible", wantState: "complete", wantReasons: []string{}},
		{name: "invisible model does not expose a gap", initial: "complete", model: "private", unknown: 1, wantState: "complete", wantReasons: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Minute)
			policy := DefaultChannelMonitorObservationConfig()
			policy.Mode = "live"
			repo := &coverageReasonObservationRepo{
				unifiedObservationRepo: unifiedObservationRepo{policy: policy},
				coverage:               ChannelMonitorObservationCoverage{State: tc.initial, CollectorState: "healthy", DataThrough: now, GapReasons: tc.reasons},
			}
			if tc.unknown > 0 {
				for i := 1; i <= 2; i++ {
					repo.facts = append(repo.facts, ChannelMonitorObservationFact{Source: "traffic", GroupID: 7, Model: tc.model, BucketStart: now.Add(-time.Duration(i) * time.Minute), UnknownRequests: tc.unknown})
				}
			}
			legacy := &channelMonitorV2RepoStub{config: ChannelMonitorV2Config{Enabled: true}}
			svc := NewChannelMonitorOverviewService(repo, legacy, unifiedGroups{items: []Group{{ID: 7, Platform: PlatformOpenAI}}}, nil)
			svc.SetModelCatalogue(func(context.Context, *Group) ([]string, error) { return []string{"visible"}, nil })
			out, err := svc.Overview(context.Background(), ChannelMonitorV2Filter{Start: now.Add(-90 * time.Minute), End: now, Bucket: 5 * time.Minute}, true, 1, false)
			require.NoError(t, err)
			require.Equal(t, tc.wantState, out.Coverage.State)
			require.Equal(t, tc.wantState == "complete", out.Coverage.CoverageComplete)
			require.ElementsMatch(t, tc.wantReasons, out.Coverage.GapReasons)
			require.Len(t, out.Coverage.GapReasons, len(tc.wantReasons), "repeat buckets must not duplicate the reason")
		})
	}
}
