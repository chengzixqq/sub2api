package service

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"go.uber.org/zap"
)

const (
	dashboardHomeLiveWindow          = 5 * time.Minute
	dashboardHomeLiveBucket          = 5 * time.Second
	dashboardHomeTopAlerts           = 5
	dashboardHomeMaxAbnormalAccounts = 100
	dashboardHomeChannelRange        = "90m"
	dashboardHomeChannelWindow       = 90 * time.Minute
	dashboardHomeChannelBucket       = 5 * time.Minute
)

// dashboardHomeLiveEnd is shared by rates and ops so both read the same window.
func dashboardHomeLiveEnd(now time.Time) time.Time {
	return now.UTC().Truncate(dashboardHomeLiveBucket)
}

func (s *DashboardHomeService) loadStats(ctx context.Context, now time.Time) (*DashboardHomeStats, error) {
	if s.stats == nil {
		return nil, ErrServiceUnavailable
	}
	stats, err := s.stats.GetDashboardStats(ctx)
	if err != nil {
		return nil, err
	}
	out := &DashboardHomeStats{
		Timezone:      timezone.Location().String(),
		TodayRequests: stats.TodayRequests, TodayTokens: stats.TodayTokens, TodayCost: stats.TodayCost,
		TodayActualCost: stats.TodayActualCost, TodayAccountCost: stats.TodayAccountCost, ActiveUsers: stats.ActiveUsers,
		StatsUpdatedAt: stats.StatsUpdatedAt, StatsStale: stats.StatsStale,
	}
	// The comparison is best-effort: without it the section still carries today.
	yesterday, err := s.stats.GetYesterdaySameTime(ctx, stats, now)
	if err != nil || yesterday == nil {
		if err != nil {
			logger.L().Warn("dashboard_home.yesterday_same_time_failed", zap.Error(err))
		}
		return out, nil
	}
	requests, actual, account, until := yesterday.Requests, yesterday.ActualCost, yesterday.AccountCost, yesterday.Until
	out.YesterdaySameTimeRequests, out.YesterdaySameTimeActualCost = &requests, &actual
	out.YesterdaySameTimeAccountCost, out.YesterdaySameTimeUntil = &account, &until
	return out, nil
}

func (s *DashboardHomeService) loadRates(ctx context.Context, now time.Time) (*OpsLiveRates, error) {
	if s.ops == nil {
		return nil, ErrServiceUnavailable
	}
	return s.ops.GetLiveRates(ctx, OpsLiveRatesQuery{Window: dashboardHomeLiveWindow, End: dashboardHomeLiveEnd(now)})
}

// loadHourly reads yesterday 00:00 to tomorrow 00:00 of loc in one hourly
// query and splits the points by their date label.
func (s *DashboardHomeService) loadHourly(ctx context.Context, loc *time.Location, now time.Time) (*DashboardHomeHourly, error) {
	if s.stats == nil {
		return nil, ErrServiceUnavailable
	}
	local := now.In(loc)
	todayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	yesterdayStart, tomorrowStart := todayStart.AddDate(0, 0, -1), todayStart.AddDate(0, 0, 1)
	points, err := s.stats.GetUsageTrendWithUsageFilters(withDashboardHomeTimezone(ctx, loc), yesterdayStart, tomorrowStart, "hour",
		usagestats.UsageLogFilters{ModelFilterSource: usagestats.ModelSourceRequested})
	if err != nil {
		return nil, err
	}
	out := &DashboardHomeHourly{Timezone: loc.String(), Today: []usagestats.TrendDataPoint{}, Yesterday: []usagestats.TrendDataPoint{}}
	todayLabel, yesterdayLabel := todayStart.Format(time.DateOnly), yesterdayStart.Format(time.DateOnly)
	for _, point := range points {
		switch {
		case strings.HasPrefix(point.Date, todayLabel):
			out.Today = append(out.Today, point)
		case strings.HasPrefix(point.Date, yesterdayLabel):
			out.Yesterday = append(out.Yesterday, point)
		}
	}
	return out, nil
}

// withDashboardHomeTimezone labels trend buckets in loc. The server zone is
// left unset: database sessions already use it, and it keeps the hourly
// pre-aggregation path available.
func withDashboardHomeTimezone(ctx context.Context, loc *time.Location) context.Context {
	options := usagequery.OptionsFrom(ctx)
	options.Timezone = ""
	if loc.String() != timezone.Location().String() {
		options.Timezone = loc.String()
	}
	return usagequery.WithOptions(ctx, options)
}

