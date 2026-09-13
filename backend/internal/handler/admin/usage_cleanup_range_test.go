package admin

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseUsageCleanupRangeExact(t *testing.T) {
	start, end := "2026-09-08T20:59:00+08:00", "2026-09-08T21:00:00+08:00"
	from, to, exclusive, err := parseUsageCleanupRange(CreateUsageCleanupTaskRequest{
		StartTime: &start, EndTime: &end, Timezone: "Asia/Shanghai",
		StartDate: "2020-01-01", EndDate: "2030-01-01",
	})
	require.NoError(t, err)
	require.True(t, exclusive)
	require.Equal(t, time.Minute, to.Sub(from))
	require.Equal(t, "2026-09-08T13:00:00Z", to.UTC().Format(time.RFC3339))
}

func TestParseUsageCleanupRangeRejectsInvalidExact(t *testing.T) {
	for _, pair := range [][2]string{{"", "2026-09-08T21:00:00+08:00"}, {"2026-09-08T21:00:00", "2026-09-08T22:00:00Z"}, {"2026-09-08T21:00:00Z", "2026-09-08T21:00:00Z"}, {"2026-09-08T21:00:00Z", "2026-09-08T20:00:00Z"}} {
		t.Run(pair[0]+"/"+pair[1], func(t *testing.T) {
			_, _, _, err := parseUsageCleanupRange(CreateUsageCleanupTaskRequest{StartTime: &pair[0], EndTime: &pair[1], StartDate: "2026-09-01", EndDate: "2026-09-08"})
			require.Error(t, err)
		})
	}
	start := "2026-09-08T21:00:00Z"
	_, _, _, err := parseUsageCleanupRange(CreateUsageCleanupTaskRequest{StartTime: &start, StartDate: "2026-09-01", EndDate: "2026-09-08"})
	require.Error(t, err)
}

func TestParseUsageCleanupRangeLegacyDST(t *testing.T) {
	for _, tc := range []struct {
		date  string
		hours time.Duration
	}{{"2026-03-08", 23}, {"2026-11-01", 25}} {
		from, to, exclusive, err := parseUsageCleanupRange(CreateUsageCleanupTaskRequest{StartDate: tc.date, EndDate: tc.date, Timezone: "America/New_York"})
		require.NoError(t, err)
		require.False(t, exclusive)
		require.Equal(t, tc.hours*time.Hour-time.Nanosecond, to.Sub(from))
		require.Equal(t, tc.date, to.Format("2006-01-02"))
	}
}
