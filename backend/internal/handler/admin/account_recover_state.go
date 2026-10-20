package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// RecoverStateRequest is the optional body of POST /admin/accounts/:id/recover-state.
//
// Without a body, or with schedulable omitted / null, recovery leaves the
// schedulable switch untouched (the historical behavior). true enables
// scheduling after recovery; false explicitly keeps the account paused.
type RecoverStateRequest struct {
	Schedulable *bool `json:"schedulable"`
}

// applyRecoveredSchedulable sets the schedulable switch after a successful
// recovery. It reports false after writing an error response; the recovery
// itself has already been persisted at that point, which the error's
// metadata (recovered=true) tells the client.
func (h *AccountHandler) applyRecoveredSchedulable(c *gin.Context, accountID int64, schedulable *bool) bool {
	if schedulable == nil {
		return true
	}
	if _, err := h.adminService.SetAccountSchedulable(c.Request.Context(), accountID, *schedulable); err != nil {
		response.ErrorFrom(c, service.ErrAccountRecoveredSchedulableFailed.
			WithCause(err).
			WithMetadata(map[string]string{"recovered": "true"}))
		return false
	}
	return true
}
