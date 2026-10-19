//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Several probe paths write a local content note (target URL, uploaded input
// summary, which upstream endpoint is being called) before the request goes
// out. Those notes must stay in the SSE stream but must not stamp
// first_token_ms: it measures when the upstream started answering.

func newGrokLocalNoteTestAccount(id int64) *Account {
	return &Account{
		ID: id, Name: "grok-local-note", Platform: PlatformGrok,
		Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "grok-access-token",
			"refresh_token": "grok-refresh-token",
			"expires_at":    time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
		},
	}
}

func newUnauthorizedUpstreamRecorder() *httpUpstreamRecorder {
	return &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":"unauthorized"}`)),
	}}
}

func requireNoFirstTokenOnUpstreamFailure(t *testing.T, body, localNote string, result AccountTestResult) {
	t.Helper()
	require.Contains(t, body, localNote, "the local note is still written to the stream")
	require.Contains(t, body, `"type":"error"`)
	require.Nil(t, result.FirstTokenMs, "a local note sent before the upstream call is not the first token")
	require.Equal(t, intPtrForTraceTest(http.StatusUnauthorized), result.UpstreamStatus)
}

func TestAccountTestTrace_LocalNotesBeforeUpstreamDoNotCountAsFirstToken(t *testing.T) {
	t.Run("openai oauth image", func(t *testing.T) {
		c, recorder := newTestContext()
		beginAccountTestTrace(c)
		svc := &AccountTestService{httpUpstream: newUnauthorizedUpstreamRecorder()}
		account := &Account{
			ID: 53, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
			Credentials: map[string]any{"access_token": "token-123"},
		}

		require.Error(t, svc.testOpenAIImageOAuth(c, context.Background(), account, "gpt-image-1", "draw a cat"))

		requireNoFirstTokenOnUpstreamFailure(t, recorder.Body.String(), "Calling Codex", BuildAccountTestResult(c, false, nil, nil, nil))
		snapshot, _ := AccountTestTraceFrom(c)
		require.Contains(t, snapshot.ResponseText, "Calling Codex", "local notes stay in the captured text")
	})

	grokCases := []struct {
		name      string
		modelID   string
		mode      string
		opts      AccountTestOptions
		localNote string
	}{
		{"grok image edit with source image", "grok-imagine-image", AccountTestModeGrokImage,
			AccountTestOptions{ImageDataURL: minimalAccountTestPNGDataURL(8, 8)}, "source image ready"},
		{"grok video with first frame", "", AccountTestModeGrokVideo,
			AccountTestOptions{ImageDataURL: minimalAccountTestPNGDataURL(8, 8)}, "using uploaded first-frame"},
		{"grok stt with uploaded audio", "", AccountTestModeGrokSTT,
			AccountTestOptions{AudioDataURL: "data:audio/wav;base64,UklGRgAAAABXQVZF"}, "uploaded audio"},
	}
	for i, tc := range grokCases {
		t.Run(tc.name, func(t *testing.T) {
			account := newGrokLocalNoteTestAccount(int64(300 + i))
			repo := &mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}
			svc := &AccountTestService{
				accountRepo:       repo,
				grokTokenProvider: NewGrokTokenProvider(repo, nil),
				httpUpstream:      newUnauthorizedUpstreamRecorder(),
			}
			c, recorder := newTestContext()

			require.Error(t, svc.TestAccountConnection(c, account.ID, tc.modelID, "probe", tc.mode, tc.opts))

			requireNoFirstTokenOnUpstreamFailure(t, recorder.Body.String(), tc.localNote, BuildAccountTestResult(c, false, nil, nil, nil))
		})
	}

	t.Run("grok realtime dial failure", func(t *testing.T) {
		account := newGrokLocalNoteTestAccount(310)
		repo := &mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}
		svc := &AccountTestService{
			accountRepo:       repo,
			grokTokenProvider: NewGrokTokenProvider(repo, nil),
			grokWSDialer: &grokRealtimeTestDialer{
				status: http.StatusUnauthorized,
				err:    &openAIWSHandshakeError{Body: []byte(`{"error":"unauthorized"}`), Err: errors.New("websocket handshake failed")},
			},
		}
		c, recorder := newTestContext()

		require.Error(t, svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeGrokRealtime))

		requireNoFirstTokenOnUpstreamFailure(t, recorder.Body.String(), "realtime target", BuildAccountTestResult(c, false, nil, nil, nil))
	})
}

// slowGrokRealtimeDialer records when the handshake finished after a delay
// that is well above the platform clock resolution.
type slowGrokRealtimeDialer struct {
	grokRealtimeTestDialer
	delay    time.Duration
	dialedAt time.Time
}

func (d *slowGrokRealtimeDialer) Dial(ctx context.Context, wsURL string, headers http.Header, proxyURL string) (openAIWSClientConn, int, http.Header, error) {
	time.Sleep(d.delay)
	d.dialedAt = time.Now()
	return d.grokRealtimeTestDialer.Dial(ctx, wsURL, headers, proxyURL)
}

func TestAccountTestTrace_GrokRealtimeFirstTokenAfterHandshake(t *testing.T) {
	account := newGrokLocalNoteTestAccount(311)
	repo := &mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}
	dialer := &slowGrokRealtimeDialer{delay: 50 * time.Millisecond}
	svc := &AccountTestService{
		accountRepo:       repo,
		grokTokenProvider: NewGrokTokenProvider(repo, nil),
		grokWSDialer:      dialer,
	}
	c, recorder := newTestContext()

	require.NoError(t, svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeGrokRealtime))

	require.Contains(t, recorder.Body.String(), "realtime target")
	require.Contains(t, recorder.Body.String(), "realtime ws handshake ok")
	snapshot, ok := AccountTestTraceFrom(c)
	require.True(t, ok)
	require.NotNil(t, snapshot.FirstOutputAt)
	require.False(t, dialer.dialedAt.IsZero())
	require.False(t, snapshot.FirstOutputAt.Before(dialer.dialedAt),
		"first output is the handshake result, not the local target note written before dialing")
}
