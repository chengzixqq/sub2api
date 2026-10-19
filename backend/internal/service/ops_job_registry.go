package service

import (
	"context"
	"time"

	"github.com/robfig/cron/v3"
)

// Job heartbeat status values. Clients should treat unknown values as
// OpsJobStatusUnknown.
const (
	OpsJobStatusOK       = "ok"
	OpsJobStatusFailing  = "failing"
	OpsJobStatusStale    = "stale"
	OpsJobStatusUnknown  = "unknown"
	OpsJobStatusDisabled = "disabled"
)

// opsJobStaleSlack is added to a job's run timeout before a missed run
// counts as stale.
const opsJobStaleSlack = time.Minute

// opsJobSchedule is how often a background job is expected to succeed.
// Interval jobs set interval; cron jobs set cron + loc.
type opsJobSchedule struct {
	disabled bool
	interval time.Duration
	cron     cron.Schedule
	loc      *time.Location
	grace    time.Duration
}

// opsCleanupScheduleReader is implemented by OpsCleanupService so the
// dashboard reads the cron that is actually running.
type opsCleanupScheduleReader interface {
	CurrentSchedule() (spec string, loc *time.Location, active bool)
}

// resolveOpsJobSchedules lists the background jobs that write heartbeats and
// their current expected cadence.
func (s *OpsService) resolveOpsJobSchedules(ctx context.Context) map[string]opsJobSchedule {
	aggregationEnabled := s.cfg == nil || s.cfg.Ops.Aggregation.Enabled

	out := map[string]opsJobSchedule{
		opsMetricsCollectorJobName: {
			interval: s.opsMetricsCollectorInterval(ctx),
			grace:    opsMetricsCollectorTimeout + opsJobStaleSlack,
		},
		opsAlertEvaluatorJobName: {
			interval: opsAlertEvaluatorIntervalFromSettings(s.GetOpsAlertRuntimeSettings(ctx)),
			grace:    opsAlertEvaluatorTimeout + opsJobStaleSlack,
		},
		opsAggHourlyJobName: {
			disabled: !aggregationEnabled,
			interval: opsAggHourlyInterval,
			grace:    opsAggHourlyTimeout + opsJobStaleSlack,
		},
		opsAggDailyJobName: {
			disabled: !aggregationEnabled,
			interval: opsAggDailyInterval,
			grace:    opsAggDailyTimeout + opsJobStaleSlack,
		},
	}
	// An unreadable email config leaves the report job unregistered (unknown).
	if emailCfg, err := s.GetEmailNotificationConfig(ctx); err == nil {
		out[opsScheduledReportJobName] = opsJobSchedule{
			disabled: !opsScheduledReportsConfigured(emailCfg),
			interval: opsScheduledReportTickInterval,
			grace:    opsScheduledReportRunTimeout + opsJobStaleSlack,
		}
	}
	if sched, ok := s.opsCleanupJobSchedule(); ok {
		out[opsCleanupJobName] = sched
	}
	return out
}

func (s *OpsService) opsMetricsCollectorInterval(ctx context.Context) time.Duration {
	if s.settingRepo == nil {
		return opsMetricsCollectorMinInterval
	}
	return opsMetricsCollectorIntervalFromRaw(s.settingRepo.GetValue(ctx, SettingKeyOpsMetricsIntervalSeconds))
}

// opsCleanupJobSchedule reports false when the cleanup schedule cannot be
// read, so the job shows as unknown rather than guessing.
func (s *OpsService) opsCleanupJobSchedule() (opsJobSchedule, bool) {
	reader, ok := s.cleanupReloader.(opsCleanupScheduleReader)
	if !ok {
		return opsJobSchedule{}, false
	}
	spec, loc, active := reader.CurrentSchedule()
	if !active {
		return opsJobSchedule{disabled: true}, true
	}
	sched, err := opsCleanupCronParser.Parse(spec)
	if err != nil {
		return opsJobSchedule{}, false
	}
	if loc == nil {
		loc = time.UTC
	}
	return opsJobSchedule{cron: sched, loc: loc, grace: opsCleanupRunTimeout + opsJobStaleSlack}, true
}

