//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type requestAttemptsRepoFake struct {
	OpsRepository
	errRows      []*OpsRequestAttemptErrorRow
	usageRows    []*OpsRequestAttemptUsageRow
	names        map[int64]string
	namesErr     error
	errLookups   []OpsRequestAttemptLookup
	usageLookups []OpsRequestAttemptLookup
	nameCalls    [][]int64
}

func (f *requestAttemptsRepoFake) ListRequestAttemptErrorLogs(_ context.Context, lookup OpsRequestAttemptLookup) ([]*OpsRequestAttemptErrorRow, error) {
	f.errLookups = append(f.errLookups, lookup)
	return f.errRows, nil
}

func (f *requestAttemptsRepoFake) ListRequestAttemptUsageLogs(_ context.Context, lookup OpsRequestAttemptLookup) ([]*OpsRequestAttemptUsageRow, error) {
	f.usageLookups = append(f.usageLookups, lookup)
	return f.usageRows, nil
}

func (f *requestAttemptsRepoFake) ListRequestAttemptAccountNames(_ context.Context, ids []int64) (map[int64]string, error) {
	f.nameCalls = append(f.nameCalls, append([]int64(nil), ids...))
	return f.names, f.namesErr
}

func attemptEventsJSON(t *testing.T, events ...OpsUpstreamErrorEvent) string {
	t.Helper()
	ptrs := make([]*OpsUpstreamErrorEvent, 0, len(events))
	for i := range events {
		ptrs = append(ptrs, &events[i])
	}
	raw, err := json.Marshal(ptrs)
	require.NoError(t, err)
	return string(raw)
}

func TestBuildOpsRequestAttempts_FailoverThenSuccess(t *testing.T) {
	usageAt := time.Date(2026, 10, 18, 1, 41, 10, 0, time.UTC)
	errRows := []*OpsRequestAttemptErrorRow{{
		ID: 8812, CreatedAt: usageAt.Add(time.Second), RequestID: "req-1", ClientRequestID: "cid-1", StatusCode: 200,
		UpstreamErrorsJSON: attemptEventsJSON(t, OpsUpstreamErrorEvent{
			AtUnixMs: 1400, StartedAtUnixMs: 1000, DurationMs: attemptInt64(400), Platform: "anthropic",
			AccountID: 3, AccountName: "Claude-03", UpstreamStatusCode: 429, Kind: "failover", Message: "rate limited",
		}),
	}}
	usageRows := []*OpsRequestAttemptUsageRow{{
		ID: 9018, CreatedAt: usageAt, RequestID: "client:cid-1", AccountID: 1, AccountName: "Claude-01",
		Platform: "anthropic", DurationMs: attemptInt64(9800), FirstTokenMs: attemptInt64(1400), UpstreamRequestID: "msg_up_1",
	}}

	got := buildOpsRequestAttempts(errRows, usageRows)

	require.Len(t, got.Attempts, 2)
	first, second := got.Attempts[0], got.Attempts[1]
	require.Equal(t, 1, first.Index)
	require.Equal(t, opsRequestAttemptSourceUpstreamError, first.Source)
	require.Equal(t, opsRequestAttemptOutcomeFailed, first.Outcome)
	require.Equal(t, int64(3), *first.AccountID)
	require.Equal(t, "Claude-03", first.AccountName)
	require.Equal(t, 429, *first.UpstreamStatusCode)
	require.Equal(t, "failover", first.Kind)
	require.Equal(t, int64(400), *first.DurationMs)
	require.True(t, first.StartedAt.Equal(time.UnixMilli(1000)))
	require.Equal(t, time.UTC, first.StartedAt.Location())
	require.True(t, first.EndedAt.Equal(time.UnixMilli(1400)))
	require.Equal(t, int64(8812), *first.ErrorLogID)
	require.Nil(t, first.UsageID)

	require.Equal(t, 2, second.Index)
	require.Equal(t, opsRequestAttemptSourceUsage, second.Source)
	require.Equal(t, opsRequestAttemptOutcomeSuccess, second.Outcome)
	require.Equal(t, int64(9018), *second.UsageID)
	require.Equal(t, int64(1), *second.AccountID)
	require.Equal(t, "Claude-01", second.AccountName)
	require.Equal(t, http.StatusOK, *second.UpstreamStatusCode)
	require.Equal(t, int64(9800), *second.DurationMs)
	require.Equal(t, int64(1400), *second.FirstTokenMs)
	require.Equal(t, "msg_up_1", second.UpstreamRequestID)
	require.True(t, second.EndedAt.Equal(usageAt))
	require.True(t, second.StartedAt.Equal(usageAt.Add(-9800*time.Millisecond)))

	require.Equal(t, http.StatusOK, *got.ClientStatusCode)
	require.Equal(t, []int64{8812}, got.ErrorLogIDs)
	require.Zero(t, got.DroppedEarlierAttempts)
}

