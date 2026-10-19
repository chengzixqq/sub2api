//go:build unit

package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func intervalJob(interval, grace time.Duration) opsJobSchedule {
	return opsJobSchedule{interval: interval, grace: grace}
}

func cronJob(t *testing.T, spec string, loc *time.Location, grace time.Duration) opsJobSchedule {
	t.Helper()
	sched, err := opsCleanupCronParser.Parse(spec)
	require.NoError(t, err)
	return opsJobSchedule{cron: sched, loc: loc, grace: grace}
}

func TestOpsJobHeartbeatStatus_IntervalJob(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	sched := intervalJob(time.Minute, 70*time.Second)

	lastRun := now.Add(-31 * time.Second)
	ok := &OpsJobHeartbeat{JobName: "j", LastRunAt: &lastRun, LastSuccessAt: timePtr(now.Add(-30 * time.Second))}
	status, expected, next := opsJobHeartbeatStatus(now, ok, sched, true)
	require.Equal(t, OpsJobStatusOK, status)
	require.NotNil(t, expected)
	require.Equal(t, int64(60), *expected)
	require.NotNil(t, next)
	require.True(t, next.Equal(now.Add(30*time.Second)), "the later of last_run_at / last_success_at plus the interval")
	require.Equal(t, time.UTC, next.Location())

	// 2 x 60s + 70s = 190s of silence is still tolerated.
	edge := &OpsJobHeartbeat{JobName: "j", LastSuccessAt: timePtr(now.Add(-190 * time.Second))}
	status, _, _ = opsJobHeartbeatStatus(now, edge, sched, true)
	require.Equal(t, OpsJobStatusOK, status)

	stale := &OpsJobHeartbeat{JobName: "j", LastSuccessAt: timePtr(now.Add(-4 * time.Minute))}
	status, _, next = opsJobHeartbeatStatus(now, stale, sched, true)
	require.Equal(t, OpsJobStatusStale, status)
	require.True(t, next.Equal(now.Add(-3*time.Minute)), "next falls back to last_success_at + interval")

	// A newer error is failing while the last success is still recent.
	failing := &OpsJobHeartbeat{
		JobName:       "j",
		LastRunAt:     timePtr(now.Add(-time.Second)),
		LastSuccessAt: timePtr(now.Add(-2 * time.Minute)),
		LastErrorAt:   timePtr(now.Add(-time.Second)),
	}
	status, expected, _ = opsJobHeartbeatStatus(now, failing, sched, true)
	require.Equal(t, OpsJobStatusFailing, status)
	require.Equal(t, int64(60), *expected)

	// Once the last success is overdue, staleness wins over a newer error.
	failingTooLong := &OpsJobHeartbeat{
		JobName:       "j",
		LastRunAt:     timePtr(now.Add(-time.Second)),
		LastSuccessAt: timePtr(now.Add(-10 * time.Minute)),
		LastErrorAt:   timePtr(now.Add(-time.Second)),
	}
	status, expected, _ = opsJobHeartbeatStatus(now, failingTooLong, sched, true)
	require.Equal(t, OpsJobStatusStale, status)
	require.Equal(t, int64(60), *expected)

	neverSucceeded := &OpsJobHeartbeat{JobName: "j", LastErrorAt: timePtr(now.Add(-time.Hour))}
	status, _, _ = opsJobHeartbeatStatus(now, neverSucceeded, sched, true)
	require.Equal(t, OpsJobStatusFailing, status)

	empty := &OpsJobHeartbeat{JobName: "j"}
	status, expected, next = opsJobHeartbeatStatus(now, empty, sched, true)
	require.Equal(t, OpsJobStatusUnknown, status)
	require.Equal(t, int64(60), *expected)
	require.Nil(t, next)

	hourly := intervalJob(10*time.Minute, 6*time.Minute)
	status, _, _ = opsJobHeartbeatStatus(now, &OpsJobHeartbeat{LastSuccessAt: timePtr(now.Add(-15 * time.Minute))}, hourly, true)
	require.Equal(t, OpsJobStatusOK, status)
	status, _, _ = opsJobHeartbeatStatus(now, &OpsJobHeartbeat{LastSuccessAt: timePtr(now.Add(-27 * time.Minute))}, hourly, true)
	require.Equal(t, OpsJobStatusStale, status)
}

