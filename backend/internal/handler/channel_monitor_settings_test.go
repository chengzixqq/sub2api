package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type observationSettingsRepo struct {
	service.ChannelMonitorObservationRepository
	saved     *service.ChannelMonitorObservationConfig
	err       error
	readError error
}

func (r *observationSettingsRepo) GetConfig(context.Context) (*service.ChannelMonitorObservationConfig, error) {
	cfg := service.DefaultChannelMonitorObservationConfig()
	cfg.Version = 4
	cfg.ProbeTargetDailyLimit = 12
	cfg.ProbeIntervalSeconds = 120
	return &cfg, r.readError
}

func (r *observationSettingsRepo) UpdateConfig(_ context.Context, cfg service.ChannelMonitorObservationConfig, _ int) (*service.ChannelMonitorObservationConfig, error) {
	r.saved = &cfg
	return &cfg, r.err
}

func TestObservationSettings_CompatibleDefaultsExplicitZeroAndConflict(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		conflict   bool
		want       int
	}{
		{"old client", `{"version":4,"enabled":true,"mode":"shadow"}`, false, http.StatusOK},
		{"runtime fields", `{"version":4,"refresh_interval_seconds":180,"probe_interval_seconds":120,"probe_timeout_seconds":45,"probe_target_daily_limit":7,"probe_global_daily_limit":20,"traffic_freshness_seconds":600,"probe_failure_threshold":4,"quota_stale_seconds":1200}`, false, http.StatusOK},
		{"explicit zero", `{"version":4,"probe_target_daily_limit":0}`, false, http.StatusBadRequest},
		{"non integer", `{"version":4,"refresh_interval_seconds":90.5}`, false, http.StatusBadRequest},
		{"version missing", `{}`, false, http.StatusBadRequest},
		{"read failure", `{"version":4}`, false, http.StatusInternalServerError},
		{"conflict", `{"version":3}`, true, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &observationSettingsRepo{}
			if tc.name == "read failure" {
				repo.readError = errors.New("database unavailable")
			}
			if tc.conflict {
				repo.err = service.ErrChannelMonitorV2ConfigConflict
			}
			h := &ChannelMonitorV2Handler{overview: service.NewChannelMonitorOverviewService(repo, nil, nil, nil)}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPut, "/observation-config", strings.NewReader(tc.body)).WithContext(service.WithScope(context.Background(), service.AdminScope()))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set(string(middleware.ContextKeyUserRole), "admin")
			h.UpdateObservationConfig(c)
			require.Equal(t, tc.want, w.Code, w.Body.String())
			if tc.want == http.StatusBadRequest || tc.want == http.StatusInternalServerError {
				require.Nil(t, repo.saved)
			}
			if tc.name == "old client" {
				require.Equal(t, 12, repo.saved.ProbeTargetDailyLimit)
				require.Equal(t, 120, repo.saved.ProbeIntervalSeconds)
				require.False(t, repo.saved.ProbeEnabled)
			}
			if tc.name == "runtime fields" {
				require.Equal(t, 180, repo.saved.RefreshIntervalSeconds)
				require.Equal(t, 45, repo.saved.ProbeTimeoutSeconds)
				require.Equal(t, 4, repo.saved.ProbeFailureThreshold)
				require.Equal(t, 1200, repo.saved.QuotaStaleSeconds)
			}
		})
	}
}
