package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type accountDetailsObservationRepo struct {
	collectorObservationRepo
	facts   []ChannelMonitorAccountObservationFact
	samples []ChannelMonitorObservationSample
}

func (r *accountDetailsObservationRepo) QueryAccounts(context.Context, ChannelMonitorV2Filter) ([]ChannelMonitorAccountObservationFact, error) {
	return r.facts, nil
}

func (r *accountDetailsObservationRepo) QuerySamples(context.Context, ChannelMonitorV2Filter) ([]ChannelMonitorObservationSample, error) {
	return r.samples, nil
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

func TestObservationAccountDetails_UnknownSamplesAreChannelFailuresWithDiagnostics(t *testing.T) {
	now := time.Now().UTC()
	repo := &accountDetailsObservationRepo{
		facts: []ChannelMonitorAccountObservationFact{{
			AccountID: 1, Source: "traffic", GroupID: 7, BucketStart: now, AttemptCount: 2, SuccessAttempts: 1, UnknownAttempts: 1,
		}},
		samples: []ChannelMonitorObservationSample{
			{CompletedAt: now, AccountID: 1, Model: "claude", Outcome: "unknown", ErrorCategory: "incomplete_stream", HTTPStatus: 200},
			{CompletedAt: now, AccountID: 1, Model: "claude", Outcome: "unknown", ErrorCategory: "empty_output", HTTPStatus: 200},
			{Outcome: "success"},
			{Outcome: "client_error", ErrorCategory: "invalid_request", HTTPStatus: 400},
			{Outcome: "cancelled", ErrorCategory: "client_cancelled"},
		},
	}
	svc := NewChannelMonitorOverviewService(repo, nil, nil, nil)
	out, err := svc.AccountDetails(WithScope(context.Background(), AdminScope()), ChannelMonitorV2Filter{AllowedGroupIDs: []int64{7}, RestrictGroups: true})
	require.NoError(t, err)
	require.Len(t, out.Items, 1)
	require.EqualValues(t, 1, out.Items[0].Metrics.UnknownRequests, "account attempt outcomes are separate from completed request outcomes")
	require.Zero(t, out.Items[0].Metrics.ChannelErrors)
	require.Len(t, out.Samples, 5)
	for i, category := range []string{"incomplete_stream", "empty_output"} {
		require.Equal(t, "channel_error", out.Samples[i].Outcome)
		require.Equal(t, category, out.Samples[i].ErrorCategory)
		require.Equal(t, 200, out.Samples[i].HTTPStatus)
		require.Equal(t, now, out.Samples[i].CompletedAt)
		require.Equal(t, "claude", out.Samples[i].Model)
		require.EqualValues(t, 1, out.Samples[i].AccountID)
	}
	require.Equal(t, "success", out.Samples[2].Outcome)
	require.Equal(t, "client_error", out.Samples[3].Outcome)
	require.Equal(t, "cancelled", out.Samples[4].Outcome)
}
