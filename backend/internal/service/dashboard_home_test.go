package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

func homeQuery(t *testing.T, zone string) DashboardHomeQuery {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	require.NoError(t, err)
	return DashboardHomeQuery{Location: loc, ViewerID: 42}
}

func TestDashboardHomeAllSectionsOK(t *testing.T) {
	f := newHomeFakes()
	home := f.service().Build(context.Background(), homeQuery(t, "Asia/Shanghai"))

	require.Equal(t, "Asia/Shanghai", home.Timezone)
	require.Equal(t, homeTestNow.Truncate(time.Second), home.GeneratedAt)
	for name, status := range homeStatuses(home) {
		require.Equal(t, DashboardHomeStatusOK, status, name)
	}
	require.False(t, home.Degraded())

	// stats: server-timezone today plus yesterday at the same moment.
	stats := home.Stats.Data
	require.Equal(t, timezone.Location().String(), stats.Timezone)
	require.Equal(t, int64(18204), stats.TodayRequests)
	require.Equal(t, 1284.5, stats.TodayActualCost)
	require.Equal(t, 512.08, stats.TodayAccountCost)
	require.Equal(t, "2026-10-18T09:40:00Z", stats.StatsUpdatedAt)
	require.Equal(t, int64(17000), *stats.YesterdaySameTimeRequests)
	require.Equal(t, 1190.27, *stats.YesterdaySameTimeActualCost)
	require.Equal(t, 480.0, *stats.YesterdaySameTimeAccountCost)
	require.True(t, stats.YesterdaySameTimeUntil.Equal(homeTestNow.Add(-24*time.Hour)))

	// hourly: yesterday 00:00 to tomorrow 00:00 in the requested zone, split by date.
	shanghai, _ := time.LoadLocation("Asia/Shanghai")
	require.Equal(t, "hour", f.stats.trendGran)
	require.True(t, f.stats.trendStart.Equal(time.Date(2026, 10, 17, 0, 0, 0, 0, shanghai)))
	require.True(t, f.stats.trendEnd.Equal(time.Date(2026, 10, 19, 0, 0, 0, 0, shanghai)))
	require.Equal(t, "Asia/Shanghai", f.stats.trendZone)
	require.Equal(t, "Asia/Shanghai", home.Hourly.Data.Timezone)
	require.Len(t, home.Hourly.Data.Today, 2)
	require.Len(t, home.Hourly.Data.Yesterday, 1)
	require.Equal(t, "2026-10-17 23:00", home.Hourly.Data.Yesterday[0].Date)

	// rates and ops share the 5-second end bucket.
	end := time.Date(2026, 10, 18, 9, 41, 5, 0, time.UTC)
	require.Equal(t, 5*time.Minute, f.ops.ratesQuery.Window)
	require.True(t, f.ops.ratesQuery.End.Equal(end))
	require.Equal(t, 46.4, home.Rates.Data.RPM)
	require.True(t, f.ops.overviewFilter.EndTime.Equal(end))
	require.True(t, f.ops.overviewFilter.StartTime.Equal(end.Add(-5*time.Minute)))
	require.Equal(t, OpsQueryModeRaw, f.ops.overviewFilter.QueryMode)
	require.Equal(t, 94, home.Ops.Data.HealthScore)
	require.Equal(t, int64(12), home.Ops.Data.Upstream429Count)

	// alerts: exact firing count from D06, five newest events.
	require.Equal(t, int64(7), home.Alerts.Data.FiringCount)
	require.Equal(t, int64(4), home.Alerts.Data.BySeverity["P2"])
	require.Len(t, home.Alerts.Data.Top, 5)
	require.Equal(t, OpsAlertStatusFiring, f.ops.eventsFilter.Status)
	require.Equal(t, 5, f.ops.eventsFilter.Limit)

	// availability: only the abnormal accounts are listed.
	require.True(t, f.avail.query.IncludeAccounts)
	ids := []int64{}
	for _, a := range home.Availability.Data.Accounts {
		ids = append(ids, a.ID)
	}
	require.Equal(t, []int64{2, 3, 4, 5}, ids)
	require.False(t, home.Availability.Data.AccountsTruncated)
	require.Len(t, home.Availability.Data.Groups, 1)

	// channels: four buckets, stale counts as unknown.
	ch := home.Channels.Data
	require.Equal(t, DashboardHomeChannels{Total: 5, Healthy: 1, Warning: 1, Critical: 1, Unknown: 2, CoverageState: "partial", CollectorState: "healthy"}, *ch)
}

func homeStatuses(home *DashboardHome) map[string]string {
	return map[string]string{
		"stats": home.Stats.Status, "rates": home.Rates.Status, "hourly": home.Hourly.Status, "ops": home.Ops.Status,
		"alerts": home.Alerts.Status, "availability": home.Availability.Status, "channels": home.Channels.Status,
	}
}

func TestDashboardHomeServerZoneLeavesTrendZoneUnset(t *testing.T) {
	f := newHomeFakes()
	f.service().Build(context.Background(), DashboardHomeQuery{})
	require.Equal(t, "", f.stats.trendZone, "the server zone keeps the hourly pre-aggregation path")
}