// Every run fails after 11:59: the job reads failing first and turns stale
// once no success has been seen for 2 x interval + grace.
func TestOpsJobHeartbeatStatus_PersistentFailureTurnsStale(t *testing.T) {
	lastSuccess := time.Date(2026, 10, 18, 11, 59, 0, 0, time.UTC)
	sched := intervalJob(time.Minute, opsMetricsCollectorTimeout+opsJobStaleSlack)
	failingAt := func(now time.Time) *OpsJobHeartbeat {
		errAt := now.Add(-time.Second)
		return &OpsJobHeartbeat{JobName: opsMetricsCollectorJobName, LastRunAt: &errAt, LastSuccessAt: &lastSuccess, LastErrorAt: &errAt}
	}

	for _, tc := range []struct {
		now  time.Time
		want string
	}{
		{time.Date(2026, 10, 18, 12, 1, 0, 0, time.UTC), OpsJobStatusFailing},
		{time.Date(2026, 10, 18, 12, 2, 0, 0, time.UTC), OpsJobStatusFailing},
		{time.Date(2026, 10, 18, 12, 5, 0, 0, time.UTC), OpsJobStatusStale},
		{time.Date(2026, 10, 18, 12, 30, 0, 0, time.UTC), OpsJobStatusStale},
		{time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC), OpsJobStatusStale},
	} {
		status, _, _ := opsJobHeartbeatStatus(tc.now, failingAt(tc.now), sched, true)
		require.Equal(t, tc.want, status, "now=%s", tc.now.Format(time.TimeOnly))
	}

	shanghai, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	cleanup := cronJob(t, "0 2 * * *", shanghai, opsCleanupRunTimeout+opsJobStaleSlack)
	cleanupFailed := &OpsJobHeartbeat{
		JobName:       opsCleanupJobName,
		LastRunAt:     timePtr(time.Date(2026, 10, 19, 2, 0, 0, 0, shanghai).UTC()),
		LastSuccessAt: timePtr(time.Date(2026, 10, 18, 2, 5, 0, 0, shanghai).UTC()),
		LastErrorAt:   timePtr(time.Date(2026, 10, 19, 2, 1, 0, 0, shanghai).UTC()),
	}
	status, _, _ := opsJobHeartbeatStatus(time.Date(2026, 10, 19, 2, 10, 0, 0, shanghai), cleanupFailed, cleanup, true)
	require.Equal(t, OpsJobStatusFailing, status, "inside the run grace")
	status, _, _ = opsJobHeartbeatStatus(time.Date(2026, 10, 19, 9, 0, 0, 0, shanghai), cleanupFailed, cleanup, true)
	require.Equal(t, OpsJobStatusStale, status, "a failed nightly cleanup is stale by morning")
}

