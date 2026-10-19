package service

import (
	"sort"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

// Section statuses of GET /api/v1/admin/dashboard/home. The response is 200
// whenever authentication and parameters pass; each section reports its own
// outcome.
const (
	DashboardHomeStatusOK       = "ok"
	DashboardHomeStatusDisabled = "disabled"
	DashboardHomeStatusError    = "error"
	DashboardHomeStatusTimeout  = "timeout"

	// DashboardHomeErrorSectionTimeout marks a section that ran out of its own
	// time budget; the next refresh retries it.
	DashboardHomeErrorSectionTimeout = "SECTION_TIMEOUT"
	// DashboardHomeErrorInternal replaces errors without a stable reason so raw
	// database messages never reach the client.
	DashboardHomeErrorInternal = "INTERNAL_ERROR"
)

// Section names accepted by the include parameter.
const (
	DashboardHomeSectionStats        = "stats"
	DashboardHomeSectionRates        = "rates"
	DashboardHomeSectionHourly       = "hourly"
	DashboardHomeSectionOps          = "ops"
	DashboardHomeSectionAlerts       = "alerts"
	DashboardHomeSectionAvailability = "availability"
	DashboardHomeSectionChannels     = "channels"
)

var dashboardHomeSections = []string{
	DashboardHomeSectionStats, DashboardHomeSectionRates, DashboardHomeSectionHourly, DashboardHomeSectionOps,
	DashboardHomeSectionAlerts, DashboardHomeSectionAvailability, DashboardHomeSectionChannels,
}

var (
	ErrDashboardHomeInvalidTimezone = infraerrors.BadRequest("INVALID_TIMEZONE", "invalid timezone")
	ErrDashboardHomeInvalidInclude  = infraerrors.BadRequest("INVALID_INCLUDE", "invalid include, use stats,rates,hourly,ops,alerts,availability,channels")
)

// DashboardHomeInclude is the set of sections to build.
type DashboardHomeInclude map[string]bool

// ParseDashboardHomeInclude reads a comma-separated section list; blank means
// every section. Spaces and repeats are ignored, unknown names are rejected.
func ParseDashboardHomeInclude(raw string) (DashboardHomeInclude, error) {
	out := DashboardHomeInclude{}
	if strings.TrimSpace(raw) == "" {
		for _, name := range dashboardHomeSections {
			out[name] = true
		}
		return out, nil
	}
	known := map[string]bool{}
	for _, name := range dashboardHomeSections {
		known[name] = true
	}
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if !known[name] {
			return nil, ErrDashboardHomeInvalidInclude
		}
		out[name] = true
	}
	if len(out) == 0 {
		return nil, ErrDashboardHomeInvalidInclude
	}
	return out, nil
}