func TestDashboardHomeOpsDisabledStillReturnsUsage(t *testing.T) {
	f := newHomeFakes()
	f.ops.monitoring = false
	f.ops.rates = &OpsLiveRates{WindowSeconds: 300, RPM: 40}
	home := f.service().Build(context.Background(), homeQuery(t, "UTC"))
	require.NotNil(t, home)

	for _, sec := range []struct{ status, reason string }{{home.Ops.Status, home.Ops.Error}, {home.Alerts.Status, home.Alerts.Error}} {
		require.Equal(t, DashboardHomeStatusDisabled, sec.status)
		require.Equal(t, "OPS_DISABLED", sec.reason)
	}
	require.Nil(t, home.Ops.Data)
	_, overviewCalls, countsCalls, eventsCalls := f.ops.callCounts()
	require.Zero(t, overviewCalls)
	require.Zero(t, countsCalls)
	require.Zero(t, eventsCalls)

	require.Equal(t, DashboardHomeStatusOK, home.Stats.Status)
	require.Equal(t, DashboardHomeStatusOK, home.Hourly.Status)
	require.Equal(t, DashboardHomeStatusOK, home.Availability.Status)
	require.Equal(t, DashboardHomeStatusOK, home.Rates.Status)
	require.Nil(t, home.Rates.Data.ErrorRate)
	require.False(t, home.Rates.Data.ErrorsAvailable)
	require.False(t, home.Degraded(), "disabled sections are not a degradation")
}

func TestDashboardHomeSectionTimeoutIsolated(t *testing.T) {
	for name, hook := range map[string]func(context.Context){
		"honours ctx": func(ctx context.Context) { <-ctx.Done() },
		"ignores ctx": func(context.Context) { time.Sleep(2 * time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newHomeFakes()
			f.ops.overviewHook = hook
			s := f.service()
			s.timeouts[DashboardHomeSectionOps] = 50 * time.Millisecond

			started := time.Now()
			home := s.Build(context.Background(), homeQuery(t, "UTC"))
			require.Less(t, time.Since(started), time.Second)

			require.Equal(t, DashboardHomeStatusTimeout, home.Ops.Status)
			require.Equal(t, DashboardHomeErrorSectionTimeout, home.Ops.Error)
			for section, status := range homeStatuses(home) {
				if section != "ops" {
					require.Equal(t, DashboardHomeStatusOK, status, section)
				}
			}
			require.True(t, home.Degraded())
		})
	}
}

func TestDashboardHomeSectionErrorReasons(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(f *homeFakes)
		status string
		reason string
	}{
		{"stable reason", func(f *homeFakes) { f.avail.err = infraerrors.ServiceUnavailable("FOO_UNAVAILABLE", "foo") }, DashboardHomeStatusError, "FOO_UNAVAILABLE"},
		{"plain error", func(f *homeFakes) { f.avail.err = errors.New("pq: secret detail") }, DashboardHomeStatusError, DashboardHomeErrorInternal},
		{"panic", func(f *homeFakes) { f.avail.panics = true }, DashboardHomeStatusError, DashboardHomeErrorInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newHomeFakes()
			tc.setup(f)
			home := f.service().Build(context.Background(), homeQuery(t, "UTC"))
			require.Equal(t, tc.status, home.Availability.Status)
			require.Equal(t, tc.reason, home.Availability.Error)
			require.Nil(t, home.Availability.Data)
			require.Equal(t, DashboardHomeStatusOK, home.Stats.Status)
			require.True(t, home.Degraded())
			raw, err := json.Marshal(home)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "secret detail")
		})
	}

	t.Run("monitoring switched off mid-request", func(t *testing.T) {
		f := newHomeFakes()
		f.ops.overviewErr = ErrOpsDisabled
		home := f.service().Build(context.Background(), homeQuery(t, "UTC"))
		require.Equal(t, DashboardHomeStatusDisabled, home.Ops.Status)
		require.Equal(t, "OPS_DISABLED", home.Ops.Error)
		require.False(t, home.Degraded())
	})
}

func TestDashboardHomeChannelsDisabled(t *testing.T) {
	f := newHomeFakes()
	f.runtime.enabled = false
	home := f.service().Build(context.Background(), homeQuery(t, "UTC"))
	require.Equal(t, DashboardHomeStatusDisabled, home.Channels.Status)
	require.Equal(t, "CHANNEL_MONITOR_DISABLED", home.Channels.Error)
	require.Zero(t, f.channels.calls)
}

func TestDashboardHomeChannelsOverviewFilter(t *testing.T) {
	f := newHomeFakes()
	q := homeQuery(t, "UTC")
	q.Refresh = true
	f.service().Build(context.Background(), q)
	require.Equal(t, 1, f.channels.calls)
	require.True(t, f.channels.admin)
	require.Equal(t, int64(42), f.channels.viewer)
	require.True(t, f.channels.refresh)
	require.Equal(t, "90m", f.channels.filter.Range)
	require.True(t, f.channels.filter.End.Equal(homeTestNow.Truncate(time.Minute)))
	require.Equal(t, 90*time.Minute, f.channels.filter.End.Sub(f.channels.filter.Start))
	require.Equal(t, 5*time.Minute, f.channels.filter.Bucket)
}

