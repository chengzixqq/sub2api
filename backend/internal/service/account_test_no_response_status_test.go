//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// sequencedTestUpstream answers each request with the next response or error.
type sequencedTestUpstream struct {
	steps []sequencedTestUpstreamStep
}

type sequencedTestUpstreamStep struct {
	resp *http.Response
	err  error
}

func (u *sequencedTestUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	if len(u.steps) == 0 {
		return nil, errors.New("no mocked step")
	}
	step := u.steps[0]
	u.steps = u.steps[1:]
	return step.resp, step.err
}

func (u *sequencedTestUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func TestRunTestBackground_NoResponseFailureDropsEarlierStatus(t *testing.T) {
	account := adaptiveCNAccountTestAccount(311, PlatformZhipu)
	repo := &openAIAccountTestRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{
		accountsByID: map[int64]*Account{account.ID: account},
	}}
	svc := &AccountTestService{
		accountRepo: repo,
		httpUpstream: &sequencedTestUpstream{steps: []sequencedTestUpstreamStep{
			{resp: adaptiveCNChatTestResponse()},
			{err: errors.New("dial tcp 10.0.0.1:443: connect: connection refused")},
		}},
		cfg: rawChatCompletionsTestConfig(),
	}

	result, err := svc.RunTestBackground(context.Background(), account.ID, "glm-4.7")

	require.NoError(t, err)
	require.Equal(t, "failed", result.Status)
	require.Contains(t, result.ErrorMessage, "Adaptive Anthropic endpoint request failed")
	require.Nil(t, result.UpstreamStatusCode, "the chat endpoint's 200 must not be reported for a request that got no response")
	require.Equal(t, strPtrForErrorKindTest(AccountErrorKindOther), result.ErrorKind)
}

func TestAccountTestTrace_GrokVideoPollNetworkErrorDropsAcceptedStatus(t *testing.T) {
	account := &Account{
		ID: 312, Name: "grok-oauth-video-poll", Platform: PlatformGrok,
		Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "grok-access-token",
			"refresh_token": "grok-refresh-token",
			"expires_at":    time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
		},
	}
	repo := &mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}
	svc := &AccountTestService{
		accountRepo:       repo,
		grokTokenProvider: NewGrokTokenProvider(repo, nil),
		httpUpstream: &sequencedTestUpstream{steps: []sequencedTestUpstreamStep{
			{resp: &http.Response{
				StatusCode: http.StatusAccepted,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"request_id":"vid_1"}`)),
			}},
			{err: errors.New("read tcp 10.0.0.1:443: connection reset by peer")},
		}},
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/312/test", nil)

	err := svc.TestAccountConnection(c, account.ID, "grok-imagine-video", "bounce ball", AccountTestModeGrokVideo)

	require.Error(t, err)
	require.Contains(t, rec.Body.String(), "Grok video status failed")
	snapshot, ok := AccountTestTraceFrom(c)
	require.True(t, ok)
	require.Nil(t, snapshot.UpstreamStatus, "the create call's 202 must not be reported for a poll that got no response")
}

func TestAccountTestTrace_ResponseFailureKeepsItsStatus(t *testing.T) {
	account := adaptiveCNAccountTestAccount(313, PlatformZhipu)
	repo := &openAIAccountTestRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{
		accountsByID: map[int64]*Account{account.ID: account},
	}}
	svc := &AccountTestService{
		accountRepo: repo,
		httpUpstream: &sequencedTestUpstream{steps: []sequencedTestUpstreamStep{
			{resp: adaptiveCNChatTestResponse()},
			{resp: newJSONResponse(http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`)},
		}},
		cfg: rawChatCompletionsTestConfig(),
	}

	result, err := svc.RunTestBackground(context.Background(), account.ID, "glm-4.7")

	require.NoError(t, err)
	require.Equal(t, "failed", result.Status)
	require.Equal(t, intPtrForErrorKindTest(http.StatusTooManyRequests), result.UpstreamStatusCode)
	require.Equal(t, strPtrForErrorKindTest(AccountErrorKindRateLimit), result.ErrorKind)
}