// Names returns the included sections sorted, for cache keys.
func (in DashboardHomeInclude) Names() []string {
	names := make([]string, 0, len(in))
	for name, on := range in {
		if on {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// ParseDashboardHomeTimezone resolves an IANA zone; blank means the server zone.
func ParseDashboardHomeTimezone(raw string) (*time.Location, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return timezone.Location(), nil
	}
	loc, err := time.LoadLocation(raw)
	if err != nil {
		return nil, ErrDashboardHomeInvalidTimezone
	}
	return loc, nil
}

// DashboardHomeSection is one independently loaded part of the home aggregate.
// Error carries a stable reason when Status is not ok; Data is set only when ok.
type DashboardHomeSection[T any] struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	Data   *T     `json:"data,omitempty"`
}

func (s *DashboardHomeSection[T]) degraded() bool {
	return s != nil && (s.Status == DashboardHomeStatusError || s.Status == DashboardHomeStatusTimeout)
}

// DashboardHome is the mobile home aggregate. Sections left out by include are
// omitted from the JSON.
type DashboardHome struct {
	GeneratedAt  time.Time                                        `json:"generated_at"`
	Timezone     string                                           `json:"timezone"`
	Stats        *DashboardHomeSection[DashboardHomeStats]        `json:"stats,omitempty"`
	Rates        *DashboardHomeSection[OpsLiveRates]              `json:"rates,omitempty"`
	Hourly       *DashboardHomeSection[DashboardHomeHourly]       `json:"hourly,omitempty"`
	Ops          *DashboardHomeSection[DashboardHomeOps]          `json:"ops,omitempty"`
	Alerts       *DashboardHomeSection[DashboardHomeAlerts]       `json:"alerts,omitempty"`
	Availability *DashboardHomeSection[DashboardHomeAvailability] `json:"availability,omitempty"`
	Channels     *DashboardHomeSection[DashboardHomeChannels]     `json:"channels,omitempty"`
}

// Degraded reports whether any section failed or timed out. Disabled sections
// reflect site settings and do not count.
func (h *DashboardHome) Degraded() bool {
	if h == nil {
		return false
	}
	return h.Stats.degraded() || h.Rates.degraded() || h.Hourly.degraded() || h.Ops.degraded() ||
		h.Alerts.degraded() || h.Availability.degraded() || h.Channels.degraded()
}

// ETagView is a copy without the timestamps that only record when the
// aggregate was built, so an unchanged rebuild keeps the same validator.
func (h *DashboardHome) ETagView() *DashboardHome {
	if h == nil {
		return nil
	}
	view := *h
	view.GeneratedAt = time.Time{}
	view.Rates = withDashboardHomeData(h.Rates, func(d *OpsLiveRates) { d.StartTime, d.EndTime = time.Time{}, time.Time{} })
	view.Ops = withDashboardHomeData(h.Ops, func(d *DashboardHomeOps) { d.StartTime, d.EndTime = time.Time{}, time.Time{} })
	view.Availability = withDashboardHomeData(h.Availability, func(d *DashboardHomeAvailability) { d.CollectedAt = time.Time{} })
	return &view
}

func withDashboardHomeData[T any](section *DashboardHomeSection[T], edit func(*T)) *DashboardHomeSection[T] {
	if section == nil || section.Data == nil {
		return section
	}
	copied := *section
	data := *section.Data
	edit(&data)
	copied.Data = &data
	return &copied
}

// DashboardHomeStats is today's billing in the server timezone (the same day
// boundary as dashboard/stats). The yesterday_same_time_* fields are null when
// that comparison could not be computed.
type DashboardHomeStats struct {
	Timezone                     string     `json:"timezone"`
	TodayRequests                int64      `json:"today_requests"`
	TodayTokens                  int64      `json:"today_tokens"`
	TodayCost                    float64    `json:"today_cost"`
	TodayActualCost              float64    `json:"today_actual_cost"`
	TodayAccountCost             float64    `json:"today_account_cost"`
	ActiveUsers                  int64      `json:"active_users"`
	StatsUpdatedAt               string     `json:"stats_updated_at"`
	StatsStale                   bool       `json:"stats_stale"`
	YesterdaySameTimeRequests    *int64     `json:"yesterday_same_time_requests"`
	YesterdaySameTimeActualCost  *float64   `json:"yesterday_same_time_actual_cost"`
	YesterdaySameTimeAccountCost *float64   `json:"yesterday_same_time_account_cost"`
	YesterdaySameTimeUntil       *time.Time `json:"yesterday_same_time_until"`
}

// DashboardHomeHourly splits one hourly trend query into today and yesterday
// of the requested timezone. Hours without usage are absent.
type DashboardHomeHourly struct {
	Timezone  string                      `json:"timezone"`
	Today     []usagestats.TrendDataPoint `json:"today"`
	Yesterday []usagestats.TrendDataPoint `json:"yesterday"`
}

// DashboardHomeOps is the last five minutes of the ops overview.
type DashboardHomeOps struct {
	StartTime            time.Time `json:"start_time"`
	EndTime              time.Time `json:"end_time"`
	HealthScore          int       `json:"health_score"`
	SLA                  float64   `json:"sla"`
	ErrorRate            float64   `json:"error_rate"`
	UpstreamErrorRate    float64   `json:"upstream_error_rate"`
	Upstream429Count     int64     `json:"upstream_429_count"`
	Upstream529Count     int64     `json:"upstream_529_count"`
	SuccessCount         int64     `json:"success_count"`
	ErrorCountTotal      int64     `json:"error_count_total"`
	ErrorCountSLA        int64     `json:"error_count_sla"`
	BusinessLimitedCount int64     `json:"business_limited_count"`
	RequestCountTotal    int64     `json:"request_count_total"`
	RequestCountSLA      int64     `json:"request_count_sla"`
	TokenConsumed        int64     `json:"token_consumed"`
}

// DashboardHomeAlerts counts every firing event (silenced ones included) and
// lists the newest few.
type DashboardHomeAlerts struct {
	FiringCount int64            `json:"firing_count"`
	BySeverity  map[string]int64 `json:"by_severity"`
	Top         []*OpsAlertEvent `json:"top"`
}

// DashboardHomeAvailability is the account status summary with only the
// accounts that need attention (error, overloaded, rate limited, temporarily
// unschedulable).
type DashboardHomeAvailability struct {
	CollectedAt       time.Time                   `json:"collected_at"`
	Totals            AccountStateCounts          `json:"totals"`
	Groups            []AccountStatusGroupSummary `json:"groups"`
	Accounts          []AccountStatusItem         `json:"accounts"`
	AccountsTruncated bool                        `json:"accounts_truncated"`
}

// DashboardHomeChannels buckets channel monitor rows by current state; stale
// and unrecognised states count as unknown.
type DashboardHomeChannels struct {
	Total          int    `json:"total"`
	Healthy        int    `json:"healthy"`
	Warning        int    `json:"warning"`
	Critical       int    `json:"critical"`
	Unknown        int    `json:"unknown"`
	CoverageState  string `json:"coverage_state"`
	CollectorState string `json:"collector_state"`
}
