//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type systemMetricsHistoryRepoFake struct {
	OpsRepository
	calls  int
	start  time.Time
	end    time.Time
	bucket int
	points []*OpsSystemMetricsHistoryPoint
	err    error
}

func (f *systemMetricsHistoryRepoFake) ListSystemMetricsHistory(_ context.Context, start, end time.Time, bucketSeconds int) ([]*OpsSystemMetricsHistoryPoint, error) {
	f.calls++
	f.start, f.end, f.bucket = start, end, bucketSeconds
	return f.points, f.err
}

func TestOpsSystemMetricsHistoryBucketSeconds(t *testing.T) {
	cases := []struct {
		window    time.Duration
		maxPoints int
		want      int
	}{
		{time.Hour, 60, 60},
		{5 * time.Minute, 60, 60},
		{2 * time.Hour, 60, 120},
		{6 * time.Hour, 100, 240},
		{6 * time.Hour, 36, 600},
		{24 * time.Hour, 60, 1440},
		{30 * 24 * time.Hour, 60, 43200},
		{time.Hour + time.Second, 60, 120},
		{time.Hour, 1, 3600},
		{0, 60, 60},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, opsSystemMetricsHistoryBucketSeconds(tc.window, tc.maxPoints), "%s/%d", tc.window, tc.maxPoints)
	}

	for _, maxPoints := range []int{1, 7, 60, 100, 1440} {
		for _, window := range []time.Duration{time.Minute, 59 * time.Minute, time.Hour, 7*time.Hour + 13*time.Second, 31*24*time.Hour + time.Hour} {
			bucket := opsSystemMetricsHistoryBucketSeconds(window, maxPoints)
			require.Zero(t, bucket%60, "bucket must be whole minutes")
			require.GreaterOrEqual(t, bucket, 60)
			bucketDur := time.Duration(bucket) * time.Second
			points := int((window + bucketDur - 1) / bucketDur)
			require.LessOrEqual(t, points, maxPoints, "%s/%d -> %ds", window, maxPoints, bucket)
		}
	}
}

func TestParseOpsSystemMetricsMaxPoints(t *testing.T) {
	for raw, want := range map[string]int{"": 60, "1": 1, "120": 120, "1440": 1440} {
		got, err := ParseOpsSystemMetricsMaxPoints(raw)
		require.NoError(t, err, raw)
		require.Equal(t, want, got, raw)
	}
	for _, raw := range []string{"0", "-1", "abc", "1441", "1.5"} {
		_, err := ParseOpsSystemMetricsMaxPoints(raw)
		require.Error(t, err, raw)
		require.Equal(t, "INVALID_MAX_POINTS", infraerrors.Reason(err), raw)
		require.Equal(t, http.StatusBadRequest, infraerrors.Code(err), raw)
	}
}

func TestOpsServiceGetSystemMetricsHistory_Success(t *testing.T) {
	cpu := 38.25
	at := time.Date(2026, 10, 18, 1, 40, 0, 0, time.UTC)
	repo := &systemMetricsHistoryRepoFake{points: []*OpsSystemMetricsHistoryPoint{{CreatedAt: at, SampleCount: 1, CPUUsagePercent: &cpu}}}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	loc := time.FixedZone("UTC+8", 8*3600)
	end := time.Date(2026, 10, 18, 9, 41, 0, 0, loc)
	start := end.Add(-time.Hour)

	got, err := svc.GetSystemMetricsHistory(context.Background(), start, end, 60)
	require.NoError(t, err)
	require.Equal(t, 1, repo.calls)
	require.True(t, repo.start.Equal(start))
	require.True(t, repo.end.Equal(end))
	require.Equal(t, 60, repo.bucket)
	require.Equal(t, 1, got.WindowMinutes)
	require.Equal(t, 60, got.BucketSeconds)
	require.Equal(t, time.UTC, got.StartTime.Location())
	require.Equal(t, time.UTC, got.EndTime.Location())
	require.True(t, got.EndTime.Equal(end))
	require.Len(t, got.Points, 1)
	require.InDelta(t, cpu, *got.Points[0].CPUUsagePercent, 1e-12)

	repo.points = nil
	got, err = svc.GetSystemMetricsHistory(context.Background(), start, end, 30)
	require.NoError(t, err)
	require.Equal(t, 120, repo.bucket)
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"points":[]`)
	require.Contains(t, string(raw), `"window_minutes":1`)
	require.Contains(t, string(raw), `"bucket_seconds":120`)
}

func TestOpsServiceGetSystemMetricsHistory_Errors(t *testing.T) {
	ctx := context.Background()
	end := time.Date(2026, 10, 18, 1, 41, 0, 0, time.UTC)

	plain := NewOpsService(struct{ OpsRepository }{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	_, err := plain.GetSystemMetricsHistory(ctx, end.Add(-time.Hour), end, 60)
	require.Equal(t, "OPS_REPO_UNAVAILABLE", infraerrors.Reason(err))
	require.Equal(t, http.StatusServiceUnavailable, infraerrors.Code(err))

	repo := &systemMetricsHistoryRepoFake{}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	for _, start := range []time.Time{end, end.Add(time.Minute)} {
		_, err = svc.GetSystemMetricsHistory(ctx, start, end, 60)
		require.Equal(t, "OPS_TIME_RANGE_INVALID", infraerrors.Reason(err))
	}

	_, err = svc.GetSystemMetricsHistory(ctx, end.Add(-32*24*time.Hour), end, 60)
	require.Equal(t, "OPS_TIME_RANGE_TOO_LARGE", infraerrors.Reason(err))
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	require.Equal(t, "31", infraerrors.FromError(err).Metadata["max_days"])

	_, err = svc.GetSystemMetricsHistory(ctx, end.Add(-(31*24*time.Hour + time.Hour)), end, 60)
	require.NoError(t, err, "31 days plus the DST hour is accepted")

	_, err = svc.GetSystemMetricsHistory(ctx, end.Add(-time.Hour), end, 0)
	require.Equal(t, "INVALID_MAX_POINTS", infraerrors.Reason(err))

	repo.err = errors.New("db down")
	_, err = svc.GetSystemMetricsHistory(ctx, end.Add(-time.Hour), end, 60)
	require.EqualError(t, err, "db down")
	repo.err = nil

	calls := repo.calls
	svc.runtimeSettings.Store(&opsRuntimeSettingsSnapshot{monitoringEnabled: false})
	_, err = svc.GetSystemMetricsHistory(ctx, end.Add(-time.Hour), end, 60)
	require.ErrorIs(t, err, ErrOpsDisabled)

	cfg := &config.Config{}
	cfg.Ops.Enabled = false
	off := NewOpsService(repo, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	_, err = off.GetSystemMetricsHistory(ctx, end.Add(-time.Hour), end, 60)
	require.Equal(t, "OPS_DISABLED", infraerrors.Reason(err))
	require.Equal(t, http.StatusNotFound, infraerrors.Code(err))
	require.Equal(t, calls, repo.calls, "disabled monitoring must not query")
}

func TestOpsServiceListIngressRejects_FallbackHasEmptyByReason(t *testing.T) {
	svc := NewOpsService(struct{ OpsRepository }{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	got, err := svc.ListIngressRejects(context.Background(), &OpsIngressRejectFilter{})
	require.NoError(t, err)
	require.Zero(t, got.RequestCountTotal)
	require.NotNil(t, got.ByReason)
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"request_count_total":0`)
	require.Contains(t, string(raw), `"by_reason":[]`)
}
