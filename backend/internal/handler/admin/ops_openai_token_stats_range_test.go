package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newOpsTokenStatsContext(rawURL string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, rawURL, nil)
	return c
}

func TestParseOpsOpenAITokenStatsFilter_CustomRange(t *testing.T) {
	wantStart := time.Date(2026, 10, 10, 16, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 10, 17, 16, 0, 0, 0, time.UTC)
	const rangeQuery = "start_time=2026-10-11T00:00:00%2B08:00&end_time=2026-10-18T00:00:00%2B08:00"

	t.Run("top_n 模式", func(t *testing.T) {
		filter, err := parseOpsOpenAITokenStatsFilter(newOpsTokenStatsContext("/?" + rangeQuery + "&top_n=10&platform=openai&group_id=3&model=gpt-5"))
		require.NoError(t, err)
		require.Equal(t, "custom", filter.TimeRange)
		require.True(t, filter.StartTime.Equal(wantStart), "start=%s", filter.StartTime)
		require.True(t, filter.EndTime.Equal(wantEnd), "end=%s", filter.EndTime)
		require.Equal(t, 10, filter.TopN)
		require.Equal(t, 0, filter.Page)
		require.Equal(t, 0, filter.PageSize)
		require.Equal(t, "openai", filter.Platform)
		require.NotNil(t, filter.GroupID)
		require.Equal(t, int64(3), *filter.GroupID)
		require.Equal(t, "gpt-5", filter.Model)
	})

	t.Run("分页模式", func(t *testing.T) {
		filter, err := parseOpsOpenAITokenStatsFilter(newOpsTokenStatsContext("/?" + rangeQuery))
		require.NoError(t, err)
		require.Equal(t, "custom", filter.TimeRange)
		require.Equal(t, 1, filter.Page)
		require.Equal(t, 20, filter.PageSize)
	})

	for _, timeRange := range []string{"1h", "7d"} {
		t.Run("优先于 time_range="+timeRange, func(t *testing.T) {
			filter, err := parseOpsOpenAITokenStatsFilter(newOpsTokenStatsContext("/?time_range=" + timeRange + "&" + rangeQuery))
			require.NoError(t, err)
			require.Equal(t, "custom", filter.TimeRange)
			require.True(t, filter.StartTime.Equal(wantStart))
			require.True(t, filter.EndTime.Equal(wantEnd))
		})
	}
}

func TestParseOpsOpenAITokenStatsFilter_CustomRangeInvalid(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"/?start_time=2026-10-11T00:00:00Z", "must be provided together"},
		{"/?end_time=2026-10-18T00:00:00Z", "must be provided together"},
		{"/?start_time=bad&end_time=2026-10-18T00:00:00Z", "invalid start_time format"},
		{"/?start_time=2026-10-11T00:00:00Z&end_time=bad", "invalid end_time format"},
		{"/?start_time=2026-10-11T00:00:00Z&end_time=2026-10-11T00:00:00Z", "start time must be before end time"},
		{"/?start_time=2026-10-18T00:00:00Z&end_time=2026-10-11T00:00:00Z", "start time must be before end time"},
		{"/?timezone=Bad/Zone&start_time=2026-10-11T00:00:00Z&end_time=2026-10-18T00:00:00Z", "invalid timezone"},
		{"/?start_time=&end_time=", "invalid start_time format"},
	}
	for _, tc := range cases {
		_, err := parseOpsOpenAITokenStatsFilter(newOpsTokenStatsContext(tc.url))
		require.Error(t, err, "url=%s", tc.url)
		require.Contains(t, err.Error(), tc.want, "url=%s", tc.url)
	}
}

func TestParseOpsOpenAITokenStatsFilter_LegacyIgnoresRangeOnlyParams(t *testing.T) {
	for _, rawURL := range []string{
		"/?timezone=Bad/Zone",
		"/?start_date=2026-10-01&end_date=2026-10-02",
	} {
		before := time.Now().UTC()
		filter, err := parseOpsOpenAITokenStatsFilter(newOpsTokenStatsContext(rawURL))
		require.NoError(t, err, "url=%s", rawURL)
		require.Equal(t, "30d", filter.TimeRange)
		require.WithinDuration(t, before.Add(-30*24*time.Hour), filter.StartTime, 2*time.Second)
		require.WithinDuration(t, before, filter.EndTime, 2*time.Second)
	}
}
