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