func TestBuildOpsRequestAttempts_LegacyEventsHaveNullTiming(t *testing.T) {
	errRows := []*OpsRequestAttemptErrorRow{{
		ID: 5, CreatedAt: time.UnixMilli(3000).UTC(), StatusCode: 502,
		UpstreamErrorsJSON: `[{"at_unix_ms":2000,"account_id":4,"upstream_status_code":500,"kind":"http_error"}]`,
	}}
	got := buildOpsRequestAttempts(errRows, nil)
	require.Len(t, got.Attempts, 1)
	raw, err := json.Marshal(got.Attempts[0])
	require.NoError(t, err)
	require.Contains(t, string(raw), `"started_at":null`)
	require.Contains(t, string(raw), `"duration_ms":null`)
	require.Contains(t, string(raw), `"ended_at":"1970-01-01T00:00:02Z"`)
	require.Contains(t, string(raw), `"usage_id":null`)
	require.Contains(t, string(raw), `"proxy_name":"unknown"`)
}

func TestBuildOpsRequestAttempts_DedupesSharedEventsAcrossRows(t *testing.T) {
	shared := []OpsUpstreamErrorEvent{
		{AtUnixMs: 1000, AccountID: 1, UpstreamStatusCode: 429, Kind: "retry"},
		{AtUnixMs: 1000, AccountID: 1, UpstreamStatusCode: 429, Kind: "retry"},
	}
	streamRow := &OpsRequestAttemptErrorRow{ID: 1, CreatedAt: time.UnixMilli(5000), StatusCode: 502, UpstreamErrorsJSON: attemptEventsJSON(t, shared...)}
	recoveredRow := &OpsRequestAttemptErrorRow{ID: 2, CreatedAt: time.UnixMilli(5001), StatusCode: 200, UpstreamErrorsJSON: attemptEventsJSON(t,
		append(append([]OpsUpstreamErrorEvent{}, shared...), OpsUpstreamErrorEvent{AtUnixMs: 2000, AccountID: 2, UpstreamStatusCode: 500, Kind: "failover"})...)}

	got := buildOpsRequestAttempts([]*OpsRequestAttemptErrorRow{streamRow, recoveredRow}, nil)

	require.Len(t, got.Attempts, 3, "two same-ms retries stay distinct; the copy in the second row is dropped")
	require.Equal(t, int64(1), *got.Attempts[0].ErrorLogID)
	require.Equal(t, int64(1), *got.Attempts[1].ErrorLogID)
	require.Equal(t, int64(2), *got.Attempts[2].ErrorLogID)
	require.Equal(t, int64(2), *got.Attempts[2].AccountID)
	require.Equal(t, []int64{1, 2}, got.ErrorLogIDs)
	require.Equal(t, 502, *got.ClientStatusCode, "the client saw the latest failure status")
}