func (s *DashboardHomeService) loadOps(ctx context.Context, now time.Time) (*DashboardHomeOps, error) {
	if s.ops == nil {
		return nil, ErrServiceUnavailable
	}
	if !s.ops.IsMonitoringEnabled(ctx) {
		return nil, ErrOpsDisabled
	}
	end := dashboardHomeLiveEnd(now)
	start := end.Add(-dashboardHomeLiveWindow)
	o, err := s.ops.GetDashboardOverview(ctx, &OpsDashboardFilter{StartTime: start, EndTime: end, QueryMode: OpsQueryModeRaw})
	if err != nil {
		return nil, err
	}
	return &DashboardHomeOps{
		StartTime: start, EndTime: end, HealthScore: o.HealthScore, SLA: o.SLA,
		ErrorRate: o.ErrorRate, UpstreamErrorRate: o.UpstreamErrorRate,
		Upstream429Count: o.Upstream429Count, Upstream529Count: o.Upstream529Count,
		SuccessCount: o.SuccessCount, ErrorCountTotal: o.ErrorCountTotal, ErrorCountSLA: o.ErrorCountSLA,
		BusinessLimitedCount: o.BusinessLimitedCount, RequestCountTotal: o.RequestCountTotal,
		RequestCountSLA: o.RequestCountSLA, TokenConsumed: o.TokenConsumed,
	}, nil
}

func (s *DashboardHomeService) loadAlerts(ctx context.Context) (*DashboardHomeAlerts, error) {
	if s.ops == nil {
		return nil, ErrServiceUnavailable
	}
	if !s.ops.IsMonitoringEnabled(ctx) {
		return nil, ErrOpsDisabled
	}
	counts, err := s.ops.GetFiringAlertEventCounts(ctx, "", nil)
	if err != nil {
		return nil, err
	}
	events, err := s.ops.ListAlertEvents(ctx, &OpsAlertEventFilter{Status: OpsAlertStatusFiring, Limit: dashboardHomeTopAlerts})
	if err != nil {
		return nil, err
	}
	if len(events) > dashboardHomeTopAlerts {
		events = events[:dashboardHomeTopAlerts]
	}
	if events == nil {
		events = []*OpsAlertEvent{}
	}
	return &DashboardHomeAlerts{FiringCount: counts.Firing, BySeverity: counts.BySeverity, Top: events}, nil
}

func (s *DashboardHomeService) loadAvailability(ctx context.Context) (*DashboardHomeAvailability, error) {
	if s.availability == nil {
		return nil, ErrServiceUnavailable
	}
	summary, err := s.availability.Summarize(ctx, AccountStatusSummaryQuery{IncludeAccounts: true})
	if err != nil {
		return nil, err
	}
	out := &DashboardHomeAvailability{CollectedAt: summary.CollectedAt, Totals: summary.Totals, Groups: summary.Groups, Accounts: []AccountStatusItem{}}
	if out.Groups == nil {
		out.Groups = []AccountStatusGroupSummary{}
	}
	for _, account := range summary.Accounts {
		switch account.State {
		case AccountStateError, AccountStateOverloaded, AccountStateRateLimited, AccountStateTempUnschedulable:
		default:
			continue
		}
		if len(out.Accounts) == dashboardHomeMaxAbnormalAccounts {
			out.AccountsTruncated = true
			break
		}
		out.Accounts = append(out.Accounts, account)
	}
	return out, nil
}

func (s *DashboardHomeService) loadChannels(ctx context.Context, q DashboardHomeQuery, now time.Time) (*DashboardHomeChannels, error) {
	if s.channels == nil {
		return nil, ErrServiceUnavailable
	}
	if s.runtime != nil && !s.runtime.GetChannelMonitorRuntime(ctx).Enabled {
		return nil, ErrChannelMonitorDisabled
	}
	end := now.UTC().Truncate(time.Minute)
	overview, err := s.channels.Overview(ctx, ChannelMonitorV2Filter{
		Range: dashboardHomeChannelRange, Start: end.Add(-dashboardHomeChannelWindow), End: end, Bucket: dashboardHomeChannelBucket,
	}, true, q.ViewerID, q.Refresh)
	if err != nil {
		return nil, err
	}
	out := &DashboardHomeChannels{Total: len(overview.Items), CoverageState: overview.Coverage.State, CollectorState: overview.Coverage.CollectorState}
	for _, item := range overview.Items {
		switch item.CurrentStatus.State {
		case "healthy":
			out.Healthy++
		case "warning":
			out.Warning++
		case "critical":
			out.Critical++
		default:
			out.Unknown++
		}
	}
	return out, nil
}
