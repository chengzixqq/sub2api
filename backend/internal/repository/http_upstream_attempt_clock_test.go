package repository

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestHTTPUpstreamDoMarksOpsAttemptStart(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(target.Close)
	upstream := NewHTTPUpstream(nil)

	ctx, clock := service.WithOpsUpstreamAttemptClock(t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL, nil)
	require.NoError(t, err)
	before := time.Now().UnixMilli()
	resp, err := upstream.Do(req, "", 7, 1)
	after := time.Now().UnixMilli()
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	accountID, startedAt, pending := clock.Pending()
	require.True(t, pending, "the dispatch waits for its attempt event")
	require.Equal(t, int64(7), accountID)
	require.GreaterOrEqual(t, startedAt, before)
	require.LessOrEqual(t, startedAt, after)

	// DoWithTLS over plain HTTP delegates to Do and marks exactly once.
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, target.URL, nil)
	require.NoError(t, err)
	resp, err = upstream.DoWithTLS(req, "", 8, 1, &tlsfingerprint.Profile{Name: "test"})
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	accountID, _, pending = clock.Pending()
	require.True(t, pending)
	require.Equal(t, int64(8), accountID, "the newest dispatch wins")

	// Without a clock the request is unaffected.
	req, err = http.NewRequestWithContext(t.Context(), http.MethodGet, target.URL, nil)
	require.NoError(t, err)
	resp, err = upstream.Do(req, "", 7, 1)
	require.NoError(t, err)
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
}