// annotateJobHeartbeats adds expected cadence and status to heartbeat copies.
func (s *OpsService) annotateJobHeartbeats(ctx context.Context, now time.Time, heartbeats []*OpsJobHeartbeat) []*OpsJobHeartbeat {
	if len(heartbeats) == 0 {
		return heartbeats
	}
	return annotateOpsJobHeartbeats(now, heartbeats, s.resolveOpsJobSchedules(ctx))
}

// annotateOpsJobHeartbeats returns copies with expected_interval_seconds,
// next_expected_at and status filled in; the input is not modified.
func annotateOpsJobHeartbeats(now time.Time, heartbeats []*OpsJobHeartbeat, schedules map[string]opsJobSchedule) []*OpsJobHeartbeat {
	if heartbeats == nil {
		return nil
	}
	out := make([]*OpsJobHeartbeat, 0, len(heartbeats))
	for _, hb := range heartbeats {
		if hb == nil {
			out = append(out, nil)
			continue
		}
		item := *hb
		sched, registered := schedules[hb.JobName]
		item.Status, item.ExpectedIntervalSeconds, item.NextExpectedAt = opsJobHeartbeatStatus(now, hb, sched, registered)
		out = append(out, &item)
	}
	return out
}

// opsJobHeartbeatStatus judges one heartbeat:
//   - disabled: the job is registered but turned off by settings
//   - stale: interval jobs without a success for 2 x interval + grace; cron
//     jobs whose first run after the last success is overdue by grace. This
//     wins over failing, so a job that keeps failing turns stale.
//   - failing: the latest error is newer than the latest success
//   - unknown: no success yet, or the job is not registered
func opsJobHeartbeatStatus(now time.Time, hb *OpsJobHeartbeat, sched opsJobSchedule, registered bool) (string, *int64, *time.Time) {
	failing := hb.LastErrorAt != nil && (hb.LastSuccessAt == nil || hb.LastErrorAt.After(*hb.LastSuccessAt))
	if registered && sched.disabled {
		return OpsJobStatusDisabled, nil, nil
	}
	if !registered || !sched.valid() {
		if failing {
			return OpsJobStatusFailing, nil, nil
		}
		return OpsJobStatusUnknown, nil, nil
	}

	expected := sched.expectedIntervalSeconds(now)
	next := sched.nextAfter(latestOpsTime(hb.LastRunAt, hb.LastSuccessAt))
	switch {
	case hb.LastSuccessAt != nil && sched.overdue(now, *hb.LastSuccessAt):
		return OpsJobStatusStale, expected, next
	case failing:
		return OpsJobStatusFailing, expected, next
	case hb.LastSuccessAt == nil:
		return OpsJobStatusUnknown, expected, next
	default:
		return OpsJobStatusOK, expected, next
	}
}

func (s opsJobSchedule) valid() bool {
	return s.cron != nil || s.interval > 0
}

func (s opsJobSchedule) location() *time.Location {
	if s.loc == nil {
		return time.UTC
	}
	return s.loc
}

func (s opsJobSchedule) expectedIntervalSeconds(now time.Time) *int64 {
	var period time.Duration
	if s.cron != nil {
		first := s.cron.Next(now.In(s.location()))
		period = s.cron.Next(first).Sub(first)
	} else {
		period = s.interval
	}
	if period <= 0 {
		return nil
	}
	seconds := int64(period / time.Second)
	return &seconds
}

func (s opsJobSchedule) nextAfter(base *time.Time) *time.Time {
	if base == nil {
		return nil
	}
	var next time.Time
	if s.cron != nil {
		next = s.cron.Next(base.In(s.location()))
		if next.IsZero() {
			return nil
		}
	} else {
		next = base.Add(s.interval)
	}
	next = next.UTC()
	return &next
}

func (s opsJobSchedule) overdue(now, lastSuccess time.Time) bool {
	if s.cron != nil {
		due := s.cron.Next(lastSuccess.In(s.location()))
		return !due.IsZero() && now.After(due.Add(s.grace))
	}
	return now.Sub(lastSuccess) > 2*s.interval+s.grace
}

func latestOpsTime(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.After(*a):
		return b
	default:
		return a
	}
}
