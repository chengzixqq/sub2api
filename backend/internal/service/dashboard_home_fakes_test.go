package service

import (
	"context"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

var homeTestNow = time.Date(2026, 10, 18, 9, 41, 7, 0, time.UTC)

type homeStatsFake struct {
	mu           sync.Mutex
	stats        *usagestats.DashboardStats
	statsErr     error
	statsCalls   int
	yesterday    *DashboardYesterdaySameTime
	yesterdayErr error
	trend        []usagestats.TrendDataPoint
	trendErr     error
	trendCalls   int
	trendStart   time.Time
	trendEnd     time.Time
	trendGran    string
	trendZone    string
}

func (f *homeStatsFake) GetDashboardStats(context.Context) (*usagestats.DashboardStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statsCalls++
	return f.stats, f.statsErr
}

func (f *homeStatsFake) GetYesterdaySameTime(context.Context, *usagestats.DashboardStats, time.Time) (*DashboardYesterdaySameTime, error) {
	return f.yesterday, f.yesterdayErr
}

func (f *homeStatsFake) GetUsageTrendWithUsageFilters(ctx context.Context, start, end time.Time, granularity string, _ usagestats.UsageLogFilters) ([]usagestats.TrendDataPoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trendCalls++
	f.trendStart, f.trendEnd, f.trendGran = start, end, granularity
	f.trendZone = usagequery.OptionsFrom(ctx).Timezone
	return f.trend, f.trendErr
}

func (f *homeStatsFake) calls() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statsCalls, f.trendCalls
}

type homeOpsFake struct {
	mu             sync.Mutex
	monitoring     bool
	rates          *OpsLiveRates
	ratesQuery     OpsLiveRatesQuery
	ratesCalls     int
	overview       *OpsDashboardOverview
	overviewErr    error
	overviewFilter *OpsDashboardFilter
	overviewCalls  int
	overviewHook   func(ctx context.Context)
	counts         *OpsAlertEventCounts
	countsCalls    int
	events         []*OpsAlertEvent
	eventsFilter   *OpsAlertEventFilter
	eventsCalls    int
}

func (f *homeOpsFake) IsMonitoringEnabled(context.Context) bool { return f.monitoring }

func (f *homeOpsFake) GetLiveRates(_ context.Context, q OpsLiveRatesQuery) (*OpsLiveRates, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ratesCalls++
	f.ratesQuery = q
	return f.rates, nil
}

func (f *homeOpsFake) GetDashboardOverview(ctx context.Context, filter *OpsDashboardFilter) (*OpsDashboardOverview, error) {
	f.mu.Lock()
	f.overviewCalls++
	copied := *filter
	f.overviewFilter = &copied
	hook := f.overviewHook
	f.mu.Unlock()
	if hook != nil {
		hook(ctx)
	}
	return f.overview, f.overviewErr
}

func (f *homeOpsFake) GetFiringAlertEventCounts(context.Context, string, *int64) (*OpsAlertEventCounts, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.countsCalls++
	return f.counts, nil
}

func (f *homeOpsFake) ListAlertEvents(_ context.Context, filter *OpsAlertEventFilter) ([]*OpsAlertEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.eventsCalls++
	copied := *filter
	f.eventsFilter = &copied
	return f.events, nil
}

func (f *homeOpsFake) callCounts() (rates, overview, counts, events int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ratesCalls, f.overviewCalls, f.countsCalls, f.eventsCalls
}

type homeAvailabilityFake struct {
	mu      sync.Mutex
	summary *AccountStatusSummary
	err     error
	panics  bool
	calls   int
	query   AccountStatusSummaryQuery
}

func (f *homeAvailabilityFake) Summarize(_ context.Context, q AccountStatusSummaryQuery) (*AccountStatusSummary, error) {
	f.mu.Lock()
	f.calls++
	f.query = q
	f.mu.Unlock()
	if f.panics {
		panic("boom")
	}
	return f.summary, f.err
}

type homeChannelFake struct {
	mu       sync.Mutex
	overview *ObservationOverview
	calls    int
	filter   ChannelMonitorV2Filter
	admin    bool
	viewer   int64
	refresh  bool
}

func (f *homeChannelFake) Overview(_ context.Context, filter ChannelMonitorV2Filter, admin bool, viewer int64, refresh bool) (*ObservationOverview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.filter, f.admin, f.viewer, f.refresh = filter, admin, viewer, refresh
	return f.overview, nil
}

