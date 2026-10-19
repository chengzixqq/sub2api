package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// writeAccountTestResult appends the trailing test_result SSE event after the
// test and any recovery finished. before is the snapshot read for the scope
// check; the account is re-read through the scoped admin service.
func (h *AccountHandler) writeAccountTestResult(c *gin.Context, accountID int64, before *service.Account, success bool, recovery *service.SuccessfulTestRecoveryResult) {
	ctx := c.Request.Context()
	if ctx.Err() != nil {
		// The client is gone; nobody would read the event.
		return
	}
	after, err := h.adminService.GetAccount(ctx, accountID)
	if err != nil {
		_ = c.Error(err)
		after = nil
	}
	h.accountTestService.SendTestResult(c, service.BuildAccountTestResult(c, success, before, after, recovery))
}
