package usagequery

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseRangeMinutePrecision(t *testing.T) {
	r, err := ParseRange(url.Values{"start_time": {"2026-09-08T20:59:00+08:00"}, "end_time": {"2026-09-08T21:00:00+08:00"}, "start_date": {"invalid"}, "timezone": {"Asia/Shanghai"}}, nil, nil)
	require.NoError(t, err)
	require.Equal(t, time.Minute, r.End.Sub(*r.Start))
	require.Equal(t, 20, r.Start.Hour())
}

func TestParseRangeValidation(t *testing.T) {
	for _, q := range []string{
		"start_time=2026-09-08T20:00:00Z", "end_time=2026-09-08T20:00:00Z", "start_time=&end_time=",
		"start_time=2026-09-08T20:00:00&end_time=2026-09-08T21:00:00Z",
		"start_time=2026-09-08T21:00:00Z&end_time=2026-09-08T20:00:00Z",
		"start_time=2026-09-08T20:00:00Z&end_time=2026-09-08T20:00:00Z",
		"start_date=broken", "end_date=broken", "timezone=broken", "start_date=2026-09-09&end_date=2026-09-08",
	} {
		t.Run(q, func(t *testing.T) {
			v, err := url.ParseQuery(q)
			require.NoError(t, err)
			_, err = ParseRange(v, nil, nil)
			require.Error(t, err)
		})
	}
}

func TestParseRangeLegacyDST(t *testing.T) {
	for _, tc := range []struct {
		day   string
		hours time.Duration
	}{{"2026-03-08", 23}, {"2026-11-01", 25}} {
		r, err := ParseRange(url.Values{"start_date": {tc.day}, "end_date": {tc.day}, "timezone": {"America/New_York"}}, nil, nil)
		require.NoError(t, err)
		require.Equal(t, tc.hours*time.Hour, r.End.Sub(*r.Start))
	}
}

func TestParseRangeUnboundedAndDefaults(t *testing.T) {
	r, err := ParseRange(url.Values{}, nil, nil)
	require.NoError(t, err)
	require.Nil(t, r.Start)
	require.Nil(t, r.End)
	end := time.Date(2026, 9, 8, 21, 0, 0, 0, time.UTC)
	start := end.Add(-24 * time.Hour)
	r, err = ParseRange(url.Values{}, &start, &end)
	require.NoError(t, err)
	require.Equal(t, 24*time.Hour, r.End.Sub(*r.Start))
}
