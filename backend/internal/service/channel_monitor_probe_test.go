//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestChannelMonitorProbeDue_RequiresHealthyContinuousIdleWindow(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	activity := ChannelMonitorProbeActivity{CollectionHealthy: true, ObservedSince: now.Add(-time.Hour)}
	require.True(t, channelMonitorProbeDue(now, activity, nil))
	activity.CollectionHealthy = false
	require.False(t, channelMonitorProbeDue(now, activity, nil))
	activity.CollectionHealthy = true
	activity.ObservedSince = now.Add(-14 * time.Minute)
	require.False(t, channelMonitorProbeDue(now, activity, nil))
	activity.ObservedSince = now.Add(-time.Hour)
	recent := now.Add(-14 * time.Minute)
	activity.LastTrafficAt = &recent
	require.False(t, channelMonitorProbeDue(now, activity, nil))
	activity.LastTrafficAt = nil
	require.False(t, channelMonitorProbeDue(now, activity, &recent))
}

func TestChannelMonitorProbeTarget_Validation(t *testing.T) {
	valid := ChannelMonitorProbeTarget{GroupID: 1, Model: "gpt-test", Protocol: "openai_chat"}
	require.NoError(t, valid.Validate())
	valid.Model = "gpt-image-1"
	require.Error(t, valid.Validate())
	valid.Model = "../token?secret=1"
	require.Error(t, valid.Validate())
	valid.Model = "gpt-test"
	valid.Protocol = "unknown"
	require.Error(t, valid.Validate())
}

func TestChannelMonitorProbeIdempotencyKey_RejectsUnboundedAndControl(t *testing.T) {
	require.True(t, validMonitorProbeKey("a-valid-key-123456789"))
	require.False(t, validMonitorProbeKey(""))
	require.False(t, validMonitorProbeKey("bad-key-12345\n67890"))
}
