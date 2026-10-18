//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestGroupShortageMinRatioSettingParses(t *testing.T) {
	svc := &SettingService{cfg: &config.Config{}}
	require.InDelta(t, 0.5, svc.parseSettings(map[string]string{}).GroupShortageMinRatio, 1e-9)
	require.InDelta(t, 0.3, svc.parseSettings(map[string]string{SettingKeyGroupShortageMinRatio: "0.3"}).GroupShortageMinRatio, 1e-9)
	require.InDelta(t, 0.0, svc.parseSettings(map[string]string{SettingKeyGroupShortageMinRatio: "0"}).GroupShortageMinRatio, 1e-9)
	require.InDelta(t, 0.5, svc.parseSettings(map[string]string{SettingKeyGroupShortageMinRatio: "7"}).GroupShortageMinRatio, 1e-9)
}

func TestGroupShortageMinRatioSettingPersistsAndValidates(t *testing.T) {
	repo := &failureBillingSettingRepo{}
	svc := &SettingService{settingRepo: repo, cfg: &config.Config{}}
	require.NoError(t, svc.UpdateSettings(context.Background(), &SystemSettings{GroupShortageMinRatio: 0.25}))
	require.Equal(t, "0.25", repo.updates[SettingKeyGroupShortageMinRatio])

	err := svc.UpdateSettings(context.Background(), &SystemSettings{GroupShortageMinRatio: 1.5})
	require.Error(t, err)
	require.Equal(t, "INVALID_GROUP_SHORTAGE_MIN_RATIO", infraerrors.Reason(err))
}