func TestOpsJobHeartbeatStatus_CronCleanupNotStaleDuringDay(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	sched := cronJob(t, "0 2 * * *", shanghai, 31*time.Minute)

	lastRun := time.Date(2026, 10, 18, 2, 0, 0, 0, shanghai)
	lastSuccess := time.Date(2026, 10, 18, 2, 5, 0, 0, shanghai)
	hb := &OpsJobHeartbeat{JobName: opsCleanupJobName, LastRunAt: timePtr(lastRun.UTC()), LastSuccessAt: timePtr(lastSuccess.UTC())}
	nextDue := time.Date(2026, 10, 19, 2, 0, 0, 0, shanghai)

	status, expected, next := opsJobHeartbeatStatus(time.Date(2026, 10, 18, 15, 0, 0, 0, shanghai), hb, sched, true)
	require.Equal(t, OpsJobStatusOK, status, "a nightly cleanup is not stale during the day")
	require.NotNil(t, next)
	require.True(t, next.Equal(nextDue), "next=%s", next)
	require.Equal(t, time.UTC, next.Location())
	require.Equal(t, int64(86400), *expected)

	status, _, _ = opsJobHeartbeatStatus(time.Date(2026, 10, 19, 2, 20, 0, 0, shanghai), hb, sched, true)
	require.Equal(t, OpsJobStatusOK, status, "inside the run grace")

	status, _, next = opsJobHeartbeatStatus(time.Date(2026, 10, 19, 2, 40, 0, 0, shanghai), hb, sched, true)
	require.Equal(t, OpsJobStatusStale, status)
	require.True(t, next.Equal(nextDue), "the missed run is reported as the expected time")

	hourly := cronJob(t, "0 * * * *", time.UTC, 31*time.Minute)
	_, expected, _ = opsJobHeartbeatStatus(time.Date(2026, 10, 18, 15, 10, 0, 0, time.UTC), hb, hourly, true)
	require.Equal(t, int64(3600), *expected)
}

func TestOpsJobHeartbeatStatus_DisabledAndUnregistered(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	old := &OpsJobHeartbeat{JobName: opsCleanupJobName, LastSuccessAt: timePtr(now.Add(-30 * 24 * time.Hour)), LastErrorAt: timePtr(now.Add(-29 * 24 * time.Hour))}
	status, expected, next := opsJobHeartbeatStatus(now, old, opsJobSchedule{disabled: true}, true)
	require.Equal(t, OpsJobStatusDisabled, status)
	require.Nil(t, expected)
	require.Nil(t, next)

	legacyFailing := &OpsJobHeartbeat{JobName: "legacy_job", LastSuccessAt: timePtr(now.Add(-time.Hour)), LastErrorAt: timePtr(now.Add(-time.Minute))}
	status, expected, next = opsJobHeartbeatStatus(now, legacyFailing, opsJobSchedule{}, false)
	require.Equal(t, OpsJobStatusFailing, status)
	require.Nil(t, expected)
	require.Nil(t, next)

	legacyOK := &OpsJobHeartbeat{JobName: "legacy_job", LastSuccessAt: timePtr(now.Add(-48 * time.Hour))}
	status, expected, _ = opsJobHeartbeatStatus(now, legacyOK, opsJobSchedule{}, false)
	require.Equal(t, OpsJobStatusUnknown, status)
	require.Nil(t, expected)

	input := []*OpsJobHeartbeat{
		{JobName: opsMetricsCollectorJobName, LastSuccessAt: timePtr(now.Add(-10 * time.Second))},
		{JobName: "legacy_job"},
	}
	out := annotateOpsJobHeartbeats(now, input, map[string]opsJobSchedule{
		opsMetricsCollectorJobName: intervalJob(time.Minute, 70*time.Second),
	})
	require.Len(t, out, 2)
	require.Equal(t, OpsJobStatusOK, out[0].Status)
	require.Equal(t, OpsJobStatusUnknown, out[1].Status)
	require.NotSame(t, input[0], out[0], "annotation returns copies")
	require.Empty(t, input[0].Status, "input heartbeats are not mutated")
	require.Nil(t, input[0].ExpectedIntervalSeconds)
	require.Nil(t, annotateOpsJobHeartbeats(now, nil, nil))

	raw, err := json.Marshal(out[1])
	require.NoError(t, err)
	require.Contains(t, string(raw), `"expected_interval_seconds":null`)
	require.Contains(t, string(raw), `"next_expected_at":null`)
	require.Contains(t, string(raw), `"status":"unknown"`)
}

func TestOpsMetricsCollectorIntervalFromRaw_PreservesClamp(t *testing.T) {
	cases := map[string]time.Duration{
		"":      time.Minute,
		"  ":    time.Minute,
		"59":    time.Minute,
		"120":   2 * time.Minute,
		" 300 ": 5 * time.Minute,
		"99999": time.Hour,
		"abc":   time.Minute,
	}
	for raw, want := range cases {
		require.Equal(t, want, opsMetricsCollectorIntervalFromRaw(raw, nil), raw)
	}
	require.Equal(t, time.Minute, opsMetricsCollectorIntervalFromRaw("300", errors.New("boom")))
}