func TestBuildOpsRequestAttempts_FinalFailureAndPartial(t *testing.T) {
	failure := &OpsRequestAttemptErrorRow{ID: 7, CreatedAt: time.UnixMilli(9000), StatusCode: 502,
		UpstreamErrorsJSON: attemptEventsJSON(t, OpsUpstreamErrorEvent{AtUnixMs: 8000, AccountID: 2, UpstreamStatusCode: 502, Kind: "http_error"})}

	got := buildOpsRequestAttempts([]*OpsRequestAttemptErrorRow{failure}, nil)
	require.Equal(t, 502, *got.ClientStatusCode)
	require.Len(t, got.Attempts, 1)
	require.Equal(t, opsRequestAttemptOutcomeFailed, got.Attempts[0].Outcome)

	usage := &OpsRequestAttemptUsageRow{ID: 33, CreatedAt: time.UnixMilli(8500), AccountID: 2}
	got = buildOpsRequestAttempts([]*OpsRequestAttemptErrorRow{failure}, []*OpsRequestAttemptUsageRow{usage})
	require.Len(t, got.Attempts, 2)
	require.Equal(t, opsRequestAttemptOutcomeFailed, got.Attempts[0].Outcome)
	require.Equal(t, opsRequestAttemptOutcomePartial, got.Attempts[1].Outcome, "usage was billed but the client still got a failure")
	require.Nil(t, got.Attempts[1].DurationMs)
	require.Nil(t, got.Attempts[1].StartedAt)
	require.Equal(t, 502, *got.ClientStatusCode)

	got = buildOpsRequestAttempts(nil, []*OpsRequestAttemptUsageRow{usage})
	require.Equal(t, http.StatusOK, *got.ClientStatusCode)
	require.Equal(t, opsRequestAttemptOutcomeSuccess, got.Attempts[0].Outcome)
	require.Empty(t, got.ErrorLogIDs)
	require.NotNil(t, got.ErrorLogIDs)

	empty := buildOpsRequestAttempts([]*OpsRequestAttemptErrorRow{{ID: 9, StatusCode: 429}}, nil)
	require.Empty(t, empty.Attempts)
	require.NotNil(t, empty.Attempts)
	require.Equal(t, 429, *empty.ClientStatusCode)
}

func TestBuildOpsRequestAttempts_DroppedCountAndCorruptRows(t *testing.T) {
	rows := []*OpsRequestAttemptErrorRow{
		{ID: 1, CreatedAt: time.UnixMilli(1000), StatusCode: 502, UpstreamErrorsJSON: `{not json`},
		{ID: 2, CreatedAt: time.UnixMilli(2000), StatusCode: 502, UpstreamErrorsJSON: attemptEventsJSON(t,
			OpsUpstreamErrorEvent{AtUnixMs: 1500, AccountID: 1, UpstreamStatusCode: 500, DroppedEarlierAttempts: 3})},
	}
	got := buildOpsRequestAttempts(rows, nil)
	require.Equal(t, 3, got.DroppedEarlierAttempts)
	require.Len(t, got.Attempts, 1)
	require.Equal(t, []int64{1, 2}, got.ErrorLogIDs, "a row with unreadable events is still listed")
}

func TestOpsServiceGetRequestAttempts_FillsMissingAccountNames(t *testing.T) {
	repo := &requestAttemptsRepoFake{
		errRows: []*OpsRequestAttemptErrorRow{{ID: 1, CreatedAt: time.UnixMilli(5000), RequestID: "req-n", ClientRequestID: "cid-n", StatusCode: 200,
			UpstreamErrorsJSON: attemptEventsJSON(t,
				OpsUpstreamErrorEvent{AtUnixMs: 1000, AccountID: 3, UpstreamStatusCode: 429, Kind: "failover"},
				OpsUpstreamErrorEvent{AtUnixMs: 2000, AccountID: 4, AccountName: "named", UpstreamStatusCode: 500})}},
		names: map[int64]string{3: "Claude-03"},
	}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	got, err := svc.GetRequestAttempts(context.Background(), "req-n", nil)
	require.NoError(t, err)
	require.Equal(t, [][]int64{{3}}, repo.nameCalls, "only nameless accounts are looked up")
	require.Equal(t, "Claude-03", got.Attempts[0].AccountName)
	require.Equal(t, "named", got.Attempts[1].AccountName)
	require.Equal(t, "req-n", got.RequestID)
	require.Equal(t, "cid-n", got.ClientRequestID)

	repo.nameCalls = nil
	repo.namesErr = errors.New("db down")
	got, err = svc.GetRequestAttempts(context.Background(), "req-n", nil)
	require.NoError(t, err, "names are cosmetic; a failed lookup keeps the chain")
	require.Empty(t, got.Attempts[0].AccountName)
}

