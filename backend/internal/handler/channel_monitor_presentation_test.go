package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMonitorPresentation_AuthenticatedMetadataOmitsPrivatePolicy(t *testing.T) {
	for _, authenticated := range []bool{false, true} {
		h := &ChannelMonitorV2Handler{overview: service.NewChannelMonitorOverviewService(&observationSettingsRepo{}, nil, nil, nil)}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/overview?presentation_only=true", nil)
		if authenticated {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 42})
		}
		h.ObservationOverview(c)
		if !authenticated {
			require.Equal(t, http.StatusUnauthorized, w.Code)
			continue
		}
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Contains(t, w.Body.String(), `"presentation_defaults"`)
		require.Contains(t, w.Body.String(), `"presentation_defaults_version":`)
		require.Contains(t, w.Body.String(), `"columns":"auto"`)
		require.NotContains(t, w.Body.String(), "probe_global_daily_limit")
		require.NotContains(t, w.Body.String(), "live_group_ids")
	}
}
