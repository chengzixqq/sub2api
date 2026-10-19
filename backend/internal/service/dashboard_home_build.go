package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"go.uber.org/zap"
)

const dashboardHomeBudget = 8 * time.Second

// Per-section time limits. Sections run concurrently, so the slowest one
// bounds the whole aggregate below dashboardHomeBudget.
var dashboardHomeDefaultTimeouts = map[string]time.Duration{
	DashboardHomeSectionStats:        5 * time.Second,
	DashboardHomeSectionRates:        3 * time.Second,
	DashboardHomeSectionHourly:       6 * time.Second,
	DashboardHomeSectionOps:          5 * time.Second,
	DashboardHomeSectionAlerts:       3 * time.Second,
	DashboardHomeSectionAvailability: 4 * time.Second,
	DashboardHomeSectionChannels:     6 * time.Second,
}

type dashboardHomeStatsSource interface {
	GetDashboardStats(ctx context.Context) (*usagestats.DashboardStats, error)
	GetYesterdaySameTime(ctx context.Context, stats *usagestats.DashboardStats, now time.Time) (*DashboardYesterdaySameTime, error)
	GetUsageTrendWithUsageFilters(ctx context.Context, startTime, endTime time.Time, granularity string, filters usagestats.UsageLogFilters) ([]usagestats.TrendDataPoint, error)
}

type dashboardHomeOpsSource interface {
	IsMonitoringEnabled(ctx context.Context) bool
	GetLiveRates(ctx context.Context, q OpsLiveRatesQuery) (*OpsLiveRates, error)
	GetDashboardOverview(ctx context.Context, filter *OpsDashboardFilter) (*OpsDashboardOverview, error)
	GetFiringAlertEventCounts(ctx context.Context, platform string, groupID *int64) (*OpsAlertEventCounts, error)
	ListAlertEvents(ctx context.Context, filter *OpsAlertEventFilter) ([]*OpsAlertEvent, error)
}

type dashboardHomeAvailabilitySource interface {
	Summarize(ctx context.Context, q AccountStatusSummaryQuery) (*AccountStatusSummary, error)
}

type dashboardHomeChannelSource interface {
	Overview(ctx context.Context, f ChannelMonitorV2Filter, admin bool, viewer int64, refresh bool) (*ObservationOverview, error)
}

type dashboardHomeChannelRuntime interface {
	GetChannelMonitorRuntime(ctx context.Context) ChannelMonitorRuntime
}

// DashboardHomeService builds GET /api/v1/admin/dashboard/home from the same
// services as the standalone endpoints. A failing or slow source only marks
// its own section.
type DashboardHomeService struct {
	stats        dashboardHomeStatsSource
	ops          dashboardHomeOpsSource
	availability dashboardHomeAvailabilitySource
	channels     dashboardHomeChannelSource
	runtime      dashboardHomeChannelRuntime
	budget       time.Duration
	timeouts     map[string]time.Duration
	now          func() time.Time
}

// NewDashboardHomeService adapts the concrete services; a nil one leaves its
// sections reporting SERVICE_UNAVAILABLE.
func NewDashboardHomeService(dashboard *DashboardService, ops *OpsService, summary *AccountStatusSummaryService, channels *ChannelMonitorOverviewService, settings *SettingService) *DashboardHomeService {
	var (
		statsSrc   dashboardHomeStatsSource
		opsSrc     dashboardHomeOpsSource
		availSrc   dashboardHomeAvailabilitySource
		channelSrc dashboardHomeChannelSource
		runtimeSrc dashboardHomeChannelRuntime
	)
	if dashboard != nil {
		statsSrc = dashboard
	}
	if ops != nil {
		opsSrc = ops
	}
	if summary != nil {
		availSrc = summary
	}
	if channels != nil {
		channelSrc = channels
	}
	if settings != nil {
		runtimeSrc = settings
	}
	return newDashboardHomeService(statsSrc, opsSrc, availSrc, channelSrc, runtimeSrc)
}

func newDashboardHomeService(stats dashboardHomeStatsSource, ops dashboardHomeOpsSource, availability dashboardHomeAvailabilitySource, channels dashboardHomeChannelSource, runtime dashboardHomeChannelRuntime) *DashboardHomeService {
	timeouts := make(map[string]time.Duration, len(dashboardHomeDefaultTimeouts))
	for name, d := range dashboardHomeDefaultTimeouts {
		timeouts[name] = d
	}
	return &DashboardHomeService{stats: stats, ops: ops, availability: availability, channels: channels, runtime: runtime,
		budget: dashboardHomeBudget, timeouts: timeouts, now: time.Now}
}

// DashboardHomeQuery selects the sections and the timezone of the hourly
// chart. A nil Location means the server timezone; an empty Include means all.
type DashboardHomeQuery struct {
	Location *time.Location
	Include  DashboardHomeInclude
	ViewerID int64
	Refresh  bool
}

