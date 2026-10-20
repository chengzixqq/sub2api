package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/gin-gonic/gin"
)

// AccountTestCaptureInput is one account test to run off the request path.
type AccountTestCaptureInput struct {
	AccountID int64
	ModelID   string
	Prompt    string
	Mode      string
}

// AccountTestCapture is the outcome of one account test: the parsed SSE output
// plus what the C07 trace observed. LatencyMs covers the whole test.
type AccountTestCapture struct {
	Success            bool
	Model              string
	ResponseText       string
	ErrorMessage       string
	LatencyMs          int64
	FirstTokenMs       *int64
	UpstreamStatusCode *int
	StartedAt          time.Time
	FinishedAt         time.Time
}

// RunTestCaptured runs an account test in memory (SSE recorded by
// httptest.NewRecorder) and returns the parsed outcome. ctx carries the
// deadline; without a workspace scope the account is read unscoped, so callers
// must check ownership first.
func (s *AccountTestService) RunTestCaptured(ctx context.Context, in AccountTestCaptureInput) AccountTestCapture {
	startedAt := time.Now()

	w := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(w)
	ginCtx.Request = (&http.Request{}).WithContext(ctx)
	ginCtx.Set(accountTestBackgroundKey, true)

	testErr := s.TestAccountConnection(ginCtx, in.AccountID, in.ModelID, in.Prompt, in.Mode)

	finishedAt := time.Now()
	responseText, errMsg := parseTestSSEOutput(w.Body.String())
	return buildAccountTestCapture(ginCtx, testErr, responseText, errMsg, in.ModelID, startedAt, finishedAt)
}

// CaptureFromContext builds the outcome of the interactive SSE test that just
// ran on c, from the trace TestAccountConnection recorded there.
func CaptureFromContext(c *gin.Context, testErr error, requestedModel string) AccountTestCapture {
	snapshot, _ := AccountTestTraceFrom(c)
	startedAt := snapshot.StartedAt
	finishedAt := time.Now()
	if snapshot.FinishedAt != nil {
		finishedAt = *snapshot.FinishedAt
	}
	if startedAt.IsZero() {
		startedAt = finishedAt
	}
	return buildAccountTestCapture(c, testErr, snapshot.ResponseText, snapshot.ErrorMessage, requestedModel, startedAt, finishedAt)
}

// buildAccountTestCapture keeps the success rule of RunTestBackground: a test
// fails when TestAccountConnection errs or an error event was written.
func buildAccountTestCapture(c *gin.Context, testErr error, responseText, errMsg, requestedModel string, startedAt, finishedAt time.Time) AccountTestCapture {
	success := testErr == nil && errMsg == ""
	if !success && errMsg == "" {
		errMsg = testErr.Error()
	}
	capture := AccountTestCapture{
		Success:            success,
		Model:              requestedModel,
		ResponseText:       responseText,
		ErrorMessage:       errMsg,
		LatencyMs:          nonNegativeMs(finishedAt.Sub(startedAt)),
		UpstreamStatusCode: accountTestRecordedUpstreamStatus(c, errMsg),
		StartedAt:          startedAt,
		FinishedAt:         finishedAt,
	}
	if snapshot, ok := AccountTestTraceFrom(c); ok {
		if snapshot.Model != "" {
			capture.Model = snapshot.Model
		}
		if snapshot.FirstOutputAt != nil {
			firstMs := nonNegativeMs(snapshot.FirstOutputAt.Sub(snapshot.StartedAt))
			capture.FirstTokenMs = &firstMs
		}
	}
	return capture
}
