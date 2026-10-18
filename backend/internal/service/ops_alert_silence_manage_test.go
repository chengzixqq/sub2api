package service

import (
	"context"
	"database/sql"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func requireOpsReason(t *testing.T, err error, reason string) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, infraerrors.Reason(err))
}

func TestListAlertSilences_DefaultsAndClampsLimit(t *testing.T) {
	var got *OpsAlertSilenceFilter
	repo := &opsRepoMock{ListAlertSilencesFn: func(_ context.Context, f *OpsAlertSilenceFilter) ([]*OpsAlertSilence, error) {
		got = f
		return []*OpsAlertSilence{{ID: 7}}, nil
	}}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	ruleID := int64(41)
	out, err := svc.ListAlertSilences(context.Background(), &OpsAlertSilenceFilter{RuleID: &ruleID, ActiveOnly: true})
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, 100, got.Limit)
	require.True(t, got.ActiveOnly)
	require.False(t, got.Now.IsZero())

	_, err = svc.ListAlertSilences(context.Background(), &OpsAlertSilenceFilter{Limit: 5000})
	require.NoError(t, err)
	require.Equal(t, 500, got.Limit)

	_, err = svc.ListAlertSilences(context.Background(), &OpsAlertSilenceFilter{RuleID: ptrInt64Svc(0)})
	requireOpsReason(t, err, "INVALID_RULE_ID")
}

func TestUpdateAlertSilenceUntil_Validates(t *testing.T) {
	called := false
	repo := &opsRepoMock{UpdateAlertSilenceUntilFn: func(_ context.Context, id int64, until time.Time, reason *string) (*OpsAlertSilence, error) {
		called = true
		return &OpsAlertSilence{ID: id, Until: until}, nil
	}}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	ctx := context.Background()

	_, err := svc.UpdateAlertSilenceUntil(ctx, 0, time.Now().Add(time.Hour), nil)
	requireOpsReason(t, err, "INVALID_SILENCE_ID")
	_, err = svc.UpdateAlertSilenceUntil(ctx, 7, time.Now().Add(-time.Minute), nil)
	requireOpsReason(t, err, "INVALID_UNTIL")
	_, err = svc.UpdateAlertSilenceUntil(ctx, 7, time.Now().Add(31*24*time.Hour), nil)
	requireOpsReason(t, err, "INVALID_UNTIL")
	require.False(t, called, "invalid input must not reach the repository")

	updated, err := svc.UpdateAlertSilenceUntil(ctx, 7, time.Now().Add(time.Hour), nil)
	require.NoError(t, err)
	require.Equal(t, int64(7), updated.ID)
}

func TestUpdateAlertSilenceUntil_NotFound(t *testing.T) {
	repo := &opsRepoMock{UpdateAlertSilenceUntilFn: func(context.Context, int64, time.Time, *string) (*OpsAlertSilence, error) {
		return nil, sql.ErrNoRows
	}}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	_, err := svc.UpdateAlertSilenceUntil(context.Background(), 7, time.Now().Add(time.Hour), nil)
	requireOpsReason(t, err, "OPS_ALERT_SILENCE_NOT_FOUND")
}

func TestEndAlertSilence(t *testing.T) {
	var endedID int64
	repo := &opsRepoMock{EndAlertSilenceFn: func(_ context.Context, id int64, now time.Time) error {
		endedID = id
		require.False(t, now.IsZero())
		if id == 404 {
			return sql.ErrNoRows
		}
		return nil
	}}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	require.NoError(t, svc.EndAlertSilence(context.Background(), 7))
	require.Equal(t, int64(7), endedID)
	requireOpsReason(t, svc.EndAlertSilence(context.Background(), 404), "OPS_ALERT_SILENCE_NOT_FOUND")
	requireOpsReason(t, svc.EndAlertSilence(context.Background(), 0), "INVALID_SILENCE_ID")
}

func ptrInt64Svc(v int64) *int64 { return &v }