func TestOpsServiceGetRequestAttempts_Errors(t *testing.T) {
	ctx := context.Background()

	plain := NewOpsService(struct{ OpsRepository }{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	_, err := plain.GetRequestAttempts(ctx, "req-1", nil)
	require.Equal(t, "OPS_REPO_UNAVAILABLE", infraerrors.Reason(err))
	require.Equal(t, http.StatusServiceUnavailable, infraerrors.Code(err))

	repo := &requestAttemptsRepoFake{}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	_, err = svc.GetRequestAttempts(ctx, "req-1", nil)
	require.ErrorIs(t, err, ErrOpsRequestNotFound)
	require.Equal(t, "OPS_REQUEST_NOT_FOUND", infraerrors.Reason(err))
	require.Equal(t, http.StatusNotFound, infraerrors.Code(err))

	// Stored ids are valid UTF-8 without NUL; Postgres would reject these with a 500.
	for _, raw := range []string{"", "   ", strings.Repeat("x", 257), "\x00", "req\x00x", "\xff", "req-\xc3"} {
		_, err = svc.GetRequestAttempts(ctx, raw, nil)
		require.Equal(t, "INVALID_REQUEST_ID", infraerrors.Reason(err), raw)
		require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	}

	repo.errRows = []*OpsRequestAttemptErrorRow{
		{ID: 1, CreatedAt: time.UnixMilli(1000), RequestID: "dup", ClientRequestID: "cid-a", StatusCode: 502},
		{ID: 2, CreatedAt: time.UnixMilli(2000), RequestID: "dup", ClientRequestID: "cid-a", StatusCode: 200},
		{ID: 3, CreatedAt: time.UnixMilli(3000), RequestID: "dup", ClientRequestID: "cid-b", StatusCode: 502},
		{ID: 4, CreatedAt: time.UnixMilli(4000), RequestID: "dup", StatusCode: 502},
	}
	usageCalls := len(repo.usageLookups)
	_, err = svc.GetRequestAttempts(ctx, "dup", nil)
	require.ErrorIs(t, err, ErrOpsRequestAmbiguous)
	require.Equal(t, "OPS_REQUEST_AMBIGUOUS", infraerrors.Reason(err))
	require.Equal(t, http.StatusConflict, infraerrors.Code(err))
	require.Equal(t, "cid-b,cid-a", infraerrors.FromError(err).Metadata["client_request_ids"], "newest request first")
	require.Equal(t, "1970-01-01T00:00:03Z,1970-01-01T00:00:02Z", infraerrors.FromError(err).Metadata["client_request_id_times"],
		"each id carries the time of its newest ops row")
	require.Equal(t, usageCalls, len(repo.usageLookups), "an ambiguous id never reaches usage")

	many := make([]*OpsRequestAttemptErrorRow, 0, 7)
	for i := 0; i < 7; i++ {
		many = append(many, &OpsRequestAttemptErrorRow{ID: int64(i + 1), CreatedAt: time.UnixMilli(int64(i+1) * 1000).UTC(), RequestID: "dup", ClientRequestID: "cid-" + string(rune('a'+i))})
	}
	repo.errRows = many
	_, err = svc.GetRequestAttempts(ctx, "dup", nil)
	require.Equal(t, "cid-g,cid-f,cid-e,cid-d,cid-c", infraerrors.FromError(err).Metadata["client_request_ids"], "at most the five newest ids are listed")
	require.Equal(t, "1970-01-01T00:00:07Z,1970-01-01T00:00:06Z,1970-01-01T00:00:05Z,1970-01-01T00:00:04Z,1970-01-01T00:00:03Z",
		infraerrors.FromError(err).Metadata["client_request_id_times"])
	repo.errRows = nil

	calls := len(repo.errLookups)
	svc.runtimeSettings.Store(&opsRuntimeSettingsSnapshot{monitoringEnabled: false})
	_, err = svc.GetRequestAttempts(ctx, "req-1", nil)
	require.ErrorIs(t, err, ErrOpsDisabled)

	cfg := &config.Config{}
	cfg.Ops.Enabled = false
	off := NewOpsService(repo, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	_, err = off.GetRequestAttempts(ctx, "req-1", nil)
	require.Equal(t, "OPS_DISABLED", infraerrors.Reason(err))
	require.Equal(t, http.StatusNotFound, infraerrors.Code(err))
	require.Equal(t, calls, len(repo.errLookups), "disabled monitoring must not query")
}

func TestOpsServiceGetRequestAttempts_ResolvesClientPrefixAndAround(t *testing.T) {
	ctx := context.Background()
	repo := &requestAttemptsRepoFake{usageRows: []*OpsRequestAttemptUsageRow{{ID: 1, CreatedAt: time.UnixMilli(1000), RequestID: "client:abc"}}}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	got, err := svc.GetRequestAttempts(ctx, "client:abc", nil)
	require.NoError(t, err)
	require.Len(t, repo.errLookups, 1)
	require.Equal(t, []string{"abc"}, repo.errLookups[0].ClientRequestIDs)
	require.Contains(t, repo.errLookups[0].RequestIDs, "client:abc")
	require.Nil(t, repo.errLookups[0].StartTime)
	require.Nil(t, repo.errLookups[0].EndTime)
	require.Contains(t, repo.usageLookups[0].RequestIDs, "client:abc")
	require.Equal(t, "abc", got.ClientRequestID)
	require.Equal(t, "client:abc", got.RequestID)

	// A bare id may be the ops request_id or the server client_request_id.
	repo.errLookups, repo.usageLookups = nil, nil
	got, err = svc.GetRequestAttempts(ctx, "  bare-id  ", nil)
	require.NoError(t, err)
	require.Equal(t, "bare-id", got.RequestID)
	require.Equal(t, "abc", got.ClientRequestID, "recovered from the usage request_id")
	require.Equal(t, []string{"bare-id"}, repo.errLookups[0].RequestIDs)
	require.Equal(t, []string{"bare-id"}, repo.errLookups[0].ClientRequestIDs)
	require.ElementsMatch(t, []string{"bare-id", "client:bare-id", "local:bare-id", "probe:bare-id"}, repo.usageLookups[0].RequestIDs)

	// Error rows pin the client_request_id the usage row is keyed by.
	repo.errLookups, repo.usageLookups = nil, nil
	repo.errRows = []*OpsRequestAttemptErrorRow{{ID: 1, CreatedAt: time.UnixMilli(1000), RequestID: "req-x", ClientRequestID: "cid-x", StatusCode: 502}}
	_, err = svc.GetRequestAttempts(ctx, "req-x", nil)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"req-x", "client:cid-x", "local:req-x", "probe:req-x"}, repo.usageLookups[0].RequestIDs)

	repo.errLookups, repo.usageLookups = nil, nil
	around := time.Date(2026, 10, 18, 9, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	_, err = svc.GetRequestAttempts(ctx, "req-x", &around)
	require.NoError(t, err)
	for _, lookup := range []OpsRequestAttemptLookup{repo.errLookups[0], repo.usageLookups[0]} {
		require.NotNil(t, lookup.StartTime)
		require.NotNil(t, lookup.EndTime)
		require.True(t, lookup.StartTime.Equal(around.Add(-time.Hour)))
		require.True(t, lookup.EndTime.Equal(around.Add(time.Hour)))
		require.Equal(t, time.UTC, lookup.StartTime.Location())
	}
}

// A coalesced probe leader without X-Request-ID is billed as probe:<ops request_id>.
func TestOpsServiceGetRequestAttempts_ResolvesProbeUsageIDs(t *testing.T) {
	ctx := context.Background()
	repo := &requestAttemptsRepoFake{
		errRows:   []*OpsRequestAttemptErrorRow{{ID: 1, CreatedAt: time.UnixMilli(1000), RequestID: "req-p", ClientRequestID: "cid-p", StatusCode: 200}},
		usageRows: []*OpsRequestAttemptUsageRow{{ID: 2, CreatedAt: time.UnixMilli(2000), RequestID: "probe:req-p"}},
	}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	got, err := svc.GetRequestAttempts(ctx, "req-p", nil)
	require.NoError(t, err)
	require.Contains(t, repo.usageLookups[0].RequestIDs, "probe:req-p")
	require.Equal(t, "req-p", got.RequestID)
	require.Equal(t, "cid-p", got.ClientRequestID)

	// The usage request_id leads back to the ops rows.
	repo.errLookups, repo.usageLookups = nil, nil
	got, err = svc.GetRequestAttempts(ctx, "probe:req-p", nil)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"probe:req-p", "req-p"}, repo.errLookups[0].RequestIDs)
	require.Empty(t, repo.errLookups[0].ClientRequestIDs)
	require.ElementsMatch(t, []string{"probe:req-p", "client:cid-p", "local:req-p"}, repo.usageLookups[0].RequestIDs)
	require.Equal(t, "req-p", got.RequestID)
	require.Equal(t, "cid-p", got.ClientRequestID)
}

