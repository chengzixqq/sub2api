package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type accountDetailsObservationRepo struct {
	collectorObservationRepo
	facts []ChannelMonitorAccountObservationFact
}

func (r *accountDetailsObservationRepo) QueryAccounts(context.Context, ChannelMonitorV2Filter) ([]ChannelMonitorAccountObservationFact, error) {
	return r.facts, nil
}

func TestObservationAccountDetails_LegacyFailedAttemptsAreNotChannelErrors(t *testing.T) {
	svc := NewChannelMonitorOverviewService(&accountDetailsObservationRepo{facts: []ChannelMonitorAccountObservationFact{{
		AccountID: 1, Source: "traffic", GroupID: 7, BucketStart: time.Now().UTC(), AttemptCount: 3, FailedAttempts: 3,
	}}}, nil, nil, nil)
	out, err := svc.AccountDetails(WithScope(context.Background(), AdminScope()), ChannelMonitorV2Filter{AllowedGroupIDs: []int64{7}, RestrictGroups: true})
	require.NoError(t, err)
	require.Len(t, out.Items, 1)
	require.Zero(t, out.Items[0].Metrics.ChannelErrors)
	require.EqualValues(t, 3, out.Items[0].Metrics.UnclassifiedAttempts)
}
