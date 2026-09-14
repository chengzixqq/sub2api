package repository

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestHTTPUpstream_RecordsGatewayAttemptAndBodyLifecycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = io.WriteString(w, "too large")
	}))
	defer server.Close()
	ctx, timing := service.WithGatewayRequestTiming(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader("payload"))
	require.NoError(t, err)
	upstream := NewHTTPUpstream(&config.Config{})
	response, err := upstream.DoWithTLS(req, "", 66, 10, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusRequestEntityTooLarge, response.StatusCode)
	_, err = io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.NoError(t, response.Body.Close())
	snapshot := timing.Snapshot()
	require.EqualValues(t, 1, snapshot.UpstreamAttempts)
	require.Zero(t, snapshot.UpstreamBodiesOpen)
	require.NotNil(t, snapshot.MaxDeclaredRequestBytes)
	require.EqualValues(t, 7, *snapshot.MaxDeclaredRequestBytes)
	require.EqualValues(t, 9, snapshot.UpstreamResponseBytes)
	require.EqualValues(t, 1, snapshot.PhaseCalls["upstream_headers"])
	require.EqualValues(t, 1, snapshot.PhaseCalls["upstream_body"])
	require.NotNil(t, snapshot.FirstUpstreamByteMS)
}
