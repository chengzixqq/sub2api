package service

import (
	"strings"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestNormalizePushSubscription(t *testing.T) {
	got, err := NormalizePushSubscription(PushSubscription{})
	require.NoError(t, err)
	require.Equal(t, []string{PushEventOpsAlertFired, PushEventAccountError}, got.Events, "omitted events use the default")
	require.Equal(t, "P1", got.MinSeverity)
	require.Empty(t, got.GroupIDs)
	require.Empty(t, got.Platforms)
	require.Nil(t, got.QuietHours)

	got, err = NormalizePushSubscription(PushSubscription{
		Events:      []string{"account.error", " ops_alert.resolved ", "account.error", "ops_alert.fired"},
		MinSeverity: "p2",
		GroupIDs:    []int64{3, 1, 3},
		Platforms:   []string{"OpenAI", "anthropic", "openai"},
		QuietHours:  &PushQuietHours{Start: "23:00", End: "07:00"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{PushEventOpsAlertFired, PushEventOpsAlertResolved, PushEventAccountError}, got.Events, "deduplicated, canonical order")
	require.Equal(t, "P2", got.MinSeverity)
	require.Equal(t, []int64{1, 3}, got.GroupIDs)
	require.Equal(t, []string{"anthropic", "openai"}, got.Platforms)
	require.Equal(t, &PushQuietHours{Start: "23:00", End: "07:00"}, got.QuietHours)

	got, err = NormalizePushSubscription(PushSubscription{Events: []string{}, QuietHours: &PushQuietHours{Start: "08:00", End: "08:00"}})
	require.NoError(t, err)
	require.Empty(t, got.Events, "an explicit empty list mutes the device")
	require.NotNil(t, got.Events)
	require.Nil(t, got.QuietHours, "start == end means no quiet hours")

	for name, in := range map[string]PushSubscription{
		"unknown event":     {Events: []string{"account.deleted"}},
		"push.test":         {Events: []string{PushEventTest}},
		"severity P5":       {MinSeverity: "P5"},
		"hour 25":           {QuietHours: &PushQuietHours{Start: "25:00", End: "07:00"}},
		"missing end":       {QuietHours: &PushQuietHours{Start: "23:00"}},
		"bad minute":        {QuietHours: &PushQuietHours{Start: "23:60", End: "07:00"}},
		"group id zero":     {GroupIDs: []int64{0}},
		"platform charset":  {Platforms: []string{"open ai"}},
		"too many groups":   {GroupIDs: sequentialIDs(pushMaxGroupFilter + 1)},
		"too many platform": {Platforms: repeatedPlatforms(pushMaxPlatformFilter + 1)},
	} {
		_, err := NormalizePushSubscription(in)
		require.Error(t, err, name)
		require.Equal(t, "INVALID_PUSH_SUBSCRIPTION", infraerrors.Reason(err), name)
	}
}

func sequentialIDs(n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = int64(i + 1)
	}
	return out
}

func repeatedPlatforms(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "p" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	return out
}

func TestPushQuietHours_CrossMidnightAndTimezone(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	q := &PushQuietHours{Start: "23:00", End: "07:00"}

	require.True(t, q.Contains(time.Date(2026, 10, 21, 15, 30, 0, 0, time.UTC), shanghai), "23:30 local")
	require.True(t, q.Contains(time.Date(2026, 10, 21, 22, 59, 0, 0, time.UTC), shanghai), "06:59 local")
	require.False(t, q.Contains(time.Date(2026, 10, 21, 23, 0, 0, 0, time.UTC), shanghai), "07:00 local ends the window")
	require.False(t, q.Contains(time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC), shanghai), "08:00 local")
	require.True(t, q.Contains(time.Date(2026, 10, 21, 23, 30, 0, 0, time.UTC), time.UTC), "same window read in UTC")

	day := &PushQuietHours{Start: "12:00", End: "14:00"}
	require.True(t, day.Contains(time.Date(2026, 10, 21, 13, 0, 0, 0, time.UTC), time.UTC))
	require.False(t, day.Contains(time.Date(2026, 10, 21, 14, 0, 0, 0, time.UTC), time.UTC))

	var none *PushQuietHours
	require.False(t, none.Contains(time.Now(), time.UTC))
	require.False(t, (&PushQuietHours{Start: "08:00", End: "08:00"}).Contains(time.Now(), time.UTC))
}

func TestPushSubscriptionMatches(t *testing.T) {
	sub, err := NormalizePushSubscription(PushSubscription{
		Events:    []string{PushEventOpsAlertFired, PushEventAccountError, PushEventAccountTestFinished},
		GroupIDs:  []int64{7},
		Platforms: []string{"anthropic"},
	})
	require.NoError(t, err)

	for _, sev := range []string{"P0", "P1", "p1"} {
		require.True(t, sub.Matches(&PushEvent{Type: PushEventOpsAlertFired, Severity: sev}), sev)
	}
	require.False(t, sub.Matches(&PushEvent{Type: PushEventOpsAlertFired, Severity: "P2"}), "below min_severity P1")
	require.False(t, sub.Matches(&PushEvent{Type: PushEventOpsAlertFired, Severity: "unknown"}), "unknown severities rank lowest")
	require.False(t, sub.Matches(&PushEvent{Type: PushEventOpsAlertResolved, Severity: "P0"}), "not subscribed")

	require.True(t, sub.Matches(&PushEvent{Type: PushEventAccountError, Platform: "anthropic", GroupIDs: []int64{3, 7}}))
	require.True(t, sub.Matches(&PushEvent{Type: PushEventAccountError, Platform: "anthropic"}), "no group info is not filtered out")
	require.False(t, sub.Matches(&PushEvent{Type: PushEventAccountError, Platform: "anthropic", GroupIDs: []int64{3}}))
	require.False(t, sub.Matches(&PushEvent{Type: PushEventAccountError, Platform: "openai", GroupIDs: []int64{7}}))
	require.True(t, sub.Matches(&PushEvent{Type: PushEventAccountError, Severity: "P3", Platform: "anthropic"}), "min_severity only applies to ops alerts")
	require.True(t, sub.Matches(&PushEvent{Type: PushEventOpsAlertFired, Severity: "P0", GroupIDs: []int64{7}}), "site-wide platform is empty")

	require.True(t, sub.Matches(&PushEvent{Type: PushEventAccountTestFinished, Platform: "openai", GroupIDs: []int64{3}}),
		"the initiator's own test result ignores the group and platform filters")
}

func TestPushSeverityRank(t *testing.T) {
	for sev, want := range map[string]int{"P0": 0, "p1": 1, " P2 ": 2, "P3": 3, "": 3, "critical": 3} {
		require.Equal(t, want, pushSeverityRank(sev), sev)
	}
}

// The same APNs token can be registered on several sites; the collapse ID must not let one site's notification
// replace another site's notification for an event with the same dedup key.
func TestPushEventMessageFor_CollapseIDIsPerSite(t *testing.T) {
	ev := &PushEvent{Type: PushEventAccountError, DedupKey: "account.error:12:987654", OccurredAt: time.Now()}
	siteA := ev.MessageFor("zh-Hans", "a.example.com")
	siteB := ev.MessageFor("zh-Hans", "b.example.com")
	require.NotEmpty(t, siteA.CollapseID)
	require.NotEqual(t, siteA.CollapseID, siteB.CollapseID, "two sites never share a collapse ID")
	require.Equal(t, siteA.CollapseID, ev.MessageFor("en-US", "a.example.com").CollapseID, "stable for one site and event")
	require.NotEqual(t, siteA.CollapseID, (&PushEvent{DedupKey: "account.error:13:987654"}).MessageFor("", "a.example.com").CollapseID)
	require.NotEmpty(t, ev.MessageFor("", "").CollapseID)

	long := &PushEvent{DedupKey: PushEventAccountTestFinished + ":" + strings.Repeat("f", 120)}
	require.LessOrEqual(t, len(long.MessageFor("", "a-very-long-site-label.example.com").CollapseID), 64, "apns-collapse-id is at most 64 bytes")
}
