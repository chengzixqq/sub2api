package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newOpsMonitoringStatusRouter(repo *testSettingRepo, configEnabled bool) (*gin.Engine, *service.OpsService) {
	gin.SetMode(gin.TestMode)
	svc := service.NewOpsService(nil, repo, &config.Config{Ops: config.OpsConfig{Enabled: configEnabled}}, nil, nil, nil, nil, nil, nil, nil, nil)
	h := NewOpsHandler(svc)
	r := gin.New()
	r.GET("/monitoring-status", h.GetMonitoringStatus)
	r.PUT("/monitoring-status", h.UpdateMonitoringStatus)
	return r, svc
}

type opsMonitoringStatusEnvelope struct {
	Reason string                      `json:"reason"`
	Data   service.OpsMonitoringStatus `json:"data"`
}

func decodeOpsMonitoringStatus(t *testing.T, rec *httptest.ResponseRecorder) opsMonitoringStatusEnvelope {
	t.Helper()
	var resp opsMonitoringStatusEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), rec.Body.String())
	return resp
}

func TestOpsMonitoringStatusHandlerGetNotGatedByMonitoring(t *testing.T) {
	repo := newTestSettingRepo()
	repo.values[service.SettingKeyOpsMonitoringEnabled] = "false"
	r, svc := newOpsMonitoringStatusRouter(repo, true)
	require.ErrorIs(t, svc.RequireMonitoringEnabled(t.Context()), service.ErrOpsDisabled)

	rec := serveOps(r, http.MethodGet, "/monitoring-status", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := decodeOpsMonitoringStatus(t, rec)
	require.False(t, resp.Data.Effective)
	require.NotNil(t, resp.Data.DisabledReason)
	require.Equal(t, service.OpsMonitoringDisabledSettingOff, *resp.Data.DisabledReason)

	repo.values[service.SettingKeyOpsMonitoringEnabled] = "true"
	rec = serveOps(r, http.MethodGet, "/monitoring-status", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	require.Contains(t, body, `"disabled_reason":null`)
	for _, key := range []string{"config_enabled", "monitoring_enabled", "realtime_monitoring_enabled", "effective"} {
		require.Contains(t, body, `"`+key+`":true`)
	}
}

func TestOpsMonitoringStatusHandlerPutEnables(t *testing.T) {
	repo := newTestSettingRepo()
	repo.values[service.SettingKeyOpsMonitoringEnabled] = "false"
	r, svc := newOpsMonitoringStatusRouter(repo, true)

	rec := serveOps(r, http.MethodPut, "/monitoring-status", map[string]any{"monitoring_enabled": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := decodeOpsMonitoringStatus(t, rec)
	require.True(t, resp.Data.Effective)
	require.Nil(t, resp.Data.DisabledReason)
	require.Equal(t, "true", repo.values[service.SettingKeyOpsMonitoringEnabled])
	require.NoError(t, svc.RequireMonitoringEnabled(t.Context()))
}

func TestOpsMonitoringStatusHandlerPutConfigDisabled(t *testing.T) {
	repo := newTestSettingRepo()
	r, _ := newOpsMonitoringStatusRouter(repo, false)

	rec := serveOps(r, http.MethodPut, "/monitoring-status", map[string]any{"monitoring_enabled": true})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, "OPS_CONFIG_DISABLED", decodeOpsMonitoringStatus(t, rec).Reason)
	_, wrote := repo.values[service.SettingKeyOpsMonitoringEnabled]
	require.False(t, wrote)

	rec = serveOps(r, http.MethodGet, "/monitoring-status", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := decodeOpsMonitoringStatus(t, rec)
	require.False(t, resp.Data.ConfigEnabled)
	require.NotNil(t, resp.Data.DisabledReason)
	require.Equal(t, service.OpsMonitoringDisabledConfigOff, *resp.Data.DisabledReason)
}

func TestOpsMonitoringStatusHandlerPutInvalidBody(t *testing.T) {
	r, _ := newOpsMonitoringStatusRouter(newTestSettingRepo(), true)
	for _, body := range []string{"{not json", ""} {
		req := httptest.NewRequest(http.MethodPut, "/monitoring-status", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code, "body %q: %s", body, rec.Body.String())
	}
}

func TestDiffOpsMonitoringStatusChangedFields(t *testing.T) {
	on := &service.OpsMonitoringStatus{MonitoringEnabled: true, RealtimeMonitoringEnabled: true}
	require.Empty(t, diffOpsMonitoringStatus(on, on))
	require.Equal(t, []string{"ops_monitoring_enabled"},
		diffOpsMonitoringStatus(on, &service.OpsMonitoringStatus{MonitoringEnabled: false, RealtimeMonitoringEnabled: true}))
	require.Equal(t, []string{"ops_monitoring_enabled", "ops_realtime_monitoring_enabled"},
		diffOpsMonitoringStatus(on, &service.OpsMonitoringStatus{}))
	require.Empty(t, diffOpsMonitoringStatus(nil, on))
}
