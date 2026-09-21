package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type pendingObservationRepo struct{ unifiedObservationRepo }

func (r *pendingObservationRepo) Query(ctx context.Context, f ChannelMonitorV2Filter) (*ChannelMonitorObservationSnapshot, error) {
	out, err := r.unifiedObservationRepo.Query(ctx, f)
	if err == nil {
		out.Coverage.PendingEvents = 17
	}
	return out, err
}

func TestObservationCoverage_PendingEventsOwnerOnly(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	policy := DefaultChannelMonitorObservationConfig()
	policy.Mode = "live"
	repo := &pendingObservationRepo{unifiedObservationRepo{policy: policy}}
	legacy := &channelMonitorV2RepoStub{config: ChannelMonitorV2Config{Enabled: true, Platforms: []ChannelMonitorV2PlatformConfig{{Platform: PlatformOpenAI, Enabled: true}}}}
	svc := NewChannelMonitorOverviewService(repo, legacy, unifiedGroups{items: []Group{{ID: 7, Platform: PlatformOpenAI}}}, nil)
	f := ChannelMonitorV2Filter{Start: now.Add(-time.Hour), End: now, Bucket: 5 * time.Minute, Range: "24h", RestrictGroups: true, AllowedGroupIDs: []int64{7}}
	for _, admin := range []bool{false, true} {
		out, err := svc.Overview(context.Background(), f, admin, 1, false)
		require.NoError(t, err)
		encoded, err := json.Marshal(out.Coverage)
		require.NoError(t, err)
		if admin {
			require.Contains(t, string(encoded), `"pending_events":17`)
		} else {
			require.NotContains(t, string(encoded), `"pending_events"`)
		}
	}
}
