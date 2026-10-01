package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestObservationOverviewMixedLagMatchesFinalDataThrough(t *testing.T) {
	for _, legacyAge := range []time.Duration{time.Hour, -time.Minute} {
		t.Run(legacyAge.String(), func(t *testing.T) {
			now := time.Now().UTC()
			policy := DefaultChannelMonitorObservationConfig()
			policy.Mode, policy.LiveGroupIDs = "live", []int64{7}
			legacyThrough := now.Add(-legacyAge)
			legacy := &channelMonitorV2RepoStub{
				config: ChannelMonitorV2Config{Enabled: true, Platforms: []ChannelMonitorV2PlatformConfig{{Platform: PlatformOpenAI, Enabled: true}}},
				matrix: &ChannelMonitorV2Matrix{Coverage: ChannelMonitorV2Coverage{DataThrough: legacyThrough}},
			}
			svc := NewChannelMonitorOverviewService(&unifiedObservationRepo{policy: policy}, legacy, unifiedGroups{items: []Group{
				{ID: 7, Platform: PlatformOpenAI}, {ID: 8, Platform: PlatformOpenAI},
			}}, nil)
			out, err := svc.Overview(context.Background(), ChannelMonitorV2Filter{
				Start: now.Add(-24 * time.Hour), End: now, Range: "24h", Bucket: 5 * time.Minute,
			}, true, 1, false)
			require.NoError(t, err)
			require.Equal(t, "mixed", out.Source)
			require.Len(t, out.Items, 2)
			if legacyAge > 0 {
				require.Equal(t, legacyThrough, out.Coverage.DataThrough)
			} else {
				require.True(t, out.Coverage.DataThrough.Before(legacyThrough), "the compact watermark remains the older boundary")
			}
			wantLag := int64(time.Since(out.Coverage.DataThrough).Seconds())
			if wantLag < 0 {
				wantLag = 0
			}
			require.InDelta(t, wantLag, out.Coverage.AggregationLagSeconds, 1, "the displayed lag must use the same final watermark as data_through")
		})
	}
}
