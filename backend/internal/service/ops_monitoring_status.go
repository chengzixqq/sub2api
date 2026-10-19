package service

import (
	"context"
	"fmt"
	"strconv"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Reasons reported by GET /api/v1/admin/ops/monitoring-status when ops
// monitoring is off. config_off means the server config (ops.enabled=false)
// disables it and no API call can turn it back on; setting_off means the
// admin setting is off and PUT monitoring-status can enable it.
const (
	OpsMonitoringDisabledConfigOff  = "config_off"
	OpsMonitoringDisabledSettingOff = "setting_off"
)

var ErrOpsConfigDisabled = infraerrors.Conflict("OPS_CONFIG_DISABLED", "Ops monitoring is disabled in the server config (ops.enabled=false)")

// OpsMonitoringStatus separates the config hard switch from the admin soft
// switches. MonitoringEnabled and RealtimeMonitoringEnabled are the stored
// setting values (missing keys count as true); Effective is what the ops
// endpoints enforce.
type OpsMonitoringStatus struct {
	ConfigEnabled             bool    `json:"config_enabled"`
	MonitoringEnabled         bool    `json:"monitoring_enabled"`
	RealtimeMonitoringEnabled bool    `json:"realtime_monitoring_enabled"`
	Effective                 bool    `json:"effective"`
	DisabledReason            *string `json:"disabled_reason"`
}

// OpsMonitoringStatusUpdate is the PUT body; nil fields keep their value.
type OpsMonitoringStatusUpdate struct {
	MonitoringEnabled         *bool `json:"monitoring_enabled"`
	RealtimeMonitoringEnabled *bool `json:"realtime_monitoring_enabled"`
}

func (s *OpsService) isOpsConfigEnabled() bool {
	return s.cfg == nil || s.cfg.Ops.Enabled
}

// GetMonitoringStatus reads the persisted switches (not the in-memory
// snapshot), so it reflects writes made on other instances right away.
func (s *OpsService) GetMonitoringStatus(ctx context.Context) (*OpsMonitoringStatus, error) {
	monitoring, realtime := true, true
	if s.settingRepo == nil {
		if snapshot := s.runtimeSettings.Load(); snapshot != nil {
			monitoring = snapshot.monitoringEnabled
		}
	} else {
		values, err := s.settingRepo.GetMultiple(ctx, []string{
			SettingKeyOpsMonitoringEnabled,
			SettingKeyOpsRealtimeMonitoringEnabled,
		})
		if err != nil {
			return nil, fmt.Errorf("load ops monitoring status: %w", err)
		}
		if raw, ok := values[SettingKeyOpsMonitoringEnabled]; ok {
			monitoring = parseOpsMonitoringEnabled(raw)
		}
		if raw, ok := values[SettingKeyOpsRealtimeMonitoringEnabled]; ok {
			realtime = parseOpsMonitoringEnabled(raw)
		}
	}
	return buildOpsMonitoringStatus(s.isOpsConfigEnabled(), monitoring, realtime), nil
}

func buildOpsMonitoringStatus(configEnabled, monitoring, realtime bool) *OpsMonitoringStatus {
	status := &OpsMonitoringStatus{
		ConfigEnabled:             configEnabled,
		MonitoringEnabled:         monitoring,
		RealtimeMonitoringEnabled: realtime,
		Effective:                 configEnabled && monitoring,
	}
	if reason := opsMonitoringDisabledReason(configEnabled, monitoring); reason != "" {
		status.DisabledReason = &reason
	}
	return status
}

func opsMonitoringDisabledReason(configEnabled, monitoring bool) string {
	switch {
	case !configEnabled:
		return OpsMonitoringDisabledConfigOff
	case !monitoring:
		return OpsMonitoringDisabledSettingOff
	default:
		return ""
	}
}

// UpdateMonitoringStatus writes only the provided switches and publishes the
// master switch to this instance's snapshot immediately; other instances
// converge on their next runtime settings refresh. It refuses every write
// while the config hard switch is off, because the setting would have no
// effect until the config changes.
func (s *OpsService) UpdateMonitoringStatus(ctx context.Context, update OpsMonitoringStatusUpdate) (before, after *OpsMonitoringStatus, err error) {
	if !s.isOpsConfigEnabled() {
		return nil, nil, ErrOpsConfigDisabled
	}
	before, err = s.GetMonitoringStatus(ctx)
	if err != nil {
		return nil, nil, err
	}
	updates := make(map[string]string, 2)
	if update.MonitoringEnabled != nil {
		updates[SettingKeyOpsMonitoringEnabled] = strconv.FormatBool(*update.MonitoringEnabled)
	}
	if update.RealtimeMonitoringEnabled != nil {
		updates[SettingKeyOpsRealtimeMonitoringEnabled] = strconv.FormatBool(*update.RealtimeMonitoringEnabled)
	}
	if len(updates) == 0 {
		return before, before, nil
	}
	if s.settingRepo == nil {
		return nil, nil, fmt.Errorf("update ops monitoring status: setting repository not available")
	}
	if err := s.settingRepo.SetMultiple(ctx, updates); err != nil {
		return nil, nil, fmt.Errorf("update ops monitoring status: %w", err)
	}
	if update.MonitoringEnabled != nil {
		s.SetMonitoringEnabled(*update.MonitoringEnabled)
	}
	after, err = s.GetMonitoringStatus(ctx)
	if err != nil {
		return nil, nil, err
	}
	return before, after, nil
}

// MonitoringDisabledReason reports why ops monitoring is off on this instance
// from the config and the in-memory snapshot, without a database read; it
// returns "" while monitoring is on.
func (s *OpsService) MonitoringDisabledReason(ctx context.Context) string {
	if !s.isOpsConfigEnabled() {
		return OpsMonitoringDisabledConfigOff
	}
	if s.IsMonitoringEnabled(ctx) {
		return ""
	}
	return OpsMonitoringDisabledSettingOff
}
