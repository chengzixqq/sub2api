package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type failoverCooldownRepo struct {
	AccountRepository
	reasons []string
	until   time.Time
}

func (r *failoverCooldownRepo) SetTempUnschedulable(_ context.Context, _ int64, until time.Time, reason string) error {
	r.reasons = append(r.reasons, reason)
	r.until = until
	return nil
}

func TestGatewayService_FailoverCooldownRequiresExplicitCause(t *testing.T) {
	tests := []struct {
		name    string
		failure UpstreamFailoverError
		pause   bool
		reason  string
	}{
		{name: "ordinary_502", failure: UpstreamFailoverError{StatusCode: 502, RetryableOnSameAccount: true}},
		{name: "ordinary_502_with_empty_text", failure: UpstreamFailoverError{StatusCode: 502, RetryableOnSameAccount: true, ResponseBody: []byte(`{"error":"empty stream response from upstream"}`)}},
		{name: "confirmed_empty_response", failure: UpstreamFailoverError{StatusCode: 502, RetryableOnSameAccount: true, Reason: GatewayFailureReason("empty_response")}, pause: true, reason: "empty stream response"},
		{name: "pre_output_read_failure", failure: UpstreamFailoverError{StatusCode: 502, RetryableOnSameAccount: true, Reason: GatewayFailureReason("stream_read_error")}, pause: true, reason: "stream read failure"},
		{name: "request_scoped", failure: UpstreamFailoverError{StatusCode: 502, RetryableOnSameAccount: true, RequestScopedTransient: true, Reason: GatewayFailureReason("empty_response")}},
		{name: "payload_too_large", failure: UpstreamFailoverError{StatusCode: http.StatusRequestEntityTooLarge, RetryableOnSameAccount: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &failoverCooldownRepo{}
			before := time.Now()
			(&GatewayService{accountRepo: repo}).TempUnscheduleRetryableError(context.Background(), 66, &tt.failure)
			if !tt.pause {
				require.Empty(t, repo.reasons)
				return
			}
			require.Len(t, repo.reasons, 1)
			require.Contains(t, repo.reasons[0], tt.reason)
			require.WithinDuration(t, before.Add(time.Minute), repo.until, time.Second)
			if tt.name == "pre_output_read_failure" {
				require.NotContains(t, repo.reasons[0], "empty stream")
			}
		})
	}
}