func TestOpsAlertEvaluatorIntervalFromSettings(t *testing.T) {
	require.Equal(t, time.Minute, opsAlertEvaluatorIntervalFromSettings(nil, nil))
	require.Equal(t, time.Minute, opsAlertEvaluatorIntervalFromSettings(&OpsAlertRuntimeSettings{EvaluationIntervalSeconds: 120}, errors.New("boom")))
	require.Equal(t, time.Minute, opsAlertEvaluatorIntervalFromSettings(&OpsAlertRuntimeSettings{EvaluationIntervalSeconds: 0}, nil))
	require.Equal(t, time.Minute, opsAlertEvaluatorIntervalFromSettings(&OpsAlertRuntimeSettings{EvaluationIntervalSeconds: 86401}, nil))
	require.Equal(t, 2*time.Minute, opsAlertEvaluatorIntervalFromSettings(&OpsAlertRuntimeSettings{EvaluationIntervalSeconds: 120}, nil))
	require.Equal(t, 24*time.Hour, opsAlertEvaluatorIntervalFromSettings(&OpsAlertRuntimeSettings{EvaluationIntervalSeconds: 86400}, nil))
}

type cleanupScheduleReaderFake struct {
	spec   string
	loc    *time.Location
	active bool
}

func (f *cleanupScheduleReaderFake) Reload(context.Context) error { return nil }

func (f *cleanupScheduleReaderFake) CurrentSchedule() (string, *time.Location, bool) {
	return f.spec, f.loc, f.active
}

func writeJSONSetting(t *testing.T, repo *runtimeSettingRepoStub, key string, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	repo.values[key] = string(raw)
}

