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
//
// The switch is ordered so that a partial failure always leaves the account
// paused: false is written before recovery (pauseBeforeRecovery), true only
// after recovery succeeded (applyRecoveredSchedulable).
type RecoverStateRequest struct {
	Schedulable *bool `json:"schedulable"`
}

// pauseBeforeRecovery handles schedulable=false. Recovery can clear a rate
// limit, overload or temp-unschedulable state on an account whose switch is
// still on, so pausing afterwards would leave a window — or, if the write
// failed, a lasting state — in which the account takes traffic against the
// caller's explicit request. Writing the pause first closes both: when it
// fails nothing has been recovered and the whole request can be retried.
// It reports false after writing an error response.
func (h *AccountHandler) pauseBeforeRecovery(c *gin.Context, accountID int64, schedulable *bool) bool {
	if schedulable == nil || *schedulable {
		return true
	}
	if _, err := h.adminService.SetAccountSchedulable(c.Request.Context(), accountID, false); err != nil {
		response.ErrorFrom(c, err)
		return false
	}
	return true
}

// applyRecoveredSchedulable handles schedulable=true after a successful
// recovery. It reports false after writing an error response; the recovery
// itself has already been persisted at that point, which the error's
// metadata (recovered=true) tells the client.
func (h *AccountHandler) applyRecoveredSchedulable(c *gin.Context, accountID int64, schedulable *bool) bool {
	if schedulable == nil || !*schedulable {
		return true
	}
	if _, err := h.adminService.SetAccountSchedulable(c.Request.Context(), accountID, true); err != nil {
		response.ErrorFrom(c, service.ErrAccountRecoveredSchedulableFailed.
			WithCause(err).
			WithMetadata(map[string]string{"recovered": "true"}))
		return false
	}
	return true
}
