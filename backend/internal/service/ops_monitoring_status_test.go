package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// newOpsMonitoringStatusService loads the runtime snapshot like NewOpsService
// but skips its startup log-config write, so tests can assert on every key.
func newOpsMonitoringStatusService(repo SettingRepository, configEnabled bool) *OpsService {
	svc := &OpsService{settingRepo: repo, cfg: &config.Config{Ops: config.OpsConfig{Enabled: configEnabled}}}
	svc.initRuntimeSettings(context.Background())
	return svc
}

func TestOpsMonitoringStatusDefaultsToEnabledWhenKeysMissing(t *testing.T) {
	svc := newOpsMonitoringStatusService(newRuntimeSettingRepoStub(), true)

	status, err := svc.GetMonitoringStatus(context.Background())
	require.NoError(t, err)
	require.True(t, status.ConfigEnabled)
	require.True(t, status.MonitoringEnabled)
	require.True(t, status.RealtimeMonitoringEnabled)
	require.True(t, status.Effective)
	require.Nil(t, status.DisabledReason)
}

func TestOpsMonitoringStatusReportsSettingOff(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	repo.values[SettingKeyOpsMonitoringEnabled] = "false"
	repo.values[SettingKeyOpsRealtimeMonitoringEnabled] = "true"
	svc := newOpsMonitoringStatusService(repo, true)

	status, err := svc.GetMonitoringStatus(context.Background())
	require.NoError(t, err)
	require.True(t, status.ConfigEnabled)
	require.False(t, status.MonitoringEnabled)
	require.False(t, status.Effective)
	require.NotNil(t, status.DisabledReason)
	require.Equal(t, OpsMonitoringDisabledSettingOff, *status.DisabledReason)
	require.True(t, status.RealtimeMonitoringEnabled, "realtime reports the stored value, not masked by the master switch")
}

func TestOpsMonitoringStatusConfigOffTakesPriority(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	repo.values[SettingKeyOpsMonitoringEnabled] = "false"
	svc := newOpsMonitoringStatusService(repo, false)

	status, err := svc.GetMonitoringStatus(context.Background())
	require.NoError(t, err)
	require.False(t, status.ConfigEnabled)
	require.False(t, status.Effective)
	require.NotNil(t, status.DisabledReason)
	require.Equal(t, OpsMonitoringDisabledConfigOff, *status.DisabledReason)
}

func TestOpsMonitoringStatusLoadFailureReturnsError(t *testing.T) {
	repo := &opsRuntimeRefreshRepo{values: map[string]string{}}
	repo.fail.Store(true)
	svc := &OpsService{settingRepo: repo, cfg: &config.Config{Ops: config.OpsConfig{Enabled: true}}}

	status, err := svc.GetMonitoringStatus(context.Background())
	require.Error(t, err)
	require.Nil(t, status)
}

func TestUpdateOpsMonitoringStatusWritesOnlyProvidedKeys(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	repo.values[SettingKeyOpsMonitoringEnabled] = "false"
	repo.values[SettingKeyOpsQueryModeDefault] = "raw"
	svc := newOpsMonitoringStatusService(repo, true)

	before, after, err := svc.UpdateMonitoringStatus(context.Background(), OpsMonitoringStatusUpdate{MonitoringEnabled: boolPtr(true)})
	require.NoError(t, err)
	require.False(t, before.MonitoringEnabled)
	require.True(t, after.MonitoringEnabled)
	require.True(t, after.Effective)
	require.Nil(t, after.DisabledReason)
	require.Equal(t, "true", repo.values[SettingKeyOpsMonitoringEnabled])
	_, wroteRealtime := repo.values[SettingKeyOpsRealtimeMonitoringEnabled]
	require.False(t, wroteRealtime)
	require.Equal(t, "raw", repo.values[SettingKeyOpsQueryModeDefault])
}

func TestUpdateOpsMonitoringStatusPublishesSnapshotImmediately(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	repo.values[SettingKeyOpsMonitoringEnabled] = "false"
	svc := newOpsMonitoringStatusService(repo, true)
	ctx := context.Background()
	require.ErrorIs(t, svc.RequireMonitoringEnabled(ctx), ErrOpsDisabled)

	_, _, err := svc.UpdateMonitoringStatus(ctx, OpsMonitoringStatusUpdate{MonitoringEnabled: boolPtr(true)})
	require.NoError(t, err)
	require.NoError(t, svc.RequireMonitoringEnabled(ctx))

	_, _, err = svc.UpdateMonitoringStatus(ctx, OpsMonitoringStatusUpdate{MonitoringEnabled: boolPtr(false)})
	require.NoError(t, err)
	require.ErrorIs(t, svc.RequireMonitoringEnabled(ctx), ErrOpsDisabled)
}

func TestUpdateOpsMonitoringStatusRealtimeOnly(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := newOpsMonitoringStatusService(repo, true)
	ctx := context.Background()

	_, after, err := svc.UpdateMonitoringStatus(ctx, OpsMonitoringStatusUpdate{RealtimeMonitoringEnabled: boolPtr(false)})
	require.NoError(t, err)
	require.Equal(t, "false", repo.values[SettingKeyOpsRealtimeMonitoringEnabled])
	_, wroteMonitoring := repo.values[SettingKeyOpsMonitoringEnabled]
	require.False(t, wroteMonitoring)
	require.False(t, after.RealtimeMonitoringEnabled)
	require.False(t, svc.IsRealtimeMonitoringEnabled(ctx))
	require.True(t, svc.IsMonitoringEnabled(ctx))
}

func TestUpdateOpsMonitoringStatusRejectsWhenConfigDisabled(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := newOpsMonitoringStatusService(repo, false)

	_, _, err := svc.UpdateMonitoringStatus(context.Background(), OpsMonitoringStatusUpdate{MonitoringEnabled: boolPtr(true)})
	require.True(t, errors.Is(err, ErrOpsConfigDisabled))
	require.Equal(t, http.StatusConflict, infraerrors.Code(err))
	require.Equal(t, "OPS_CONFIG_DISABLED", infraerrors.Reason(err))
	require.Empty(t, repo.values, "config-disabled updates must not touch the settings table")
}

func TestUpdateOpsMonitoringStatusEmptyUpdateIsNoop(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	repo.values[SettingKeyOpsMonitoringEnabled] = "false"
	svc := newOpsMonitoringStatusService(repo, true)

	before, after, err := svc.UpdateMonitoringStatus(context.Background(), OpsMonitoringStatusUpdate{})
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, map[string]string{SettingKeyOpsMonitoringEnabled: "false"}, repo.values)
}

func TestOpsMonitoringDisabledReasonFromSnapshot(t *testing.T) {
	ctx := context.Background()
	require.Equal(t, OpsMonitoringDisabledConfigOff, newOpsMonitoringStatusService(nil, false).MonitoringDisabledReason(ctx))

	svc := newOpsMonitoringStatusService(nil, true)
	require.Equal(t, "", svc.MonitoringDisabledReason(ctx))
	svc.SetMonitoringEnabled(false)
	require.Equal(t, OpsMonitoringDisabledSettingOff, svc.MonitoringDisabledReason(ctx))
	svc.SetMonitoringEnabled(true)
	require.Equal(t, "", svc.MonitoringDisabledReason(ctx))
}