func TestOpsServiceResolveJobSchedules(t *testing.T) {
	ctx := context.Background()
	repo := newRuntimeSettingRepoStub()
	repo.values[SettingKeyOpsMetricsIntervalSeconds] = "300"
	writeJSONSetting(t, repo, SettingKeyOpsAlertRuntimeSettings, &OpsAlertRuntimeSettings{EvaluationIntervalSeconds: 120})
	cfg := &config.Config{}
	cfg.Ops.Enabled = true
	cfg.Ops.Aggregation.Enabled = true
	svc := &OpsService{settingRepo: repo, cfg: cfg}

	got := svc.resolveOpsJobSchedules(ctx)
	require.Equal(t, 5*time.Minute, got[opsMetricsCollectorJobName].interval)
	require.Equal(t, opsMetricsCollectorTimeout+opsJobStaleSlack, got[opsMetricsCollectorJobName].grace)
	require.Equal(t, 2*time.Minute, got[opsAlertEvaluatorJobName].interval)
	require.Equal(t, opsAlertEvaluatorTimeout+opsJobStaleSlack, got[opsAlertEvaluatorJobName].grace)
	require.Equal(t, opsAggHourlyInterval, got[opsAggHourlyJobName].interval)
	require.False(t, got[opsAggHourlyJobName].disabled)
	require.Equal(t, opsAggDailyInterval, got[opsAggDailyJobName].interval)
	require.True(t, got[opsScheduledReportJobName].disabled, "no report is configured by default")
	_, hasCleanup := got[opsCleanupJobName]
	require.False(t, hasCleanup, "cleanup is unregistered without a schedule reader")

	repo.values[SettingKeyOpsMetricsIntervalSeconds] = "30"
	require.Equal(t, time.Minute, svc.resolveOpsJobSchedules(ctx)[opsMetricsCollectorJobName].interval)
	repo.values[SettingKeyOpsMetricsIntervalSeconds] = "abc"
	require.Equal(t, time.Minute, svc.resolveOpsJobSchedules(ctx)[opsMetricsCollectorJobName].interval)

	cfg.Ops.Aggregation.Enabled = false
	got = svc.resolveOpsJobSchedules(ctx)
	require.True(t, got[opsAggHourlyJobName].disabled)
	require.True(t, got[opsAggDailyJobName].disabled)

	report := defaultOpsEmailNotificationConfig()
	report.Report.Enabled = true
	report.Report.DailySummaryEnabled = true
	writeJSONSetting(t, repo, SettingKeyOpsEmailNotificationConfig, report)
	got = svc.resolveOpsJobSchedules(ctx)
	require.False(t, got[opsScheduledReportJobName].disabled)
	require.Equal(t, opsScheduledReportTickInterval, got[opsScheduledReportJobName].interval)

	svc.SetCleanupReloader(&cleanupScheduleReaderFake{spec: "0 3 * * *", loc: time.UTC, active: true})
	got = svc.resolveOpsJobSchedules(ctx)
	cleanup, ok := got[opsCleanupJobName]
	require.True(t, ok)
	require.False(t, cleanup.disabled)
	require.NotNil(t, cleanup.cron)
	require.Equal(t, opsCleanupRunTimeout+opsJobStaleSlack, cleanup.grace)
	from := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	require.True(t, cleanup.cron.Next(from).Equal(time.Date(2026, 10, 19, 3, 0, 0, 0, time.UTC)))

	svc.SetCleanupReloader(&cleanupScheduleReaderFake{active: false})
	require.True(t, svc.resolveOpsJobSchedules(ctx)[opsCleanupJobName].disabled)

	svc.SetCleanupReloader(&cleanupScheduleReaderFake{spec: "not a cron", active: true})
	_, hasCleanup = svc.resolveOpsJobSchedules(ctx)[opsCleanupJobName]
	require.False(t, hasCleanup, "an unparsable schedule is reported as unknown")

	svc.SetCleanupReloader(&runtimeCleanupReloader{})
	_, hasCleanup = svc.resolveOpsJobSchedules(ctx)[opsCleanupJobName]
	require.False(t, hasCleanup, "reloaders that cannot report a schedule stay unregistered")

	repo.getValueFn = func(key string) (string, error) {
		if key == SettingKeyOpsEmailNotificationConfig {
			return "", errors.New("settings unavailable")
		}
		if v, ok := repo.values[key]; ok {
			return v, nil
		}
		return "", ErrSettingNotFound
	}
	_, hasReports := svc.resolveOpsJobSchedules(ctx)[opsScheduledReportJobName]
	require.False(t, hasReports, "an unreadable email config is reported as unknown, not disabled")
}

func TestOpsScheduledReportsConfigured(t *testing.T) {
	require.False(t, opsScheduledReportsConfigured(nil))
	cfg := defaultOpsEmailNotificationConfig()
	require.False(t, opsScheduledReportsConfigured(cfg))
	cfg.Report.Enabled = true
	require.False(t, opsScheduledReportsConfigured(cfg), "no report type is enabled")
	cfg.Report.WeeklySummaryEnabled = true
	cfg.Report.WeeklySummarySchedule = "  "
	require.False(t, opsScheduledReportsConfigured(cfg), "empty schedule")
	cfg.Report.WeeklySummarySchedule = "not cron"
	require.False(t, opsScheduledReportsConfigured(cfg), "invalid schedule")
	cfg.Report.WeeklySummarySchedule = "0 9 * * 1"
	require.True(t, opsScheduledReportsConfigured(cfg))
	cfg.Report.Enabled = false
	require.False(t, opsScheduledReportsConfigured(cfg))
}

