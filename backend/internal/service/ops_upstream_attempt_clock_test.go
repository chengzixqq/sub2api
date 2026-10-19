package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newAttemptClockTestContext(t *testing.T) (*gin.Context, *OpsUpstreamAttemptClock) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	clock := InstallOpsUpstreamAttemptClock(c)
	require.NotNil(t, clock)
	return c, clock
}

func lastOpsUpstreamEvent(t *testing.T, c *gin.Context) *OpsUpstreamErrorEvent {
	t.Helper()
	v, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := v.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.NotEmpty(t, events)
	return events[len(events)-1]
}

func TestAppendOpsUpstreamErrorStampsAttemptStartAndDuration(t *testing.T) {
	c, clock := newAttemptClockTestContext(t)
	clock.mark(42, 1000)

	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{AccountID: 42, AtUnixMs: 1412, UpstreamStatusCode: 429, Kind: "failover"})

	ev := lastOpsUpstreamEvent(t, c)
	require.Equal(t, int64(1000), ev.StartedAtUnixMs)
	require.NotNil(t, ev.DurationMs)
	require.Equal(t, int64(412), *ev.DurationMs)
	raw, err := json.Marshal(ev)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"started_at_unix_ms":1000`)
	require.Contains(t, string(raw), `"duration_ms":412`)
}

func TestAppendOpsUpstreamErrorAttemptStartClaimedOnce(t *testing.T) {
	c, clock := newAttemptClockTestContext(t)
	clock.mark(42, 1000)

	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{AccountID: 42, AtUnixMs: 1300, UpstreamStatusCode: 401, Kind: "http_error"})
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{AccountID: 42, AtUnixMs: 1350, Kind: "account_auth", Stage: "account_auth", Message: "refresh failed"})

	v, _ := c.Get(OpsUpstreamErrorsKey)
	events := v.([]*OpsUpstreamErrorEvent)
	require.Len(t, events, 2)
	require.Equal(t, int64(1000), events[0].StartedAtUnixMs)
	require.Equal(t, int64(300), *events[0].DurationMs)
	require.Zero(t, events[1].StartedAtUnixMs, "a dispatch start is claimed by one event only")
	require.Nil(t, events[1].DurationMs)
	_, _, pending := clock.Pending()
	require.False(t, pending)

	// A new dispatch re-arms the clock for the next attempt.
	clock.mark(42, 2000)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{AccountID: 42, AtUnixMs: 2100, UpstreamStatusCode: 500})
	ev := lastOpsUpstreamEvent(t, c)
	require.Equal(t, int64(2000), ev.StartedAtUnixMs)
	require.Equal(t, int64(100), *ev.DurationMs)
}

func TestAppendOpsUpstreamErrorAttemptStartIgnoresOtherAccount(t *testing.T) {
	c, clock := newAttemptClockTestContext(t)
	clock.mark(1, 1000)

	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{AccountID: 2, AtUnixMs: 1500, UpstreamStatusCode: 429})
	ev := lastOpsUpstreamEvent(t, c)
	require.Zero(t, ev.StartedAtUnixMs)
	require.Nil(t, ev.DurationMs)

	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{AtUnixMs: 1500, Message: "no account"})
	ev = lastOpsUpstreamEvent(t, c)
	require.Zero(t, ev.StartedAtUnixMs)
	require.Nil(t, ev.DurationMs)

	// A dispatch that started after the event cannot belong to it.
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{AccountID: 1, AtUnixMs: 900, UpstreamStatusCode: 500})
	ev = lastOpsUpstreamEvent(t, c)
	require.Zero(t, ev.StartedAtUnixMs)

	accountID, startedAt, pending := clock.Pending()
	require.True(t, pending, "unrelated events leave the dispatch unclaimed")
	require.Equal(t, int64(1), accountID)
	require.Equal(t, int64(1000), startedAt)
}

func TestAppendOpsUpstreamErrorKeepsCallerSuppliedTiming(t *testing.T) {
	c, clock := newAttemptClockTestContext(t)
	clock.mark(7, 1000)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{AccountID: 7, AtUnixMs: 1500, StartedAtUnixMs: 1200, UpstreamStatusCode: 500})
	ev := lastOpsUpstreamEvent(t, c)
	require.Equal(t, int64(1200), ev.StartedAtUnixMs)
	require.Equal(t, int64(300), *ev.DurationMs)
	_, _, pending := clock.Pending()
	require.True(t, pending, "an explicit start does not consume the clock")

	orphan := int64(55)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{AccountID: 8, AtUnixMs: 1500, DurationMs: &orphan, UpstreamStatusCode: 500})
	ev = lastOpsUpstreamEvent(t, c)
	require.Zero(t, ev.StartedAtUnixMs)
	require.Nil(t, ev.DurationMs, "a duration without a start is dropped")
}

func TestAppendOpsUpstreamErrorWithoutClockKeepsLegacyShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.Nil(t, c.Request)

	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{AccountID: 3, AtUnixMs: 1412, UpstreamStatusCode: 429, Kind: "failover"})

	ev := lastOpsUpstreamEvent(t, c)
	raw, err := json.Marshal(ev)
	require.NoError(t, err)
	require.Equal(t, `{"at_unix_ms":1412,"account_id":3,"proxy_id":null,"proxy_name":"unknown","upstream_status_code":429,"kind":"failover"}`, string(raw))
}

func TestSanitizeOpsUpstreamErrorsKeepsAttemptTiming(t *testing.T) {
	negative := int64(-5)
	kept := int64(412)
	orphan := int64(99)
	entry := &OpsInsertErrorLogInput{UpstreamErrors: []*OpsUpstreamErrorEvent{
		{AccountID: 1, AtUnixMs: 1412, StartedAtUnixMs: 1000, DurationMs: &kept, UpstreamStatusCode: 429},
		{AccountID: 2, AtUnixMs: 1500, StartedAtUnixMs: 1600, DurationMs: &negative, UpstreamStatusCode: 500},
		{AccountID: 3, AtUnixMs: 1700, StartedAtUnixMs: -1, DurationMs: &orphan, UpstreamStatusCode: 502},
		{AccountID: 4, AtUnixMs: 1800, DurationMs: &orphan, UpstreamStatusCode: 503},
	}}
	require.NoError(t, SanitizeOpsUpstreamErrorsForQueue(entry))
	require.NotNil(t, entry.UpstreamErrorsJSON)
	require.Contains(t, *entry.UpstreamErrorsJSON, `"started_at_unix_ms":1000`)
	require.Contains(t, *entry.UpstreamErrorsJSON, `"duration_ms":412`)
	require.Equal(t, int64(-5), negative, "sanitizing must not write through caller pointers")

	events, err := ParseOpsUpstreamErrors(*entry.UpstreamErrorsJSON)
	require.NoError(t, err)
	require.Len(t, events, 4)
	require.Equal(t, int64(1000), events[0].StartedAtUnixMs)
	require.Equal(t, int64(412), *events[0].DurationMs)
	require.Equal(t, int64(1600), events[1].StartedAtUnixMs)
	require.Equal(t, int64(0), *events[1].DurationMs, "negative durations are clamped")
	for _, ev := range events[2:] {
		require.Zero(t, ev.StartedAtUnixMs)
		require.Nil(t, ev.DurationMs, "duration without a start is dropped")
	}
}

func TestMarkOpsUpstreamAttemptStartWithoutClockIsNoop(t *testing.T) {
	require.NotPanics(t, func() {
		MarkOpsUpstreamAttemptStart(context.Background(), 1)
		MarkOpsUpstreamAttemptStart(nil, 1) //nolint:staticcheck // nil context is tolerated on purpose
	})
	allocs := testing.AllocsPerRun(100, func() {
		MarkOpsUpstreamAttemptStart(context.Background(), 1)
	})
	require.Zero(t, allocs, "the gateway hot path must not allocate without a clock")

	ctx, clock := WithOpsUpstreamAttemptClock(context.Background())
	again, same := WithOpsUpstreamAttemptClock(ctx)
	require.Same(t, clock, same)
	require.Equal(t, ctx, again)

	before := time.Now().UnixMilli()
	MarkOpsUpstreamAttemptStart(ctx, 0)
	_, _, pending := clock.Pending()
	require.False(t, pending, "account 0 is not a dispatch")
	MarkOpsUpstreamAttemptStart(ctx, 9)
	after := time.Now().UnixMilli()
	accountID, startedAt, pending := clock.Pending()
	require.True(t, pending)
	require.Equal(t, int64(9), accountID)
	require.GreaterOrEqual(t, startedAt, before)
	require.LessOrEqual(t, startedAt, after)
}

func TestInstallOpsUpstreamAttemptClockIsIdempotent(t *testing.T) {
	c, clock := newAttemptClockTestContext(t)
	request := c.Request
	require.Same(t, clock, OpsUpstreamAttemptClockFromContext(c.Request.Context()))
	require.Same(t, clock, InstallOpsUpstreamAttemptClock(c))
	require.Same(t, request, c.Request, "a second install does not copy the request")

	gin.SetMode(gin.TestMode)
	bare, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.NotNil(t, InstallOpsUpstreamAttemptClock(bare), "works without a request")
	require.Nil(t, InstallOpsUpstreamAttemptClock(nil))
}

// The gateway builds upstream requests from c.Request.Context(); a dispatch
// marked on that derived context is claimed by the event appended on c.
func TestOpsUpstreamAttemptClockReachesEventsThroughDerivedRequestContext(t *testing.T) {
	c, _ := newAttemptClockTestContext(t)
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://upstream.example/v1/messages", nil)
	require.NoError(t, err)

	before := time.Now().UnixMilli()
	MarkOpsUpstreamAttemptStart(upstreamReq.Context(), 42)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{AccountID: 42, UpstreamStatusCode: 429, Kind: "failover"})

	ev := lastOpsUpstreamEvent(t, c)
	require.GreaterOrEqual(t, ev.StartedAtUnixMs, before)
	require.NotNil(t, ev.DurationMs)
	require.Equal(t, ev.AtUnixMs-ev.StartedAtUnixMs, *ev.DurationMs)
}
