package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Rules scoped only by group, server rules (CPU, memory, queue) and global rules — including every preset rule — carry
// no platform. A silence on them is stored with an empty platform and must still take effect.

func TestCreateAlertSilence_AllowsRuleWithoutPlatform(t *testing.T) {
	var stored *OpsAlertSilence
	repo := &opsRepoMock{CreateAlertSilenceFn: func(_ context.Context, in *OpsAlertSilence) (*OpsAlertSilence, error) {
		stored = in
		return in, nil
	}}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	groupID := int64(2)
	_, err := svc.CreateAlertSilence(context.Background(), &OpsAlertSilence{RuleID: 1, Platform: "  ", GroupID: &groupID, Until: time.Now().Add(time.Hour)})
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, "", stored.Platform)
}

func TestIsAlertSilenced_ChecksRuleWithoutPlatform(t *testing.T) {
	var asked bool
	repo := &opsRepoMock{IsAlertSilencedFn: func(_ context.Context, ruleID int64, platform string, _ *int64, _ *string, _ time.Time) (bool, error) {
		asked = true
		require.Equal(t, int64(1), ruleID)
		require.Equal(t, "", platform)
		return true, nil
	}}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	silenced, err := svc.IsAlertSilenced(context.Background(), 1, "", nil, nil, time.Now())
	require.NoError(t, err)
	require.True(t, asked, "an empty platform must still be looked up")
	require.True(t, silenced)
}

func TestScopedSilenceActive_GlobalRuleIsChecked(t *testing.T) {
	repo := &opsRepoMock{IsAlertSilencedFn: func(_ context.Context, _ int64, platform string, _ *int64, _ *string, _ time.Time) (bool, error) {
		return platform == "", nil
	}}
	evaluator := &OpsAlertEvaluatorService{opsService: NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)}

	require.True(t, evaluator.scopedSilenceActive(context.Background(), 1, "", nil, nil, time.Now()))
	require.False(t, evaluator.scopedSilenceActive(context.Background(), 1, "openai", nil, nil, time.Now()))
}

func TestScopedSilenceActive_NoOpsServiceIsNotSilenced(t *testing.T) {
	require.False(t, (&OpsAlertEvaluatorService{}).scopedSilenceActive(context.Background(), 1, "", nil, nil, time.Now()))
}