type homeChannelRuntimeFake struct{ enabled bool }

func (f homeChannelRuntimeFake) GetChannelMonitorRuntime(context.Context) ChannelMonitorRuntime {
	return ChannelMonitorRuntime{Enabled: f.enabled}
}

type homeFakes struct {
	stats    *homeStatsFake
	ops      *homeOpsFake
	avail    *homeAvailabilityFake
	channels *homeChannelFake
	runtime  homeChannelRuntimeFake
}

func newHomeFakes() *homeFakes {
	f64 := func(v float64) *float64 { return &v }
	until := homeTestNow.Add(-24 * time.Hour)
	return &homeFakes{
		stats: &homeStatsFake{
			stats:     &usagestats.DashboardStats{TodayRequests: 18204, TodayTokens: 900, TodayCost: 1300, TodayActualCost: 1284.5, TodayAccountCost: 512.08, ActiveUsers: 12, StatsUpdatedAt: "2026-10-18T09:40:00Z"},
			yesterday: &DashboardYesterdaySameTime{Requests: 17000, ActualCost: 1190.27, AccountCost: 480, Until: until},
			trend: []usagestats.TrendDataPoint{
				{Date: "2026-10-16 23:00", Requests: 1},
				{Date: "2026-10-17 23:00", Requests: 2},
				{Date: "2026-10-18 00:00", Requests: 3},
				{Date: "2026-10-18 17:00", Requests: 4},
			},
		},
		ops: &homeOpsFake{
			monitoring: true,
			rates:      &OpsLiveRates{WindowSeconds: 300, RPM: 46.4, TPM: 240000, ErrorRate: f64(0.0086), ErrorsAvailable: true, OpsMonitoringEnabled: true},
			overview:   &OpsDashboardOverview{HealthScore: 94, SLA: 0.99, ErrorRate: 0.008, Upstream429Count: 12, Upstream529Count: 3, RequestCountTotal: 300},
			counts:     &OpsAlertEventCounts{Firing: 7, BySeverity: map[string]int64{"P0": 1, "P1": 2, "P2": 4, "P3": 0}},
			events:     homeAlertEvents(7),
		},
		avail: &homeAvailabilityFake{summary: &AccountStatusSummary{
			CollectedAt: homeTestNow,
			Totals:      AccountStateCounts{Total: 7, Available: 1},
			Groups:      []AccountStatusGroupSummary{{GroupID: 3, GroupName: "g", AccountStateCounts: AccountStateCounts{Total: 7, Available: 1}}},
			Accounts: []AccountStatusItem{
				{ID: 1, State: AccountStateAvailable}, {ID: 2, State: AccountStateError}, {ID: 3, State: AccountStateOverloaded},
				{ID: 4, State: AccountStateRateLimited}, {ID: 5, State: AccountStateTempUnschedulable}, {ID: 6, State: AccountStatePaused},
				{ID: 7, State: AccountStateInactive},
			},
		}},
		channels: &homeChannelFake{overview: &ObservationOverview{
			Coverage: ObservationCoverage{State: "partial", CollectorState: "healthy"},
			Items: []ObservationChannel{
				{GroupID: 1, CurrentStatus: ObservationCurrentStatus{State: "healthy"}},
				{GroupID: 2, CurrentStatus: ObservationCurrentStatus{State: "warning"}},
				{GroupID: 3, CurrentStatus: ObservationCurrentStatus{State: "critical"}},
				{GroupID: 4, CurrentStatus: ObservationCurrentStatus{State: "unknown"}},
				{GroupID: 5, CurrentStatus: ObservationCurrentStatus{State: "stale"}},
			},
		}},
		runtime: homeChannelRuntimeFake{enabled: true},
	}
}

func homeAlertEvents(n int) []*OpsAlertEvent {
	out := make([]*OpsAlertEvent, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, &OpsAlertEvent{ID: int64(i + 1), Severity: "P2", Status: OpsAlertStatusFiring})
	}
	return out
}

func (f *homeFakes) service() *DashboardHomeService {
	s := newDashboardHomeService(f.stats, f.ops, f.avail, f.channels, f.runtime)
	s.now = func() time.Time { return homeTestNow }
	return s
}
