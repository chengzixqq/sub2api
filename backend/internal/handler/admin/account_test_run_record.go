package admin

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// accountTestRunRecordTimeout bounds the best-effort write of a manual test.
const accountTestRunRecordTimeout = 5 * time.Second

type accountTestRunRecorder interface {
	RecordManual(ctx context.Context, in service.ManualAccountTestRun) error
}

// SetAccountTestRunService attaches the store for interactive test outcomes.
func (h *AccountHandler) SetAccountTestRunService(runs *service.AccountTestRunService) {
	if h != nil && runs != nil {
		h.testRuns = runs
	}
}

// recordManualTestRun stores the outcome of the interactive SSE test as a
// source=manual run. It runs after the SSE stream is complete, detached from
// the request so a gone client is still recorded (as interrupted when the
// test failed); a write failure is only logged and never changes the stream.
func (h *AccountHandler) recordManualTestRun(c *gin.Context, accountID int64, requestedModel string, testErr error, recovery *service.SuccessfulTestRecoveryResult) {
	if h.testRuns == nil {
		return
	}
	reqCtx := c.Request.Context()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(reqCtx), accountTestRunRecordTimeout)
	defer cancel()
	err := h.testRuns.RecordManual(ctx, service.ManualAccountTestRun{
		AccountID:   accountID,
		Capture:     service.CaptureFromContext(c, testErr, requestedModel),
		Interrupted: reqCtx.Err() != nil,
		Recovery:    recovery,
		CreatedBy:   accountTestRunActor(c),
	})
	if err != nil {
		_ = c.Error(err)
	}
}
