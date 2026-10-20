//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type syntheticCaptureRepo struct {
	AccountRepository
	account *Account
}

func (r syntheticCaptureRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	if r.account == nil || id != r.account.ID {
		return nil, ErrAccountNotFound
	}
	acc := *r.account
	return &acc, nil
}

func (r syntheticCaptureRepo) GetByIDScoped(ctx context.Context, id int64) (*Account, error) {
	return r.GetByID(ctx, id)
}

func syntheticCaptureAccount() *Account {
	return &Account{ID: 41, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Extra: map[string]any{"synthetic_ui_test": true}}
}

func TestRunTestCaptured_SyntheticAccountCapturesModelAndFirstToken(t *testing.T) {
	svc := &AccountTestService{accountRepo: syntheticCaptureRepo{account: syntheticCaptureAccount()}}

	for _, mode := range []string{AccountTestModeDefault, AccountTestModeCompact} {
		capture := svc.RunTestCaptured(context.Background(), AccountTestCaptureInput{AccountID: 41, Prompt: "hi", Mode: mode})

		require.True(t, capture.Success, mode)
		require.Equal(t, claude.DefaultTestModel, capture.Model)
		require.NotEmpty(t, capture.ResponseText)
		require.Empty(t, capture.ErrorMessage)
		require.NotNil(t, capture.FirstTokenMs)
		require.GreaterOrEqual(t, *capture.FirstTokenMs, int64(0))
		require.GreaterOrEqual(t, capture.LatencyMs, int64(0))
		require.Nil(t, capture.UpstreamStatusCode, "synthetic accounts never reach an upstream")
		require.False(t, capture.StartedAt.IsZero())
		require.False(t, capture.FinishedAt.Before(capture.StartedAt))
	}
}

func TestRunTestCaptured_MissingAccountFails(t *testing.T) {
	svc := &AccountTestService{accountRepo: syntheticCaptureRepo{}}

	capture := svc.RunTestCaptured(context.Background(), AccountTestCaptureInput{AccountID: 41, ModelID: "gpt-5"})

	require.False(t, capture.Success)
	require.Equal(t, "Account not found", capture.ErrorMessage)
	require.Equal(t, "gpt-5", capture.Model)
	require.Nil(t, capture.FirstTokenMs)
}

func TestCaptureFromContext_ManualSSEPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &AccountTestService{accountRepo: syntheticCaptureRepo{account: syntheticCaptureAccount()}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/41/test", nil)

	testErr := svc.TestAccountConnection(c, 41, "", "", AccountTestModeDefault)
	capture := CaptureFromContext(c, testErr, "")

	require.True(t, capture.Success)
	require.Equal(t, claude.DefaultTestModel, capture.Model)
	require.NotNil(t, capture.FirstTokenMs)
	responseText, errMsg := parseTestSSEOutput(rec.Body.String())
	require.Equal(t, responseText, capture.ResponseText)
	require.Empty(t, errMsg)
}

func TestCaptureFromContext_FailureUsesErrorText(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &AccountTestService{accountRepo: syntheticCaptureRepo{}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/41/test", nil)

	testErr := svc.TestAccountConnection(c, 41, "claude-x", "", AccountTestModeDefault)
	capture := CaptureFromContext(c, testErr, "claude-x")

	require.False(t, capture.Success)
	require.Equal(t, "Account not found", capture.ErrorMessage)
	require.Equal(t, "claude-x", capture.Model)
}