// usage duration_ms runs from the Forward entry, so a same-account retry
// inside Forward would otherwise make the billed try overlap the failed one.
func TestBuildOpsRequestAttempts_SameAccountRetryClampsBilledStart(t *testing.T) {
	base := time.Date(2026, 10, 18, 1, 41, 0, 0, time.UTC)
	ms := base.UnixMilli()
	retryRow := &OpsRequestAttemptErrorRow{ID: 1, CreatedAt: base.Add(12 * time.Second), StatusCode: 200,
		UpstreamErrorsJSON: attemptEventsJSON(t, OpsUpstreamErrorEvent{
			AtUnixMs: ms + 300, StartedAtUnixMs: ms, DurationMs: attemptInt64(300),
			AccountID: 1, UpstreamStatusCode: 529, Kind: "retry",
		})}
	usage := &OpsRequestAttemptUsageRow{ID: 9, CreatedAt: base.Add(11100 * time.Millisecond), AccountID: 1,
		DurationMs: attemptInt64(11100), FirstTokenMs: attemptInt64(2500)}

	got := buildOpsRequestAttempts([]*OpsRequestAttemptErrorRow{retryRow}, []*OpsRequestAttemptUsageRow{usage})

	require.Len(t, got.Attempts, 2)
	first, billed := got.Attempts[0], got.Attempts[1]
	require.True(t, first.StartedAt.Equal(base))
	require.Equal(t, int64(300), *first.DurationMs)
	require.Equal(t, opsRequestAttemptOutcomeSuccess, billed.Outcome)
	require.True(t, billed.StartedAt.Equal(base.Add(300*time.Millisecond)), "the billed try starts no earlier than the previous one ended")
	require.Equal(t, time.UTC, billed.StartedAt.Location())
	require.Equal(t, int64(10800), *billed.DurationMs)
	require.Equal(t, int64(2200), *billed.FirstTokenMs, "first token is re-based on the clamped start")
	require.True(t, billed.EndedAt.Equal(usage.CreatedAt))

	// A first token reported before the clamped start is dropped, not negative.
	early := *usage
	early.FirstTokenMs = attemptInt64(200)
	got = buildOpsRequestAttempts([]*OpsRequestAttemptErrorRow{retryRow}, []*OpsRequestAttemptUsageRow{&early})
	require.Nil(t, got.Attempts[1].FirstTokenMs)
	require.Equal(t, int64(10800), *got.Attempts[1].DurationMs)

	// A partial billed try keeps the estimate: its own stream failure event
	// ends inside it and is not an earlier attempt.
	streamRow := &OpsRequestAttemptErrorRow{ID: 2, CreatedAt: base.Add(6 * time.Second), StatusCode: 502,
		UpstreamErrorsJSON: attemptEventsJSON(t, OpsUpstreamErrorEvent{
			AtUnixMs: ms + 5000, StartedAtUnixMs: ms + 10, AccountID: 1, UpstreamStatusCode: 403, Kind: "stream_error",
		})}
	partial := &OpsRequestAttemptUsageRow{ID: 10, CreatedAt: base.Add(5010 * time.Millisecond), AccountID: 1,
		DurationMs: attemptInt64(5010), FirstTokenMs: attemptInt64(900)}
	got = buildOpsRequestAttempts([]*OpsRequestAttemptErrorRow{streamRow}, []*OpsRequestAttemptUsageRow{partial})
	require.Len(t, got.Attempts, 2)
	require.Equal(t, opsRequestAttemptOutcomePartial, got.Attempts[1].Outcome)
	require.True(t, got.Attempts[1].StartedAt.Equal(base))
	require.Equal(t, int64(5010), *got.Attempts[1].DurationMs)
	require.Equal(t, int64(900), *got.Attempts[1].FirstTokenMs)
}

func TestParseOpsRequestAttemptsAround(t *testing.T) {
	got, err := ParseOpsRequestAttemptsAround("")
	require.NoError(t, err)
	require.Nil(t, got)

	got, err = ParseOpsRequestAttemptsAround(" 2026-10-18T01:41:00.5+08:00 ")
	require.NoError(t, err)
	require.True(t, got.Equal(time.Date(2026, 10, 17, 17, 41, 0, 500_000_000, time.UTC)))
	require.Equal(t, time.UTC, got.Location())

	for _, raw := range []string{"abc", "2026-10-18", "1760751660"} {
		_, err = ParseOpsRequestAttemptsAround(raw)
		require.Equal(t, "INVALID_AROUND", infraerrors.Reason(err), raw)
		require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	}
}

func attemptInt64(v int64) *int64 { return &v }