// Build runs the included sections concurrently and never fails as a whole.
func (s *DashboardHomeService) Build(ctx context.Context, q DashboardHomeQuery) *DashboardHome {
	if q.Location == nil {
		q.Location = timezone.Location()
	}
	if len(q.Include) == 0 {
		q.Include, _ = ParseDashboardHomeInclude("")
	}
	now := s.now()
	ctx, cancel := context.WithTimeout(ctx, s.budget)
	defer cancel()

	home := &DashboardHome{GeneratedAt: now.UTC().Truncate(time.Second), Timezone: q.Location.String()}
	loaders := map[string]func(context.Context){
		DashboardHomeSectionStats: func(c context.Context) {
			home.Stats = runDashboardHomeSection(c, DashboardHomeSectionStats, s.timeouts[DashboardHomeSectionStats], func(c context.Context) (*DashboardHomeStats, error) { return s.loadStats(c, now) })
		},
		DashboardHomeSectionRates: func(c context.Context) {
			home.Rates = runDashboardHomeSection(c, DashboardHomeSectionRates, s.timeouts[DashboardHomeSectionRates], func(c context.Context) (*OpsLiveRates, error) { return s.loadRates(c, now) })
		},
		DashboardHomeSectionHourly: func(c context.Context) {
			home.Hourly = runDashboardHomeSection(c, DashboardHomeSectionHourly, s.timeouts[DashboardHomeSectionHourly], func(c context.Context) (*DashboardHomeHourly, error) { return s.loadHourly(c, q.Location, now) })
		},
		DashboardHomeSectionOps: func(c context.Context) {
			home.Ops = runDashboardHomeSection(c, DashboardHomeSectionOps, s.timeouts[DashboardHomeSectionOps], func(c context.Context) (*DashboardHomeOps, error) { return s.loadOps(c, now) })
		},
		DashboardHomeSectionAlerts: func(c context.Context) {
			home.Alerts = runDashboardHomeSection(c, DashboardHomeSectionAlerts, s.timeouts[DashboardHomeSectionAlerts], s.loadAlerts)
		},
		DashboardHomeSectionAvailability: func(c context.Context) {
			home.Availability = runDashboardHomeSection(c, DashboardHomeSectionAvailability, s.timeouts[DashboardHomeSectionAvailability], s.loadAvailability)
		},
		DashboardHomeSectionChannels: func(c context.Context) {
			home.Channels = runDashboardHomeSection(c, DashboardHomeSectionChannels, s.timeouts[DashboardHomeSectionChannels], func(c context.Context) (*DashboardHomeChannels, error) { return s.loadChannels(c, q, now) })
		},
	}
	var wg sync.WaitGroup
	for _, name := range q.Include.Names() {
		load := loaders[name]
		wg.Add(1)
		go func() {
			defer wg.Done()
			load(ctx)
		}()
	}
	wg.Wait()
	return home
}

type dashboardHomeResult[T any] struct {
	data *T
	err  error
}

// runDashboardHomeSection returns as soon as the section's deadline passes,
// even if the source ignores its context; the late result is discarded.
func runDashboardHomeSection[T any](parent context.Context, name string, timeout time.Duration, load func(context.Context) (*T, error)) *DashboardHomeSection[T] {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	done := make(chan dashboardHomeResult[T], 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- dashboardHomeResult[T]{err: fmt.Errorf("panic: %v", r)}
			}
		}()
		data, err := load(ctx)
		done <- dashboardHomeResult[T]{data: data, err: err}
	}()

	var res dashboardHomeResult[T]
	select {
	case res = <-done:
	case <-ctx.Done():
		select {
		case res = <-done:
		default:
			res.err = ctx.Err()
		}
	}
	status, reason := classifyDashboardHomeError(ctx, res.err)
	if status == DashboardHomeStatusOK {
		return &DashboardHomeSection[T]{Status: status, Data: res.data}
	}
	if status != DashboardHomeStatusDisabled {
		logger.L().Warn("dashboard_home.section_failed",
			zap.String("section", name), zap.String("status", status), zap.String("reason", reason),
			zap.String("error", logredact.RedactText(res.err.Error())))
	}
	return &DashboardHomeSection[T]{Status: status, Error: reason}
}

func classifyDashboardHomeError(ctx context.Context, err error) (status, reason string) {
	switch {
	case err == nil:
		return DashboardHomeStatusOK, ""
	case errors.Is(err, ErrOpsDisabled):
		return DashboardHomeStatusDisabled, infraerrors.Reason(ErrOpsDisabled)
	case errors.Is(err, ErrChannelMonitorDisabled):
		return DashboardHomeStatusDisabled, infraerrors.Reason(ErrChannelMonitorDisabled)
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || ctx.Err() != nil:
		return DashboardHomeStatusTimeout, DashboardHomeErrorSectionTimeout
	}
	if reason := infraerrors.Reason(err); reason != "" {
		return DashboardHomeStatusError, reason
	}
	return DashboardHomeStatusError, DashboardHomeErrorInternal
}
