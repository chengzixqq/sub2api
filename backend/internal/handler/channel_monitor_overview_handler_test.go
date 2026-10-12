package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestObservationFilter_Default24HoursAndFiveMinuteBuckets(t *testing.T) {
	h := &ChannelMonitorV2Handler{service: service.NewChannelMonitorV2Service(nil)}
	for _, tc := range []struct {
		query, wantRange string
		bucket           time.Duration
	}{
		{"", "24h", 5 * time.Minute}, {"?range=24h", "24h", 5 * time.Minute},
		{"?range=7d", "7d", time.Hour}, {"?range=30d", "30d", time.Hour},
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/overview"+tc.query, nil)
		f, ok := h.parseObservationFilter(c)
		require.True(t, ok)
		require.Equal(t, tc.wantRange, f.Range)
		require.Equal(t, tc.bucket, f.Bucket)
	}
}

func TestObservationTimelineFilter_ExplicitMinutesAndTargetCount(t *testing.T) {
	h := &ChannelMonitorV2Handler{service: service.NewChannelMonitorV2Service(nil)}
	for _, tc := range []struct {
		query          string
		bucket, source time.Duration
	}{
		{"?range=90m&bucket_minutes=15", 15 * time.Minute, time.Minute},
		{"?range=24h&bucket_minutes=35", 35 * time.Minute, time.Minute},
		{"?range=24h&bucket_count=18", 80 * time.Minute, time.Minute},
		{"?range=24h&bucket_count=20", 72 * time.Minute, time.Minute},
		{"?range=24h&bucket_count=60", 24 * time.Minute, time.Minute},
		{"?range=24h&bucket_minutes=1", time.Minute, time.Minute},
		{"?range=90m&bucket_count=720", time.Minute, time.Minute},
		{"?range=7d&bucket_count=20", 9 * time.Hour, time.Hour},
		{"?range=30d&bucket_minutes=360", 6 * time.Hour, time.Hour},
		{"?range=30d&bucket_count=20", 36 * time.Hour, time.Hour},
		{"?range=30d&bucket_count=1", 30 * 24 * time.Hour, time.Hour},
	} {
		t.Run(tc.query, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/overview"+tc.query, nil)
			f, ok := h.parseObservationFilter(c)
			require.True(t, ok, w.Body.String())
			require.Equal(t, tc.bucket, f.Bucket)
			require.Equal(t, tc.source, f.RollupBucket)
			require.Equal(t, observationWindow(f.Range), f.End.Sub(f.Start))
		})
	}
}

func TestObservationTimelineFilter_RejectsUnsupportedOrAmbiguousQuery(t *testing.T) {
	h := &ChannelMonitorV2Handler{service: service.NewChannelMonitorV2Service(nil)}
	for _, query := range []string{
		"?bucket_minutes=", "?bucket_count=", "?bucket_count=0", "?bucket_count=-1", "?bucket_count=721", "?bucket_count=1.5", "?bucket_count=99999999999999999999",
		"?bucket_minutes=0", "?bucket_minutes=1.5", "?bucket_minutes=60&bucket_count=24", "?bucket_minutes=5&bucket_minutes=10", "?bucket_count=10&bucket_count=20",
		"?range=90m&bucket_minutes=95", "?range=24h&bucket_minutes=1445", "?range=7d&bucket_minutes=30", "?range=30d&bucket_minutes=90", "?range=30d&bucket_minutes=43260",
	} {
		t.Run(query, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/overview"+query, nil)
			_, ok := h.parseObservationFilter(c)
			require.False(t, ok)
			require.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

func TestObservationTimelineFilter_HistoricalShortWindowCannotInventMinutePrecision(t *testing.T) {
	end := time.Now().UTC().Add(-10 * 24 * time.Hour)
	f := service.ChannelMonitorV2Filter{Range: "24h", Start: end.Add(-24 * time.Hour), End: end}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/overview?bucket_minutes=5", nil)
	require.Error(t, applyObservationBucketQuery(c, &f))
	c.Request = httptest.NewRequest(http.MethodGet, "/overview?bucket_count=60", nil)
	require.NoError(t, applyObservationBucketQuery(c, &f))
	require.Equal(t, time.Hour, f.Bucket)
	require.Equal(t, time.Hour, f.RollupBucket)
}

func TestObservationPreview_RequiresOwnerScope(t *testing.T) {
	for _, role := range []string{service.RoleUser, service.RoleVendor, service.RoleAdmin} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/overview?preview=true", nil)
		c.Set(string(middleware.ContextKeyUserRole), role)
		_, ok := observationPreviewContext(c, true)
		require.False(t, ok)
		require.Equal(t, http.StatusForbidden, w.Code)
	}
}
