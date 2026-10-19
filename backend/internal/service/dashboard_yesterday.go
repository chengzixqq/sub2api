package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

// DashboardYesterdaySameTime is yesterday's usage from 00:00 up to the moment
// that matches today's statistics, in the server timezone.
type DashboardYesterdaySameTime struct {
	Requests    int64
	ActualCost  float64
	AccountCost float64
	Until       time.Time
}

// GetYesterdaySameTime sums yesterday's usage up to the same time of day as
// stats. With pre-aggregation on, today's figures stop at the aggregation
// watermark (stats_updated_at), so the cutoff is that watermark minus one
// day; otherwise it is now minus one day. Whole hours come from the hourly
// aggregate and the partial edge from raw rows.
func (s *DashboardService) GetYesterdaySameTime(ctx context.Context, stats *usagestats.DashboardStats, now time.Time) (*DashboardYesterdaySameTime, error) {
	loc := timezone.Location()
	local := now.In(loc)
	todayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	ref := local
	if s.aggEnabled && stats != nil {
		if at, err := time.Parse(time.RFC3339, stats.StatsUpdatedAt); err == nil {
			ref = clampDashboardTime(at.In(loc), todayStart, local)
		}
	}
	yesterdayStart := todayStart.AddDate(0, 0, -1)
	until := ref.AddDate(0, 0, -1)
	if !until.After(yesterdayStart) {
		return &DashboardYesterdaySameTime{Until: yesterdayStart}, nil
	}

	options := usagequery.OptionsFrom(ctx)
	options.Timezone = ""
	points, err := s.GetUsageTrendWithUsageFilters(usagequery.WithOptions(ctx, options), yesterdayStart, until, "day",
		usagestats.UsageLogFilters{ModelFilterSource: usagestats.ModelSourceRequested})
	if err != nil {
		return nil, fmt.Errorf("get yesterday same time usage: %w", err)
	}
	out := &DashboardYesterdaySameTime{Until: until}
	for _, point := range points {
		out.Requests += point.Requests
		out.ActualCost += point.ActualCost
		if point.AccountCost != nil {
			out.AccountCost += *point.AccountCost
		}
	}
	return out, nil
}

func clampDashboardTime(t, lower, upper time.Time) time.Time {
	if t.Before(lower) {
		return lower
	}
	if t.After(upper) {
		return upper
	}
	return t
}