func TestOpsCleanupServiceCurrentSchedule(t *testing.T) {
	var nilSvc *OpsCleanupService
	_, _, active := nilSvc.CurrentSchedule()
	require.False(t, active)

	svc := makeOverlayService(newRuntimeSettingRepoStub(), config.OpsCleanupConfig{Enabled: true, Schedule: "0 3 * * *"})
	svc.cfg.Timezone = "Asia/Shanghai"
	_, _, active = svc.CurrentSchedule()
	require.False(t, active, "not scheduled before the cron starts")

	svc.mu.Lock()
	require.NoError(t, svc.applyScheduleLocked(context.Background()))
	svc.mu.Unlock()
	t.Cleanup(svc.Stop)

	spec, loc, active := svc.CurrentSchedule()
	require.True(t, active)
	require.Equal(t, "0 3 * * *", spec)
	require.Equal(t, "Asia/Shanghai", loc.String())

	svc.Stop()
	_, _, active = svc.CurrentSchedule()
	require.False(t, active)

	disabled := makeOverlayService(newRuntimeSettingRepoStub(), config.OpsCleanupConfig{Enabled: false, Schedule: "0 3 * * *"})
	disabled.mu.Lock()
	require.NoError(t, disabled.applyScheduleLocked(context.Background()))
	disabled.mu.Unlock()
	_, _, active = disabled.CurrentSchedule()
	require.False(t, active)
}

type overviewHeartbeatRepo struct {
	OpsRepository
	heartbeats []*OpsJobHeartbeat
}

func (r *overviewHeartbeatRepo) GetDashboardOverview(context.Context, *OpsDashboardFilter) (*OpsDashboardOverview, error) {
	return &OpsDashboardOverview{RequestCountTotal: 10, RequestCountSLA: 10, SuccessCount: 10}, nil
}

func (r *overviewHeartbeatRepo) GetLatestSystemMetrics(context.Context, int) (*OpsSystemMetricsSnapshot, error) {
	return nil, sql.ErrNoRows
}

func (r *overviewHeartbeatRepo) ListJobHeartbeats(context.Context) ([]*OpsJobHeartbeat, error) {
	return r.heartbeats, nil
}

func TestGetDashboardOverview_AnnotatesJobHeartbeats(t *testing.T) {
	now := time.Now().UTC()
	repo := &overviewHeartbeatRepo{heartbeats: []*OpsJobHeartbeat{
		{JobName: opsMetricsCollectorJobName, LastRunAt: timePtr(now.Add(-11 * time.Second)), LastSuccessAt: timePtr(now.Add(-10 * time.Second))},
		{JobName: opsAlertEvaluatorJobName, LastSuccessAt: timePtr(now.Add(-2 * time.Minute)), LastErrorAt: timePtr(now.Add(-time.Minute)), LastError: stringPtr("boom")},
		{JobName: opsAggHourlyJobName, LastSuccessAt: timePtr(now.Add(-2 * time.Hour)), LastErrorAt: timePtr(now.Add(-time.Minute)), LastError: stringPtr("boom")},
	}}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	got, err := svc.GetDashboardOverview(context.Background(), &OpsDashboardFilter{
		StartTime: now.Add(-time.Hour), EndTime: now, QueryMode: OpsQueryModeRaw,
	})
	require.NoError(t, err)
	require.Len(t, got.JobHeartbeats, 3)
	require.Equal(t, OpsJobStatusOK, got.JobHeartbeats[0].Status)
	require.Equal(t, int64(60), *got.JobHeartbeats[0].ExpectedIntervalSeconds)
	require.NotNil(t, got.JobHeartbeats[0].NextExpectedAt)
	require.Equal(t, OpsJobStatusFailing, got.JobHeartbeats[1].Status)
	require.Equal(t, int64(60), *got.JobHeartbeats[1].ExpectedIntervalSeconds)
	require.Equal(t, OpsJobStatusStale, got.JobHeartbeats[2].Status, "failing for longer than the stale window")
	require.Empty(t, repo.heartbeats[0].Status, "repository rows are not mutated")

	want := computeDashboardHealthScore(now, &OpsDashboardOverview{
		RequestCountTotal: 10, RequestCountSLA: 10, SuccessCount: 10, JobHeartbeats: repo.heartbeats,
	})
	require.Equal(t, want, got.HealthScore, "health score is unchanged by the annotation")
}
