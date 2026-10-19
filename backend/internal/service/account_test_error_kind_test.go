package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func intPtrForErrorKindTest(v int) *int { return &v }

func TestAccountTestErrorKind(t *testing.T) {
	cases := []struct {
		name     string
		status   string
		code     *int
		message  string
		wantKind *string
	}{
		{"401 maps to auth", "failed", intPtrForErrorKindTest(401), "API returned 401: x", strPtrForErrorKindTest(AccountErrorKindAuth)},
		{"403 maps to forbidden", "failed", intPtrForErrorKindTest(403), "API returned 403: x", strPtrForErrorKindTest(AccountErrorKindForbidden)},
		{"402 maps to quota", "failed", intPtrForErrorKindTest(402), "API returned 402: x", strPtrForErrorKindTest(AccountErrorKindQuota)},
		{"429 maps to rate_limit", "failed", intPtrForErrorKindTest(429), "API returned 429: slow down", strPtrForErrorKindTest(AccountErrorKindRateLimit)},
		{"500 stays other", "failed", intPtrForErrorKindTest(500), "API returned 500: boom", strPtrForErrorKindTest(AccountErrorKindOther)},
		{"structured status wins over text", "failed", intPtrForErrorKindTest(429), "API returned 500: boom", strPtrForErrorKindTest(AccountErrorKindRateLimit)},
		{"network error without status", "failed", nil, "Request failed: dial tcp 1.2.3.4:443", strPtrForErrorKindTest(AccountErrorKindOther)},
		{"legacy row falls back to text", "failed", nil, "API returned 401: invalid key", strPtrForErrorKindTest(AccountErrorKindAuth)},
		{"legacy row with HTTP form", "failed", nil, "Grok Realtime WS handshake failed (HTTP 429): x", strPtrForErrorKindTest(AccountErrorKindRateLimit)},
		{"prefix classification wins", "failed", intPtrForErrorKindTest(400), "Token refresh failed (non-retryable): invalid_grant", strPtrForErrorKindTest(AccountErrorKindTokenRefresh)},
		{"empty message still classified", "failed", nil, "", strPtrForErrorKindTest(AccountErrorKindOther)},
		{"success has no kind", "success", intPtrForErrorKindTest(200), "", nil},
		{"success ignores stale text", "success", nil, "API returned 401", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.wantKind, AccountTestErrorKind(tc.status, tc.code, tc.message))
		})
	}
}

func strPtrForErrorKindTest(v string) *string { return &v }

func TestRunTestBackgroundRecordsUpstreamStatus(t *testing.T) {
	t.Run("rate limited", func(t *testing.T) {
		svc, _, _, _, _ := newTypeSafeAccountTestFixture(t, http.StatusTooManyRequests, `{"detail":"slow down"}`)
		result, err := svc.RunTestBackground(context.Background(), 31, "")
		require.NoError(t, err)
		require.Equal(t, "failed", result.Status)
		require.Contains(t, result.ErrorMessage, "API returned 429")
		require.Equal(t, intPtrForErrorKindTest(429), result.UpstreamStatusCode)
		require.Equal(t, strPtrForErrorKindTest(AccountErrorKindRateLimit), result.ErrorKind)
	})

	t.Run("success keeps the recorded status and no kind", func(t *testing.T) {
		svc, _, _, _, _ := newTypeSafeAccountTestFixture(t, http.StatusOK, `{"model":"jev-1.13.0","answers":{}}`)
		result, err := svc.RunTestBackground(context.Background(), 31, "")
		require.NoError(t, err)
		require.Equal(t, "success", result.Status)
		require.Equal(t, intPtrForErrorKindTest(200), result.UpstreamStatusCode)
		require.Nil(t, result.ErrorKind)
	})

	t.Run("no upstream response", func(t *testing.T) {
		svc := &AccountTestService{accountRepo: missingAccountErrorKindRepo{}}
		result, err := svc.RunTestBackground(context.Background(), 31, "")
		require.NoError(t, err)
		require.Equal(t, "failed", result.Status)
		require.Nil(t, result.UpstreamStatusCode)
		require.Equal(t, strPtrForErrorKindTest(AccountErrorKindOther), result.ErrorKind)
	})
}

type missingAccountErrorKindRepo struct{ AccountRepository }

func (missingAccountErrorKindRepo) GetByID(context.Context, int64) (*Account, error) {
	return nil, errors.New("missing")
}

type scheduledTestResultRepoStub struct {
	ScheduledTestResultRepository
	results []*ScheduledTestResult
}

func (s *scheduledTestResultRepoStub) ListByPlanID(context.Context, int64, int) ([]*ScheduledTestResult, error) {
	return s.results, nil
}

func TestScheduledTestServiceListResultsDerivesErrorKind(t *testing.T) {
	repo := &scheduledTestResultRepoStub{results: []*ScheduledTestResult{
		{ID: 1, Status: "failed", UpstreamStatusCode: intPtrForErrorKindTest(429), ErrorMessage: "API returned 429: slow down"},
		{ID: 2, Status: "failed", ErrorMessage: "API returned 401: invalid key"},
		{ID: 3, Status: "success", UpstreamStatusCode: intPtrForErrorKindTest(200)},
	}}
	svc := NewScheduledTestService(nil, repo)

	results, err := svc.ListResults(context.Background(), 7, 10)
	require.NoError(t, err)
	require.Len(t, results, 3)

	require.Equal(t, strPtrForErrorKindTest(AccountErrorKindRateLimit), results[0].ErrorKind)
	require.Equal(t, intPtrForErrorKindTest(429), results[0].UpstreamStatusCode)

	require.Equal(t, strPtrForErrorKindTest(AccountErrorKindAuth), results[1].ErrorKind)
	require.Nil(t, results[1].UpstreamStatusCode, "the parsed status is only used for classification")

	require.Nil(t, results[2].ErrorKind)
	require.Equal(t, intPtrForErrorKindTest(200), results[2].UpstreamStatusCode)
}

func TestScheduledTestResultJSONNullableUpstreamFields(t *testing.T) {
	raw, err := json.Marshal(&ScheduledTestResult{ID: 1, PlanID: 2, Status: "success"})
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))

	for _, key := range []string{"id", "plan_id", "status", "response_text", "error_message", "latency_ms", "started_at", "finished_at", "created_at"} {
		require.Contains(t, decoded, key)
	}
	require.Contains(t, decoded, "upstream_status_code")
	require.Nil(t, decoded["upstream_status_code"])
	require.Contains(t, decoded, "error_kind")
	require.Nil(t, decoded["error_kind"])

	raw, err = json.Marshal(&ScheduledTestResult{Status: "failed", UpstreamStatusCode: intPtrForErrorKindTest(429), ErrorKind: strPtrForErrorKindTest(AccountErrorKindRateLimit)})
	require.NoError(t, err)
	require.Contains(t, string(raw), `"upstream_status_code":429`)
	require.Contains(t, string(raw), `"error_kind":"rate_limit"`)
}
