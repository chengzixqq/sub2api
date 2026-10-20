//go:build unit

package repository

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

var oauthLogCaptureMu sync.Mutex

type oauthLogSink struct {
	mu     sync.Mutex
	events []logger.LogEvent
}

func (s *oauthLogSink) WriteLogEvent(event *logger.LogEvent) {
	if event == nil {
		return
	}
	s.mu.Lock()
	s.events = append(s.events, *event)
	s.mu.Unlock()
}

func (s *oauthLogSink) find(component, substr string) (logger.LogEvent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range s.events {
		if ev.Fields["component"] == component && strings.Contains(ev.Message, substr) {
			return ev, true
		}
	}
	return logger.LogEvent{}, false
}

func captureOAuthLog(t *testing.T) *oauthLogSink {
	t.Helper()
	oauthLogCaptureMu.Lock()
	require.NoError(t, logger.Init(logger.InitOptions{
		Level:    "debug",
		Format:   "json",
		Output:   logger.OutputOptions{ToStdout: true},
		Sampling: logger.SamplingOptions{Enabled: false},
	}))
	sink := &oauthLogSink{}
	logger.SetSink(sink)
	t.Cleanup(func() {
		logger.SetSink(nil)
		oauthLogCaptureMu.Unlock()
	})
	return sink
}

const rejectedExchangeBody = `{"error":"invalid_grant","refresh_token":"leak-secret"}`

func assertCodeRejectedLogged(t *testing.T, sink *oauthLogSink, component string) {
	t.Helper()
	ev, ok := sink.find(component, "token exchange rejected")
	require.True(t, ok, "expected a rejected-code log from %s", component)
	require.Equal(t, "warn", ev.Level)
	require.Contains(t, ev.Message, "status 400")
	require.Contains(t, ev.Message, "invalid_grant")
	require.NotContains(t, ev.Message, "leak-secret")
}

func TestOpenAIOAuthExchangeLogsRejectedCode(t *testing.T) {
	sink := captureOAuthLog(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(rejectedExchangeBody))
	}))
	defer server.Close()

	svc := &openaiOAuthService{tokenURL: server.URL}
	_, err := svc.ExchangeCode(context.Background(), "code", "ver", openai.DefaultRedirectURI, "", "")
	require.Equal(t, "OPENAI_OAUTH_CODE_REJECTED", infraerrors.Reason(err))
	assertCodeRejectedLogged(t, sink, "repository.openai_oauth")
}

func TestGrokOAuthExchangeLogsRejectedCode(t *testing.T) {
	sink := captureOAuthLog(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(rejectedExchangeBody))
	}))
	defer server.Close()
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	t.Setenv(xai.EnvTokenURL, server.URL)

	_, err := NewGrokOAuthClient().ExchangeCode(context.Background(), "auth-code", "verifier", "http://127.0.0.1:56121/callback", "", "client-id")
	require.Equal(t, "GROK_OAUTH_CODE_REJECTED", infraerrors.Reason(err))
	assertCodeRejectedLogged(t, sink, "repository.grok_oauth")
}
