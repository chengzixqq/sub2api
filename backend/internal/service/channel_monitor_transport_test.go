package service

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type monitorRoundTripFunc func(*http.Request) (*http.Response, error)

func (f monitorRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestChannelMonitorTransport_OptInAndPreservesResponse(t *testing.T) {
	client := &http.Client{Transport: monitorRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("unchanged")), Request: r}, nil
	})}
	req, _ := http.NewRequest(http.MethodPost, "https://fixture.invalid/v1/messages", nil)
	require.Same(t, client, ObserveChannelMonitorHTTPClient(client, req, 66))
	ctx, attempts := WithChannelMonitorAttempts(context.Background())
	req = req.WithContext(ctx)
	resp, err := ObserveChannelMonitorHTTPClient(client, req, 66).Do(req)
	require.NoError(t, err)
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, "unchanged", string(data))
	got, truncated := attempts.Snapshot()
	require.False(t, truncated)
	require.Len(t, got, 1)
	require.EqualValues(t, 66, got[0].AccountID)
	require.Equal(t, 200, got[0].HTTPStatus)
	require.Equal(t, "unknown", got[0].Outcome)
	require.Equal(t, "http_complete", got[0].ErrorCategory)
}

func TestChannelMonitorTransport_SkipsAuthAndBoundsRetries(t *testing.T) {
	ctx, attempts := WithChannelMonitorAttempts(context.Background())
	client := &http.Client{Transport: monitorRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("credentials omitted")), Request: r}, nil
	})}
	auth, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://fixture.invalid/oauth/token", nil)
	require.Same(t, client, ObserveChannelMonitorHTTPClient(client, auth, 66))
	for range ChannelMonitorObservationMaxAttempts + 3 {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://fixture.invalid/v1/messages", nil)
		resp, err := ObserveChannelMonitorHTTPClient(client, req, 66).Do(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
	}
	got, truncated := attempts.Snapshot()
	require.True(t, truncated)
	require.Len(t, got, ChannelMonitorObservationMaxAttempts)
	require.Equal(t, "channel_error", got[0].Outcome)
	require.Equal(t, "upstream_auth", got[0].ErrorCategory)
}

func TestChannelMonitorTransportRecognizesCustomGenerationBasePath(t *testing.T) {
	for _, path := range []string{"/proxy/messages", "/gateway/chat/completions", "/custom/responses", "/projects/p:generateContent"} {
		u, err := url.Parse("https://fixture.invalid" + path)
		require.NoError(t, err)
		require.True(t, channelMonitorBusinessURL(u), path)
	}
	u, err := url.Parse("https://fixture.invalid/oauth/token")
	require.NoError(t, err)
	require.False(t, channelMonitorBusinessURL(u))
}