func TestDashboardHomeIncludeSubset(t *testing.T) {
	f := newHomeFakes()
	include, err := ParseDashboardHomeInclude("stats,ops")
	require.NoError(t, err)
	q := homeQuery(t, "UTC")
	q.Include = include
	home := f.service().Build(context.Background(), q)

	require.NotNil(t, home.Stats)
	require.NotNil(t, home.Ops)
	require.Nil(t, home.Rates)
	require.Nil(t, home.Hourly)
	require.Nil(t, home.Alerts)
	require.Nil(t, home.Availability)
	require.Nil(t, home.Channels)
	ratesCalls, overviewCalls, countsCalls, eventsCalls := f.ops.callCounts()
	_, trendCalls := f.stats.calls()
	require.Equal(t, []int{0, 1, 0, 0, 0, 0, 0}, []int{ratesCalls, overviewCalls, countsCalls, eventsCalls, trendCalls, f.avail.calls, f.channels.calls})

	raw, err := json.Marshal(home)
	require.NoError(t, err)
	var keys map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &keys))
	for _, absent := range []string{"rates", "hourly", "alerts", "availability", "channels"} {
		require.NotContains(t, keys, absent)
	}
}

func TestDashboardHomeAvailabilityCapsAbnormalAccounts(t *testing.T) {
	f := newHomeFakes()
	accounts := make([]AccountStatusItem, 0, 105)
	for i := 0; i < 105; i++ {
		accounts = append(accounts, AccountStatusItem{ID: int64(i + 1), State: AccountStateError})
	}
	f.avail.summary.Accounts = accounts
	home := f.service().Build(context.Background(), homeQuery(t, "UTC"))
	require.Len(t, home.Availability.Data.Accounts, dashboardHomeMaxAbnormalAccounts)
	require.True(t, home.Availability.Data.AccountsTruncated)
}

func TestDashboardHomeYesterdayFailureKeepsStats(t *testing.T) {
	f := newHomeFakes()
	f.stats.yesterday, f.stats.yesterdayErr = nil, errors.New("timeout")
	home := f.service().Build(context.Background(), homeQuery(t, "UTC"))
	require.Equal(t, DashboardHomeStatusOK, home.Stats.Status)
	require.Nil(t, home.Stats.Data.YesterdaySameTimeRequests)
	require.Nil(t, home.Stats.Data.YesterdaySameTimeUntil)
	require.Equal(t, int64(18204), home.Stats.Data.TodayRequests)
}

func TestDashboardHomeETagViewDropsBuildTimestamps(t *testing.T) {
	f := newHomeFakes()
	f.ops.rates.StartTime, f.ops.rates.EndTime = homeTestNow.Add(-time.Minute), homeTestNow
	home := f.service().Build(context.Background(), homeQuery(t, "UTC"))
	view := home.ETagView()

	require.True(t, view.GeneratedAt.IsZero())
	require.True(t, view.Rates.Data.EndTime.IsZero())
	require.True(t, view.Ops.Data.EndTime.IsZero())
	require.True(t, view.Availability.Data.CollectedAt.IsZero())
	require.Equal(t, home.Rates.Data.RPM, view.Rates.Data.RPM)
	// The response itself keeps every timestamp.
	require.False(t, home.GeneratedAt.IsZero())
	require.False(t, home.Rates.Data.EndTime.IsZero())
	require.False(t, home.Ops.Data.EndTime.IsZero())
	require.False(t, home.Availability.Data.CollectedAt.IsZero())
}

func TestParseDashboardHomeInclude(t *testing.T) {
	all, err := ParseDashboardHomeInclude("")
	require.NoError(t, err)
	require.Equal(t, []string{"alerts", "availability", "channels", "hourly", "ops", "rates", "stats"}, all.Names())

	some, err := ParseDashboardHomeInclude(" stats, ops ,stats")
	require.NoError(t, err)
	require.Equal(t, []string{"ops", "stats"}, some.Names())

	_, err = ParseDashboardHomeInclude("stats,foo")
	require.Equal(t, "INVALID_INCLUDE", infraerrors.Reason(err))
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))

	_, err = ParseDashboardHomeInclude(" , ")
	require.Equal(t, "INVALID_INCLUDE", infraerrors.Reason(err), "only separators names nothing")
}

func TestParseDashboardHomeTimezone(t *testing.T) {
	loc, err := ParseDashboardHomeTimezone("")
	require.NoError(t, err)
	require.Equal(t, timezone.Location(), loc)

	loc, err = ParseDashboardHomeTimezone(" Asia/Shanghai ")
	require.NoError(t, err)
	require.Equal(t, "Asia/Shanghai", loc.String())

	_, err = ParseDashboardHomeTimezone("Mars/Base")
	require.Equal(t, "INVALID_TIMEZONE", infraerrors.Reason(err))
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
}
